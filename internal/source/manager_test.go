package source

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// fakeSource is a scriptable Source for manager tests.
type fakeSource struct {
	id      domain.SourceKind
	timeout time.Duration

	mu       sync.Mutex
	behavior func(ctx context.Context) (domain.Snapshot, error)

	refreshes   atomic.Int64
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
}

func newFakeSource(id domain.SourceKind, behavior func(ctx context.Context) (domain.Snapshot, error)) *fakeSource {
	return &fakeSource{id: id, timeout: 2 * time.Second, behavior: behavior}
}

func (f *fakeSource) Kind() domain.SourceKind       { return f.id }
func (f *fakeSource) RequestTimeout() time.Duration { return f.timeout }

func (f *fakeSource) Refresh(ctx context.Context) (domain.Snapshot, error) {
	cur := f.inFlight.Add(1)
	for {
		max := f.maxInFlight.Load()
		if cur <= max || f.maxInFlight.CompareAndSwap(max, cur) {
			break
		}
	}
	defer f.inFlight.Add(-1)
	f.refreshes.Add(1)

	f.mu.Lock()
	behavior := f.behavior
	f.mu.Unlock()

	return behavior(ctx)
}

func (f *fakeSource) setBehavior(b func(ctx context.Context) (domain.Snapshot, error)) {
	f.mu.Lock()
	f.behavior = b
	f.mu.Unlock()
}

type fakeSnap struct {
	id domain.SourceKind
	at time.Time
}

func (s fakeSnap) SnapshotKind() domain.SourceKind { return s.id }
func (s fakeSnap) Timestamp() time.Time            { return s.at }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func reg(id domain.InstanceID, src Source, enabled bool, interval time.Duration) map[domain.InstanceID]Registration {
	return map[domain.InstanceID]Registration{
		id: {Kind: src.Kind(), Source: src, Enabled: enabled, Interval: interval},
	}
}

func snapOf(kind domain.SourceKind) domain.Snapshot {
	return fakeSnap{id: kind, at: time.Now()}
}

func TestManagerStaleRetentionAndRecovery(t *testing.T) {
	const inst = domain.InstanceID("minimax-1")
	src := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindMiniMax), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(inst, src, true, 10*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return src.refreshes.Load() >= 1 })

	// Fail everything afterwards.
	src.setBehavior(func(ctx context.Context) (domain.Snapshot, error) {
		return nil, errors.New("provider down")
	})
	waitFor(t, 500*time.Millisecond, func() bool {
		st := m.States()[inst]

		return st.LastError != nil
	})

	st := m.States()[inst]
	if st.Snapshot == nil {
		t.Fatal("last successful snapshot must be retained after failures")
	}
	if got := st.DeriveState(); got != domain.StateStale {
		t.Errorf("state = %s, want Stale", got)
	}

	// Recover.
	src.setBehavior(func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindMiniMax), nil
	})
	waitFor(t, 500*time.Millisecond, func() bool {
		st := m.States()[inst]

		return st.LastError == nil
	})
	if got := m.States()[inst].DeriveState(); got != domain.StateHealthy {
		t.Errorf("state after recovery = %s, want Healthy", got)
	}
}

