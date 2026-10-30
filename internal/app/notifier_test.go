package app

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// notifierHarness builds a notifier over test knobs: three gates and the
// known-instance set are switchable mid-test, and every send is recorded.
type notifierHarness struct {
	n    *notifier
	sent []string

	master, low, exhausted bool
	known                  map[domain.InstanceID]struct{}
}

func newNotifierHarness(t *testing.T) *notifierHarness {
	t.Helper()
	h := &notifierHarness{
		master:    true,
		low:       true,
		exhausted: true,
		known:     map[domain.InstanceID]struct{}{"mm": {}},
	}
	h.n = newNotifier(slog.New(slog.NewTextHandler(io.Discard, nil)), notifierConfig{
		Master:    func() bool { return h.master },
		Low:       func() bool { return h.low },
		Exhausted: func() bool { return h.exhausted },
		Send: func(title, content string) {
			h.sent = append(h.sent, title+" | "+content)
		},
		Known: func() map[domain.InstanceID]struct{} { return h.known },
	})
	t.Cleanup(h.n.Close)

	return h
}

// quotaEvent builds a one-window event; handle takes them directly — the
// worker goroutine stays parked on its channel and never touches the
// latch state concurrently.
func qEvent(id, name string, windows ...domain.QuotaWindow) quotaEvent {
	return quotaEvent{id: domain.InstanceID(id), name: name, windows: windows}
}

func qWindow(id string, remaining float64) domain.QuotaWindow {
	return domain.QuotaWindow{ID: id, Name: id, RemainingFraction: remaining, UsedFraction: 1 - remaining}
}

// The full low-event lifecycle: entering the red band fires once, staying
// there stays silent, climbing back out re-arms, re-entering fires again.
func TestQuotaLowTripSilenceRearm(t *testing.T) {
	h := newNotifierHarness(t)

	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.12)))
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.08)))
	if len(h.sent) != 1 {
		t.Fatalf("sends = %v, want exactly the band-entry notification", h.sent)
	}
	if want := "MiniMax | interval: 8% remaining"; h.sent[0] != want {
		t.Errorf("send = %q, want %q", h.sent[0], want)
	}

	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.05)))
	if len(h.sent) != 1 {
		t.Errorf("deepening in-band fired again: %v", h.sent)
	}

	// Refill out of the band re-arms without a reset marker.
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.25)))
	if len(h.sent) != 1 {
		t.Errorf("re-arm itself fired: %v", h.sent)
	}

	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.08)))
	if len(h.sent) != 2 {
		t.Errorf("re-entry after re-arm sent %v, want exactly a second ping", h.sent)
	}
}

// A moved ResetAt is a new window generation: both edges clear even while
// still in-band.
func TestQuotaResetAtChangeRearms(t *testing.T) {
	h := newNotifierHarness(t)
	first, second := time.Unix(1770000000, 0), time.Unix(1770018000, 0)

	ev := func(reset time.Time) quotaEvent {
		w := qWindow("interval", 0.08)
		w.ResetAt = &reset

		return qEvent("mm", "MiniMax", w)
	}

	h.n.handle(ev(first))
	if len(h.sent) != 1 {
		t.Fatalf("first trip = %v", h.sent)
	}
	h.n.handle(ev(first))
	if len(h.sent) != 1 {
		t.Errorf("same generation fired twice: %v", h.sent)
	}
	h.n.handle(ev(second))
	if len(h.sent) != 2 {
		t.Errorf("new generation stayed silent: %v", h.sent)
	}
}

// Exhaustion fires once, subsumes low (falling straight through the band
// into zero pings once), and re-arms only after the window recovers past
// the red band.
func TestQuotaExhaustedLifecycle(t *testing.T) {
	h := newNotifierHarness(t)

	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.15)))
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0)))
	if len(h.sent) != 1 {
		t.Fatalf("zero fell through the band without subsuming low: %v", h.sent)
	}
	if want := "MiniMax | interval: exhausted"; h.sent[0] != want {
		t.Errorf("send = %q, want %q", h.sent[0], want)
	}

	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0)))
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.05)))
	if len(h.sent) != 1 {
		t.Errorf("exhaustion re-fired or low fired beside it: %v", h.sent)
	}

	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.25)))
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0)))
	if len(h.sent) != 2 {
		t.Errorf("exhaustion did not re-arm after recovery: %v", h.sent)
	}
}

