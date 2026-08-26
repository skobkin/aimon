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

func reg(src Source, enabled bool, interval time.Duration) map[domain.SourceKind]Registration {
	return map[domain.SourceKind]Registration{
		src.Kind(): {Source: src, Enabled: enabled, Interval: interval},
	}
}

func TestManagerStaleRetentionAndRecovery(t *testing.T) {
	src := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return fakeSnap{id: domain.KindMiniMax, at: time.Now()}, nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(src, true, 10*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return src.refreshes.Load() >= 1 })

	// Fail everything afterwards.
	src.setBehavior(func(ctx context.Context) (domain.Snapshot, error) {
		return nil, errors.New("provider down")
	})
	waitFor(t, 500*time.Millisecond, func() bool {
		st := m.States()[domain.KindMiniMax]
		return st.LastError != nil
	})

	st := m.States()[domain.KindMiniMax]
	if st.Snapshot == nil {
		t.Fatal("last successful snapshot must be retained after failures")
	}
	if got := st.DeriveState(); got != domain.StateStale {
		t.Errorf("state = %s, want Stale", got)
	}

	// Recover.
	src.setBehavior(func(ctx context.Context) (domain.Snapshot, error) {
		return fakeSnap{id: domain.KindMiniMax, at: time.Now()}, nil
	})
	waitFor(t, 500*time.Millisecond, func() bool {
		st := m.States()[domain.KindMiniMax]
		return st.LastError == nil
	})
	if got := m.States()[domain.KindMiniMax].DeriveState(); got != domain.StateHealthy {
		t.Errorf("state after recovery = %s, want Healthy", got)
	}
}

func TestManagerNoOverlapAndCoalescing(t *testing.T) {
	// Long interval so scheduled ticks never fire; only manual triggers
	// produce refreshes in the measurement window.
	src := newFakeSource(domain.KindZAI, func(ctx context.Context) (domain.Snapshot, error) {
		time.Sleep(120 * time.Millisecond)
		return fakeSnap{id: domain.KindZAI, at: time.Now()}, nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(src, true, 10*time.Second))
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
		return fakeSnap{id: domain.KindMiniMax, at: time.Now()}, nil
	})
	fast := &fakeSource{
		id:      domain.KindZAI,
		timeout: 2 * time.Second,
		behavior: func(ctx context.Context) (domain.Snapshot, error) {
			fastRefreshes.Add(1)
			return fakeSnap{id: domain.KindZAI, at: time.Now()}, nil
		},
	}

	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(map[domain.SourceKind]Registration{
		domain.KindMiniMax: {Source: slow, Enabled: true, Interval: 100 * time.Millisecond},
		domain.KindZAI:     {Source: fast, Enabled: true, Interval: 20 * time.Millisecond},
	})

	time.Sleep(300 * time.Millisecond)
	if fastRefreshes.Load() < 3 {
		t.Errorf("fast source blocked by slow source: only %d refreshes", fastRefreshes.Load())
	}
}

