package source

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/skobkin/llm-monitor/internal/domain"
)

// Source is one pollable data source adapter. Implementations MUST be
// pointer types: the manager detects adapter replacement by pointer
// identity and restarts the loop when the pointer changes.
type Source interface {
	// ID reports the domain source this adapter serves.
	ID() domain.SourceID
	// RequestTimeout is the per-request timeout applied on top of the
	// loop context (3s local, 10s remote by convention).
	RequestTimeout() time.Duration
	// Refresh performs one poll. It must honor ctx cancellation.
	Refresh(ctx context.Context) (domain.Snapshot, error)
}

// Registration is the desired state of one source.
type Registration struct {
	Source   Source
	Enabled  bool
	Interval time.Duration
}

// loop is the running state of one source's polling goroutine.
type loop struct {
	id       domain.SourceID
	source   Source
	interval time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
	trigger  chan struct{}
	done     chan struct{}
}

// Manager runs one independent polling loop per enabled source. Loops
// never share work: a slow or failing source cannot delay any other, two
// refreshes of the same source never overlap, and the last successful
// snapshot survives later failures (DECISIONS D6).
//
// Threading: Apply cancel-and-waits on stopped loops, so it may block for
// up to one in-flight request timeout and must not run on a UI thread.
// RefreshAll/States/Subscribe/Cancel are non-blocking and UI-safe.
type Manager struct {
	log *slog.Logger

	rootCtx    context.Context
	rootCancel context.CancelFunc
	wg         sync.WaitGroup

	mu       sync.Mutex
	loops    map[domain.SourceID]*loop
	runtimes map[domain.SourceID]*domain.Runtime

	subMu       sync.Mutex
	subscribers map[int]func(domain.SourceID)
	nextSubID   int
}

// NewManager creates a manager with no sources applied yet.
func NewManager(log *slog.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		log:         log,
		rootCtx:     ctx,
		rootCancel:  cancel,
		loops:       map[domain.SourceID]*loop{},
		runtimes:    map[domain.SourceID]*domain.Runtime{},
		subscribers: map[int]func(domain.SourceID){},
	}
}

// Apply reconciles the running loops with regs. Sources missing from regs
// are stopped. Newly enabled sources start with an immediate refresh.
// A source whose interval or adapter (pointer) changed restarts, handing
// its runtime — including any last-good snapshot — to the new loop.
// Stopped loops are waited for before returning.
func (m *Manager) Apply(regs map[domain.SourceID]Registration) {
	var stopped []*loop

	m.mu.Lock()
	for id := range m.loops {
		if _, wanted := regs[id]; !wanted {
			stopped = append(stopped, m.detachLoopLocked(id))
		}
	}
	for id, reg := range regs {
		l, running := m.loops[id]
		switch {
		case !running && reg.Enabled:
			m.startLoopLocked(id, reg)
		case running && !reg.Enabled:
			stopped = append(stopped, m.detachLoopLocked(id))
			m.disableRuntimeLocked(id)
		case running && reg.Enabled && (l.source != reg.Source || l.interval != reg.Interval):
			stopped = append(stopped, m.detachLoopLocked(id))
			m.startLoopLocked(id, reg)
		}
	}
	m.mu.Unlock()

	for _, l := range stopped {
		<-l.done
	}
}

// RefreshAll asks every running loop to refresh as soon as it is idle.
// Triggers coalesce: a loop that is already refreshing treats further
// calls as satisfied by the in-flight refresh, and a pending trigger is
// never queued twice. Safe to call from a UI callback.
func (m *Manager) RefreshAll() {
	m.mu.Lock()
	type target struct {
		l          *loop
		refreshing bool
	}
	targets := make([]target, 0, len(m.loops))
	for id, l := range m.loops {
		refreshing := false
		if rt, ok := m.runtimes[id]; ok {
			refreshing = rt.Refreshing
		}
		targets = append(targets, target{l: l, refreshing: refreshing})
	}
	m.mu.Unlock()

	for _, tgt := range targets {
		if tgt.refreshing {
			continue // the in-flight refresh satisfies this request
		}
		select {
		case tgt.l.trigger <- struct{}{}:
		default: // a trigger is already pending; coalesce
		}
	}
}

// States returns a copy of the current runtime of every known source.
func (m *Manager) States() map[domain.SourceID]domain.Runtime {
	m.mu.Lock()
	defer m.mu.Unlock()

	states := make(map[domain.SourceID]domain.Runtime, len(m.runtimes))
	for id, rt := range m.runtimes {
		states[id] = *rt
	}
	return states
}