// A window absent from a snapshot loses its latch: a reset that rotated
// the window set cannot carry state into the next generation.
func TestQuotaWindowVanishDropsLatch(t *testing.T) {
	h := newNotifierHarness(t)

	two := qEvent("mm", "MiniMax", qWindow("interval", 0.5), qWindow("weekly", 0.08))
	h.n.handle(two)
	if len(h.sent) != 1 {
		t.Fatalf("weekly trip = %v", h.sent)
	}

	// The weekly window vanished (reset rotated the set); the interval
	// window stays mid-band, silent.
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.5)))
	if len(h.sent) != 1 {
		t.Errorf("vanish handling fired: %v", h.sent)
	}

	// The weekly window is back — a fresh generation, so it trips again.
	h.n.handle(two)
	if len(h.sent) != 2 {
		t.Errorf("weekly latch survived the vanish: %v", h.sent)
	}
}

// Disabled or removed instances lose their latch state; re-enabling and
// re-entering the band fires fresh.
func TestQuotaLatchesDropWithInstance(t *testing.T) {
	h := newNotifierHarness(t)

	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.08)))
	if len(h.sent) != 1 {
		t.Fatalf("trip = %v", h.sent)
	}

	h.known = map[domain.InstanceID]struct{}{} // instance disabled or removed
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.5)))
	if len(h.n.latches) != 0 {
		t.Errorf("latches survived the instance: %v", h.n.latches)
	}

	h.known = map[domain.InstanceID]struct{}{"mm": {}}
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.08)))
	if len(h.sent) != 2 {
		t.Errorf("re-enabled instance stayed silent: %v", h.sent)
	}
}

// An instance outside the known set is dropped outright, and a genuinely
// unknown instance never latches.
func TestQuotaUnknownInstanceDropped(t *testing.T) {
	h := newNotifierHarness(t)

	h.n.handle(qEvent("ghost", "Ghost", qWindow("interval", 0.08)))
	if len(h.sent) != 0 || len(h.n.latches) != 0 {
		t.Errorf("unknown instance fired or latched: %v / %v", h.sent, h.n.latches)
	}
}

// Gating is audibility only: a silenced event still arms its edge, so
// enabling it mid-band cannot retro-fire; only the next transition after
// a re-arm is heard. The master switch gates the same way.
func TestQuotaGatingMatrix(t *testing.T) {
	t.Run("event toggle silenced then enabled", func(t *testing.T) {
		h := newNotifierHarness(t)
		h.low = false

		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.08)))
		if len(h.sent) != 0 {
			t.Fatalf("disabled event fired: %v", h.sent)
		}

		h.low = true
		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.05)))
		if len(h.sent) != 0 {
			t.Errorf("enabling mid-band retro-fired: %v", h.sent)
		}

		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.25)))
		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.08)))
		if len(h.sent) != 1 {
			t.Errorf("next transition after enable did not fire: %v", h.sent)
		}
	})

	t.Run("exhausted toggle", func(t *testing.T) {
		h := newNotifierHarness(t)
		h.exhausted = false
		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0)))
		if len(h.sent) != 0 {
			t.Fatalf("disabled exhausted fired: %v", h.sent)
		}

		h.exhausted = true
		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.25)))
		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0)))
		if len(h.sent) != 1 {
			t.Errorf("exhausted after enable did not fire at its next edge: %v", h.sent)
		}
	})

	t.Run("master switch", func(t *testing.T) {
		h := newNotifierHarness(t)
		h.master = false
		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0)))
		if len(h.sent) != 0 {
			t.Fatalf("master-off fired: %v", h.sent)
		}

		h.master = true
		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.25)))
		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0)))
		if len(h.sent) != 1 {
			t.Errorf("master-on did not fire at the next edge: %v", h.sent)
		}
	})
}