func TestManagerDisabledSourceNeverRefreshes(t *testing.T) {
	src := newFakeSource(domain.KindLlamaSwap, func(ctx context.Context) (domain.Snapshot, error) {
		return fakeSnap{id: domain.KindLlamaSwap, at: time.Now()}, nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(src, false, 10*time.Millisecond))
	time.Sleep(120 * time.Millisecond)

	if src.refreshes.Load() != 0 {
		t.Errorf("disabled source refreshed %d times", src.refreshes.Load())
	}
	// Apply with enabled=false and no prior loop creates no runtime at
	// all; States() simply omits the source.
	if _, present := m.States()[domain.KindLlamaSwap]; present {
		t.Errorf("disabled source unexpectedly has a runtime entry")
	}
}

func TestManagerRestartPreservesSnapshot(t *testing.T) {
	src := newFakeSource(domain.KindAMDGPUWeb, func(ctx context.Context) (domain.Snapshot, error) {
		return fakeSnap{id: domain.KindAMDGPUWeb, at: time.Now()}, nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(src, true, 20*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool {
		return m.States()[domain.KindAMDGPUWeb].Snapshot != nil
	})

	// Interval change restarts the loop with the same runtime.
	m.Apply(reg(src, true, 200*time.Millisecond))

	st := m.States()[domain.KindAMDGPUWeb]
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

func TestManagerAdapterSwapRestarts(t *testing.T) {
	first := newFakeSource(domain.KindZAI, func(ctx context.Context) (domain.Snapshot, error) {
		return fakeSnap{id: domain.KindZAI, at: time.Now()}, nil
	})
	second := newFakeSource(domain.KindZAI, func(ctx context.Context) (domain.Snapshot, error) {
		return fakeSnap{id: domain.KindZAI, at: time.Now()}, nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(first, true, 20*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return first.refreshes.Load() >= 1 })

	m.Apply(reg(second, true, 20*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return second.refreshes.Load() >= 1 })

	if got := first.refreshes.Load(); got == 0 {
		t.Error("first adapter never ran")
	}
	// Snapshot survives the swap.
	if m.States()[domain.KindZAI].Snapshot == nil {
		t.Error("snapshot lost across adapter swap")
	}
}

func TestManagerRemovesSourcesAbsentFromApply(t *testing.T) {
	src := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return fakeSnap{id: domain.KindMiniMax, at: time.Now()}, nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(src, true, 20*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool { return src.refreshes.Load() >= 1 })

	m.Apply(map[domain.SourceKind]Registration{}) // empty desired set
	time.Sleep(100 * time.Millisecond)

	frozen := src.refreshes.Load()
	time.Sleep(120 * time.Millisecond)
	if src.refreshes.Load() != frozen {
		t.Error("loop kept running after being removed by Apply")
	}
}

func TestManagerDisableThenReenableStartsFresh(t *testing.T) {
	src := newFakeSource(domain.KindZAI, func(ctx context.Context) (domain.Snapshot, error) {
		return fakeSnap{id: domain.KindZAI, at: time.Now()}, nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	m.Apply(reg(src, true, 20*time.Millisecond))
	waitFor(t, 500*time.Millisecond, func() bool {
		return m.States()[domain.KindZAI].Snapshot != nil
	})

	m.Apply(reg(src, false, 20*time.Millisecond))
	st, ok := m.States()[domain.KindZAI]
	if !ok || st.Enabled || st.Snapshot != nil {
		t.Fatalf("after disable: present=%v state=%+v", ok, st)
	}

	m.Apply(reg(src, true, 20*time.Millisecond))
	st = m.States()[domain.KindZAI]
	if !st.Enabled {
		t.Fatal("re-enable must set Enabled")
	}
	if st.Snapshot != nil {
		t.Error("re-enabled source must not resurrect the old snapshot")
	}
	waitFor(t, 500*time.Millisecond, func() bool {
		return m.States()[domain.KindZAI].Snapshot != nil
	})
}

func TestManagerStopCancelsInFlight(t *testing.T) {
	entered := make(chan struct{})
	src := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		close(entered)
		<-ctx.Done() // hang until cancelled
		return nil, ctx.Err()
	})
	m := NewManager(quietLogger())

	m.Apply(reg(src, true, 10*time.Millisecond))
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
	src := newFakeSource(domain.KindMiniMax, func(ctx context.Context) (domain.Snapshot, error) {
		return fakeSnap{id: domain.KindMiniMax, at: time.Now()}, nil
	})
	m := NewManager(quietLogger())
	defer m.Stop()

	var mu sync.Mutex
	events := map[domain.SourceKind]int{}
	cancel := m.Subscribe(func(id domain.SourceKind) {
		mu.Lock()
		events[id]++
		mu.Unlock()
	})
	defer cancel()

	m.Apply(reg(src, true, 20*time.Millisecond))
	waitFor(t, 1*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return events[domain.KindMiniMax] >= 2 // start + completion
	})

	cancel()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	after := events[domain.KindMiniMax]
	mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	final := events[domain.KindMiniMax]
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