// Subscribe registers fn to be called (from the loop goroutine) whenever
// a source's runtime changes. The returned cancel function unsubscribes.
func (m *Manager) Subscribe(fn func(domain.SourceID)) (cancel func()) {
	m.subMu.Lock()
	id := m.nextSubID
	m.nextSubID++
	m.subscribers[id] = fn
	m.subMu.Unlock()

	return func() {
		m.subMu.Lock()
		delete(m.subscribers, id)
		m.subMu.Unlock()
	}
}

// Cancel stops all loops and aborts in-flight requests without waiting.
// It is idempotent and safe from a UI callback.
func (m *Manager) Cancel() {
	m.rootCancel()
}

// Wait blocks until every loop goroutine has exited. Bounded by the
// per-request timeouts because cancellation aborts in-flight requests.
func (m *Manager) Wait() {
	m.wg.Wait()
}

// Stop cancels and waits. Convenience for tests and shutdown paths that
// are not on the UI thread.
func (m *Manager) Stop() {
	m.Cancel()
	m.Wait()
}

// startLoopLocked launches the polling goroutine for a source. The
// existing runtime (if any) is reused so last-good data survives
// interval/adapter restarts.
func (m *Manager) startLoopLocked(id domain.SourceID, reg Registration) {
	rt, ok := m.runtimes[id]
	if !ok {
		rt = &domain.Runtime{}
		m.runtimes[id] = rt
	}
	rt.Enabled = true

	ctx, cancel := context.WithCancel(m.rootCtx)
	l := &loop{
		id:       id,
		source:   reg.Source,
		interval: reg.Interval,
		ctx:      ctx,
		cancel:   cancel,
		trigger:  make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	m.loops[id] = l
	m.wg.Add(1)
	go m.runLoop(l)
}

// detachLoopLocked stops a loop's scheduling but leaves its runtime in
// place for whoever starts next. The caller must wait on l.done outside
// the lock.
func (m *Manager) detachLoopLocked(id domain.SourceID) *loop {
	l := m.loops[id]
	delete(m.loops, id)
	if l != nil {
		l.cancel()
	}
	return l
}

// disableRuntimeLocked resets the runtime of a disabled source: disabled
// sources hold no snapshot, so a later re-enable starts from Loading.
func (m *Manager) disableRuntimeLocked(id domain.SourceID) {
	m.runtimes[id] = &domain.Runtime{}
}

func (m *Manager) runLoop(l *loop) {
	defer m.wg.Done()
	defer close(l.done)
	for {
		m.refreshOnce(l)

		timer := time.NewTimer(l.interval)
		select {
		case <-l.ctx.Done():
			timer.Stop()
			return
		case <-l.trigger:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// refreshOnce runs a single poll with the source's per-request timeout,
// updating the runtime and notifying subscribers. A failed refresh never
// clears the stored snapshot. Refreshes cancelled by loop teardown leave
// the runtime untouched.
func (m *Manager) refreshOnce(l *loop) {
	now := time.Now()
	m.mu.Lock()
	rt := m.runtimes[l.id]
	rt.Refreshing = true
	rt.LastAttemptAt = now
	m.mu.Unlock()
	m.notify(l.id)

	rctx, cancel := context.WithTimeout(l.ctx, l.source.RequestTimeout())
	snap, err := l.source.Refresh(rctx)
	cancel()

	if l.ctx.Err() != nil {
		// The loop was stopped (shutdown or restart); the result is
		// obsolete and the runtime now belongs to the next loop.
		m.mu.Lock()
		rt.Refreshing = false
		m.mu.Unlock()
		return
	}

	now = time.Now()
	m.mu.Lock()
	rt.Refreshing = false
	rt.LastAttemptAt = now
	if err != nil {
		rt.LastError = err
		m.log.Warn("refresh failed", "source", string(l.id), "error", err)
	} else {
		rt.LastError = nil
		rt.LastSuccessAt = now
		rt.Snapshot = snap
	}
	m.mu.Unlock()
	m.notify(l.id)
}

func (m *Manager) notify(id domain.SourceID) {
	m.subMu.Lock()
	subs := make([]func(domain.SourceID), 0, len(m.subscribers))
	for _, fn := range m.subscribers {
		subs = append(subs, fn)
	}
	m.subMu.Unlock()
	for _, fn := range subs {
		fn(id)
	}
}