// The full pipeline: enqueue on a producer goroutine, worker delivers
// through the injected Send. Serves as the -race smoke for the handoff.
func TestNotifierPipelineDelivers(t *testing.T) {
	delivered := make(chan string, 4)
	n := newNotifier(slog.New(slog.NewTextHandler(io.Discard, nil)), notifierConfig{
		Master:    func() bool { return true },
		Low:       func() bool { return true },
		Exhausted: func() bool { return true },
		Send:      func(title, content string) { delivered <- title + " | " + content },
		Known: func() map[domain.InstanceID]struct{} {
			return map[domain.InstanceID]struct{}{"mm": {}}
		},
	})
	defer n.Close()

	n.Enqueue("MiniMax", "mm", []domain.QuotaWindow{qWindow("interval", 0.05)})
	select {
	case got := <-delivered:
		if want := "MiniMax | interval: 5% remaining"; got != want {
			t.Errorf("delivered = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification never delivered")
	}
}

// A full queue drops events instead of blocking the manager loop: with
// Send parked, one event sits in flight, the queue absorbs 16, and the
// 18th is dropped — Enqueue itself never blocks.
func TestNotifierQueueDropsInsteadOfBlocking(t *testing.T) {
	release := make(chan struct{})
	// stop releases a Send still parked when the test aborts early, so
	// Close can always join the worker.
	stop := make(chan struct{})
	var mu sync.Mutex
	count := 0
	n := newNotifier(slog.New(slog.NewTextHandler(io.Discard, nil)), notifierConfig{
		Master:    func() bool { return true },
		Low:       func() bool { return true },
		Exhausted: func() bool { return true },
		Send: func(title, content string) {
			select {
			case <-release:
			case <-stop:
			}
			mu.Lock()
			count++
			mu.Unlock()
		},
		Known: func() map[domain.InstanceID]struct{} {
			return map[domain.InstanceID]struct{}{"mm": {}}
		},
	})
	defer func() {
		close(stop)
		n.Close()
	}()

	// The worker pulls the first event and parks inside its Send; the
	// remaining enqueues must all return without blocking. Each event
	// carries its own fresh window so every one of them would fire — the
	// edge-triggered latches must not mask the drop being tested.
	filled := make(chan struct{})
	go func() {
		defer close(filled)
		for i := 0; i < notifyQueueLen+2; i++ {
			n.Enqueue("MiniMax", "mm", []domain.QuotaWindow{qWindow(fmt.Sprintf("w%d", i), 0.05)})
		}
	}()
	select {
	case <-filled:
	case <-time.After(time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}

	// Release the drain: 1 in-flight + 16 queued deliver, 1 was dropped.
	close(release)
	want := notifyQueueLen + 1
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := count >= want
		mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != want {
		t.Errorf("delivered %d notifications, want exactly %d (one in flight, queue %d, one dropped)",
			count, want, notifyQueueLen)
	}
}

// Notification text carries percentages only: rounding to a whole number,
// with a sub-one-percent floor instead of a misleading 0%.
func TestPercentRemaining(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.08, "8% remaining"},
		{0.0999, "10% remaining"},
		{0.0099, "<1% remaining"},
		{0.001, "<1% remaining"},
	}
	for _, c := range cases {
		if got := percentRemaining(c.in); got != c.want {
			t.Errorf("percentRemaining(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Missing display names degrade to identifiers, never to empty
// notification text.
func TestQuotaNameFallbacks(t *testing.T) {
	h := newNotifierHarness(t)

	h.n.handle(qEvent("mm", "", qWindow("", 0.05)))
	if len(h.sent) != 1 {
		t.Fatalf("sends = %v", h.sent)
	}
	if want := "mm | window: 5% remaining"; h.sent[0] != want {
		t.Errorf("fallback send = %q, want %q", h.sent[0], want)
	}
}