func TestManagerNoOverlapAndCoalescing(t *testing.T) {
	const inst = domain.InstanceID("zai-1")
	// Long interval so scheduled ticks never fire; only manual triggers
	// produce refreshes in the measurement window.
	src := newFakeSource(domain.KindZAI, func(ctx context.Context) (domain.Snapshot, error) {
		time.Sleep(120 * time.Millisecond)

		return snapOf(domain.KindZAI), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(inst, src, true, 10*time.Second))
	waitFor(t, 500*time.Millisecond, func() bool { return src.refreshes.Load() >= 1 })
	time.Sleep(30 * time.Millisecond) // first refresh is now in flight

	// Five triggers while a refresh is in flight must coalesce to at most
	// one deferred refresh, never an in-flight overlap.
	for i := 0; i < 5; i++ {
		m.RefreshAll()
	}
	time.Sleep(500 * time.Millisecond)

	if got := src.maxInFlight.Load(); got > 1 {
		t.Errorf("overlapping refreshes detected: max in-flight = %d", got)
	}

	base := src.refreshes.Load()
	m.RefreshAll()
	m.RefreshAll()
	m.RefreshAll()
	time.Sleep(600 * time.Millisecond)
	// Expect exactly 1: one immediate refresh, the other two calls
	// coalesced away. A benign microscopic race (trigger landing between
	// the loop consuming the wakeup and setting Refreshing) can add one
	// deferred refresh; overlap is still impossible.
	if extra := src.refreshes.Load() - base; extra > 2 {
		t.Errorf("coalescing failed: %d extra refreshes for 3 triggers, want 1", extra)
	}
}

func TestManagerIndependentLoops(t *testing.T) {
	var fastRefreshes atomic.Int64
	slow := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		time.Sleep(400 * time.Millisecond) // blocks a full refresh

		return snapOf(domain.KindMiniMax), nil
	})
	fast := &fakeSource{
		id:      domain.KindZAI,
		timeout: 2 * time.Second,
		behavior: func(ctx context.Context) (domain.Snapshot, error) {
			fastRefreshes.Add(1)

			return snapOf(domain.KindZAI), nil
		},
	}

	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(map[domain.InstanceID]Registration{
		domain.InstanceID("minimax-slow"): {Kind: domain.KindMiniMax, Source: slow, Enabled: true, Interval: 100 * time.Millisecond},
		domain.InstanceID("zai-fast"):     {Kind: domain.KindZAI, Source: fast, Enabled: true, Interval: 20 * time.Millisecond},
	})

	time.Sleep(300 * time.Millisecond)
	if fastRefreshes.Load() < 3 {
		t.Errorf("fast source blocked by slow source: only %d refreshes", fastRefreshes.Load())
	}
}

func TestManagerTwoSameKindInstancesAreIndependent(t *testing.T) {
	const (
		instA = domain.InstanceID("mm-a")
		instB = domain.InstanceID("mm-b")
	)
	a := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindMiniMax), nil
	})
	b := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindMiniMax), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	regs := func() map[domain.InstanceID]Registration {
		return map[domain.InstanceID]Registration{
			instA: {Kind: domain.KindMiniMax, Source: a, Enabled: true, Interval: 20 * time.Millisecond},
			instB: {Kind: domain.KindMiniMax, Source: b, Enabled: true, Interval: 20 * time.Millisecond},
		}
	}
	m.Apply(regs())
	waitFor(t, 500*time.Millisecond, func() bool {
		return a.refreshes.Load() >= 1 && b.refreshes.Load() >= 1
	})

	states := m.States()
	sa, okA := states[instA]
	sb, okB := states[instB]
	if !okA || !okB {
		t.Fatalf("both same-kind instances need their own runtime: a=%v b=%v", okA, okB)
	}
	if sa.Snapshot == nil || sb.Snapshot == nil {
		t.Error("both instances must collect snapshots independently")
	}

	// Removing one instance cancels only its loop; the other keeps polling,
	// and the removed instance leaves no runtime state behind.
	baseB := b.refreshes.Load()
	only := regs()
	delete(only, instA)
	m.Apply(only)
	waitFor(t, 500*time.Millisecond, func() bool { return b.refreshes.Load() > baseB })
	if frozen := a.refreshes.Load(); frozen > 0 {
		time.Sleep(120 * time.Millisecond)
		if m2 := a.refreshes.Load(); m2 != frozen {
			t.Error("removed instance's loop kept running")
		}
	}
	if _, present := m.States()[instA]; present {
		t.Error("removed instance must leave no runtime state behind")
	}
}

func TestManagerDisabledSourceNeverRefreshes(t *testing.T) {
	const inst = domain.InstanceID("swap-1")
	src := newFakeSource(domain.KindLlamaSwap, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindLlamaSwap), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(inst, src, false, 10*time.Millisecond))
	time.Sleep(120 * time.Millisecond)

	if src.refreshes.Load() != 0 {
		t.Errorf("disabled source refreshed %d times", src.refreshes.Load())
	}
	// Apply with enabled=false and no prior loop creates no runtime at
	// all; States() simply omits the instance.
	if _, present := m.States()[inst]; present {
		t.Errorf("disabled source unexpectedly has a runtime entry")
	}
}

func TestManagerRestartPreservesSnapshot(t *testing.T) {
	const inst = domain.InstanceID("gpu-1")
	src := newFakeSource(domain.KindAMDGPUWeb, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindAMDGPUWeb), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(inst, src, true, 20*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool {
		return m.States()[inst].Snapshot != nil
	})

	// Interval change restarts the loop with the same runtime.
	m.Apply(reg(inst, src, true, 200*time.Millisecond))

	st := m.States()[inst]
	if st.Snapshot == nil {
		t.Fatal("snapshot lost across interval restart")
	}
	if !st.Enabled {
		t.Error("source must remain enabled across restart")
	}

	// The new loop still refreshes.
	waitFor(t, 1*time.Second, func() bool {
		return src.refreshes.Load() >= 2
	})
}

func TestManagerIdenticalApplyDoesNotRestart(t *testing.T) {
	const inst = domain.InstanceID("gpu-1")
	src := newFakeSource(domain.KindAMDGPUWeb, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindAMDGPUWeb), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	// Long interval: after the initial refresh, no scheduled tick fires in
	// the measurement window, so any extra refresh means a restart.
	m.Apply(reg(inst, src, true, 500*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return src.refreshes.Load() >= 1 })
	time.Sleep(50 * time.Millisecond)

	base := src.refreshes.Load()
	m.Apply(reg(inst, src, true, 500*time.Millisecond)) // identical re-Apply
	time.Sleep(200 * time.Millisecond)
	if got := src.refreshes.Load(); got != base {
		t.Errorf("identical re-Apply restarted the loop (%d -> %d refreshes); rename/reorder must not disturb polling", base, got)
	}
}

func TestManagerAdapterSwapRestartsAndClearsSnapshot(t *testing.T) {
	const inst = domain.InstanceID("zai-1")
	first := newFakeSource(domain.KindZAI, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindZAI), nil
	})
	release := make(chan struct{})
	second := newFakeSource(domain.KindZAI, func(ctx context.Context) (domain.Snapshot, error) {
		<-release // hold the first refresh until the swap is asserted

		return snapOf(domain.KindZAI), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(inst, first, true, 20*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return first.refreshes.Load() >= 1 })
	waitFor(t, 500*time.Millisecond, func() bool {
		return m.States()[inst].Snapshot != nil
	})

	m.Apply(reg(inst, second, true, 20*time.Millisecond))
	// The new loop's first refresh is blocked, so any snapshot visible now
	// would be the old endpoint's — it must have been cleared.
	if st := m.States()[inst]; st.Snapshot != nil {
		t.Fatal("old endpoint's snapshot must not pass as the new configuration's data")
	}

	close(release)
	waitFor(t, 500*time.Millisecond, func() bool { return second.refreshes.Load() >= 1 })
	if got := first.refreshes.Load(); got == 0 {
		t.Error("first adapter never ran")
	}
	waitFor(t, 500*time.Millisecond, func() bool {
		return m.States()[inst].Snapshot != nil
	})
}

func TestManagerRemovesSourcesAbsentFromApply(t *testing.T) {
	const inst = domain.InstanceID("minimax-1")
	src := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindMiniMax), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(inst, src, true, 20*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return src.refreshes.Load() >= 1 })

	m.Apply(map[domain.InstanceID]Registration{}) // empty desired set
	time.Sleep(100 * time.Millisecond)

	if _, present := m.States()[inst]; present {
		t.Error("removed instance must leave no runtime state behind")
	}
	frozen := src.refreshes.Load()
	time.Sleep(120 * time.Millisecond)
	if src.refreshes.Load() != frozen {
		t.Error("loop kept running after being removed by Apply")
	}
}

func TestManagerDisableThenReenableStartsFresh(t *testing.T) {
	const inst = domain.InstanceID("zai-1")
	src := newFakeSource(domain.KindZAI, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindZAI), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(inst, src, true, 20*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool {
		return m.States()[inst].Snapshot != nil
	})

	m.Apply(reg(inst, src, false, 20*time.Millisecond))
	st, ok := m.States()[inst]
	if !ok || st.Enabled || st.Snapshot != nil {
		t.Fatalf("after disable: present=%v state=%+v", ok, st)
	}

	m.Apply(reg(inst, src, true, 20*time.Millisecond))
	st = m.States()[inst]
	if !st.Enabled {
		t.Fatal("re-enable must set Enabled")
	}
	if st.Snapshot != nil {
		t.Error("re-enabled source must not resurrect the old snapshot")
	}
	waitFor(t, 500*time.Millisecond, func() bool {
		return m.States()[inst].Snapshot != nil
	})
}

func TestManagerRefreshSingleInstance(t *testing.T) {
	const (
		instA = domain.InstanceID("mm-a")
		instB = domain.InstanceID("mm-b")
	)
	a := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindMiniMax), nil
	})
	b := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindMiniMax), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	// Long interval: no scheduled ticks, only Refresh() triggers.
	m.Apply(map[domain.InstanceID]Registration{
		instA: {Kind: domain.KindMiniMax, Source: a, Enabled: true, Interval: 10 * time.Second},
		instB: {Kind: domain.KindMiniMax, Source: b, Enabled: true, Interval: 10 * time.Second},
	})
	waitFor(t, 1*time.Second, func() bool {
		return a.refreshes.Load() >= 1 && b.refreshes.Load() >= 1
	})

	baseA, baseB := a.refreshes.Load(), b.refreshes.Load()
	m.Refresh(instA)
	waitFor(t, 1*time.Second, func() bool { return a.refreshes.Load() > baseA })
	time.Sleep(100 * time.Millisecond)
	if got := b.refreshes.Load(); got != baseB {
		t.Errorf("Refresh(one instance) also refreshed another (%d -> %d)", baseB, got)
	}

	m.Refresh(domain.InstanceID("unknown")) // must be a safe no-op
	m.Refresh(instB)                        // disabled/absent paths covered above; running path here
	waitFor(t, 1*time.Second, func() bool { return b.refreshes.Load() > baseB })
}

func TestManagerStopCancelsInFlight(t *testing.T) {
	const inst = domain.InstanceID("minimax-1")
	entered := make(chan struct{})
	src := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		close(entered)
		<-ctx.Done() // hang until cancelled

		return nil, ctx.Err()
	})
	m := NewManager(quietLogger())

	m.Apply(reg(inst, src, true, 10*time.Millisecond))
	<-entered

	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel the in-flight refresh")
	}
}

func TestManagerSubscribe(t *testing.T) {
	const inst = domain.InstanceID("minimax-1")
	src := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindMiniMax), nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	var mu sync.Mutex
	events := map[domain.InstanceID]int{}
	cancel := m.Subscribe(func(id domain.InstanceID) {
		mu.Lock()
		events[id]++
		mu.Unlock()
	})
	defer cancel()

	// Long interval so no scheduled tick can add refreshes in this window:
	// a second event would mean the in-flight start was notified too.
	m.Apply(reg(inst, src, true, 10*time.Second))
	waitFor(t, 1*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return events[inst] >= 1 // completion only; the in-flight start is not notified
	})

	// Long interval: no scheduled tick can add refreshes in this window, so
	// a second event now would mean the in-flight start was notified too.
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	got := events[inst]
	mu.Unlock()
	refreshes := src.refreshes.Load()
	if got != 1 || refreshes != 1 {
		t.Errorf("events = %d for %d refreshes, want exactly one completion notification per refresh", got, refreshes)
	}

	cancel()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	after := events[inst]
	mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	final := events[inst]
	mu.Unlock()
	if final != after {
		t.Errorf("subscriber still receiving events after cancel (%d -> %d)", after, final)
	}
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// closerSource is a fakeSource that also implements Closer, counting Close
// calls so tests can prove when the manager disposes a source.
type closerSource struct {
	*fakeSource
	mu    sync.Mutex
	calls int
}

func newCloserSource(kind domain.SourceKind) *closerSource {
	return &closerSource{fakeSource: newFakeSource(kind, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(kind), nil
	})}
}

func (c *closerSource) Close() {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
}

func (c *closerSource) closeCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.calls
}

// plainSource is deliberately not a Closer: existing kinds must be untouched
// by the disposal hook.
type plainSource struct {
	*fakeSource
}

func TestManagerClosesRemovedSource(t *testing.T) {
	src := newCloserSource(domain.KindCodex)
	m := NewManager(quietLogger())
	m.Apply(reg("codex-1", src, true, 10*time.Millisecond))
	if src.closeCalls() != 0 {
		t.Fatal("applying a source must not close it")
	}

	m.Apply(map[domain.InstanceID]Registration{})

	if got := src.closeCalls(); got != 1 {
		t.Errorf("Close calls after removal = %d, want 1", got)
	}
}

func TestManagerClosesDisabledSource(t *testing.T) {
	const inst = domain.InstanceID("codex-1")
	src := newCloserSource(domain.KindCodex)
	m := NewManager(quietLogger())
	m.Apply(reg(inst, src, true, 10*time.Millisecond))

	// A disabled instance stays registered with Enabled false: the loop is
	// gone and a later Wait can no longer reach the source, so the disable
	// itself must dispose it.
	m.Apply(reg(inst, src, false, 10*time.Millisecond))

	if got := src.closeCalls(); got != 1 {
		t.Errorf("Close calls after disable = %d, want 1", got)
	}

	// Re-enabling the same adapter restarts its resources lazily; the
	// adapter contract allows a closed source to be used again.
	m.Apply(reg(inst, src, true, 10*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return src.refreshes.Load() >= 1 })
	if got := src.closeCalls(); got != 1 {
		t.Errorf("Close calls after re-enable = %d, want still 1", got)
	}
}

func TestManagerClosesReplacedSourceOnly(t *testing.T) {
	const inst = domain.InstanceID("codex-1")
	old := newCloserSource(domain.KindCodex)
	replacement := newCloserSource(domain.KindCodex)
	m := NewManager(quietLogger())
	m.Apply(reg(inst, old, true, 10*time.Millisecond))

	m.Apply(reg(inst, replacement, true, 10*time.Millisecond))

	if got := old.closeCalls(); got != 1 {
		t.Errorf("old adapter Close calls = %d, want 1", got)
	}
	if got := replacement.closeCalls(); got != 0 {
		t.Errorf("replacement adapter Close calls = %d, want 0", got)
	}
}

func TestManagerKeepsSourceOnIntervalChange(t *testing.T) {
	const inst = domain.InstanceID("codex-1")
	src := newCloserSource(domain.KindCodex)
	m := NewManager(quietLogger())
	m.Apply(reg(inst, src, true, 10*time.Millisecond))

	// An interval-only restart reuses the same enabled adapter: its
	// long-lived resources must survive the loop restart.
	m.Apply(reg(inst, src, true, 20*time.Millisecond))

	if got := src.closeCalls(); got != 0 {
		t.Errorf("Close calls after interval change = %d, want 0", got)
	}
	waitFor(t, 500*time.Millisecond, func() bool { return src.refreshes.Load() >= 2 })
	if got := src.closeCalls(); got != 0 {
		t.Errorf("Close calls after restart refreshes = %d, want 0", got)
	}
}

func TestManagerWaitClosesSurvivorsOnce(t *testing.T) {
	src := newCloserSource(domain.KindCodex)
	plain := &plainSource{fakeSource: newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return snapOf(domain.KindMiniMax), nil
	})}
	m := NewManager(quietLogger())
	m.Apply(reg("codex-1", src, true, 10*time.Millisecond))
	m.Apply(reg("mm-1", plain, true, 10*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return src.refreshes.Load() >= 1 })

	m.Cancel()
	m.Wait()
	m.Wait() // idempotent: the close phase must run exactly once

	if got := src.closeCalls(); got != 1 {
		t.Errorf("Close calls after Wait = %d, want exactly 1", got)
	}
	// A non-Closer source has nothing to dispose and no way to observe this;
	// its loop must simply have stopped.
	if got := plain.refreshes.Load(); got < 1 {
		t.Errorf("plain source refreshes = %d, want at least 1 before shutdown", got)
	}
}
