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

// notifierHarness builds a notifier over test knobs: the four gates and
// the known-instance set are switchable mid-test, and every send is
// recorded.
type notifierHarness struct {
	n    *notifier
	sent []string

	master, low, exhausted, reset bool
	known                         map[domain.InstanceID]struct{}
}

func newNotifierHarness(t *testing.T) *notifierHarness {
	t.Helper()
	h := &notifierHarness{
		master:    true,
		low:       true,
		exhausted: true,
		reset:     true,
		known:     map[domain.InstanceID]struct{}{"mm": {}},
	}
	h.n = newNotifier(slog.New(slog.NewTextHandler(io.Discard, nil)), notifierConfig{
		Master:    func() bool { return h.master },
		Low:       func() bool { return h.low },
		Exhausted: func() bool { return h.exhausted },
		Reset:     func() bool { return h.reset },
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

// A ResetAt moved beyond the jitter tolerance is a new window generation:
// the used generation's "reset" fires once and both edges re-arm even
// while still in-band. Sub-tolerance movement — a countdown-derived reset
// time re-derived every poll — stays silent.
func TestQuotaResetAtChangeRearms(t *testing.T) {
	h := newNotifierHarness(t)
	first := time.Unix(1770000000, 0)

	ev := func(reset time.Time) quotaEvent {
		w := qWindow("interval", 0.08)
		w.ResetAt = &reset

		return qEvent("mm", "MiniMax", w)
	}

	h.n.handle(ev(first))
	if len(h.sent) != 1 {
		t.Fatalf("first trip = %v", h.sent)
	}

	// Countdown jitter: the same generation re-derived seconds later must
	// not repeat the notification.
	h.n.handle(ev(first.Add(30 * time.Second)))
	h.n.handle(ev(first.Add(-90 * time.Second)))
	if len(h.sent) != 1 {
		t.Errorf("sub-tolerance ResetAt movement fired: %v", h.sent)
	}

	// A real rollover: "reset" for the consumed generation, then the
	// fresh generation trips "low" again in the same snapshot.
	h.n.handle(ev(first.Add(-5 * time.Hour)))
	if len(h.sent) != 3 {
		t.Fatalf("rollover sent %v, want reset + re-armed low", h.sent)
	}
	if want := "MiniMax | interval: reset"; h.sent[1] != want {
		t.Errorf("rollover send = %q, want %q", h.sent[1], want)
	}
	if want := "MiniMax | interval: 8% remaining"; h.sent[2] != want {
		t.Errorf("post-rollover send = %q, want %q", h.sent[2], want)
	}

	// The new generation latches: jittering polls stay silent.
	h.n.handle(ev(first.Add(-5*time.Hour + 45*time.Second)))
	if len(h.sent) != 3 {
		t.Errorf("new generation re-fired: %v", h.sent)
	}
}

// The reset event fires only for a generation that was actually used: a
// window that sat untouched across a rollover resets silently.
func TestQuotaResetRequiresUsage(t *testing.T) {
	h := newNotifierHarness(t)

	untouched := qWindow("interval", 1)
	gen := func(reset time.Time) quotaEvent {
		w := untouched
		w.ResetAt = &reset

		return qEvent("mm", "MiniMax", w)
	}
	first, second := time.Unix(1770000000, 0), time.Unix(1770018000, 0)

	h.n.handle(gen(first))
	h.n.handle(gen(second))
	if len(h.sent) != 0 {
		t.Errorf("unused generation's rollover fired: %v", h.sent)
	}

	// Touch the window, then roll it: now the reset is worth announcing.
	touched := gen(second)
	touched.windows[0].RemainingFraction = 0.9
	touched.windows[0].UsedFraction = 0.1
	h.n.handle(touched)
	h.n.handle(gen(second.Add(-6 * time.Hour)))
	if len(h.sent) != 1 || h.sent[0] != "MiniMax | interval: reset" {
		t.Errorf("used generation's rollover sent %v, want exactly one reset", h.sent)
	}
}

// A provider gaining or losing the reset time is not a rollover: the
// anchor is adopted or dropped and the edges keep their state.
func TestQuotaResetAtAdoptAndDrop(t *testing.T) {
	h := newNotifierHarness(t)

	// Latch low with no reset time exposed.
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.08)))
	if len(h.sent) != 1 {
		t.Fatalf("first trip = %v", h.sent)
	}

	// The provider starts exposing ResetAt: adopt it, stay silent.
	adopt := qWindow("interval", 0.05)
	reset := time.Unix(1770000000, 0)
	adopt.ResetAt = &reset
	h.n.handle(qEvent("mm", "MiniMax", adopt))
	if len(h.sent) != 1 {
		t.Errorf("anchor adoption fired: %v", h.sent)
	}

	// The provider stops exposing it again: the latch survives.
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.03)))
	if len(h.sent) != 1 {
		t.Errorf("anchor drop fired: %v", h.sent)
	}

	// Unanchored again, the band-exit re-arm is back in charge.
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.4)))
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.05)))
	if len(h.sent) != 2 {
		t.Errorf("band-exit re-arm after anchor drop broken: %v", h.sent)
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

// A window absent from a snapshot keeps its latch through a grace of
// consecutive absences — one flaky snapshot must not resurrect its edges
// — and loses it only after the full grace: a reset that rotated the
// window set cannot carry state into the next generation.
func TestQuotaWindowVanishDropsLatch(t *testing.T) {
	h := newNotifierHarness(t)

	two := qEvent("mm", "MiniMax", qWindow("interval", 0.5), qWindow("weekly", 0.08))
	h.n.handle(two)
	if len(h.sent) != 1 {
		t.Fatalf("weekly trip = %v", h.sent)
	}

	// The weekly window flickers out for a single snapshot; the interval
	// window stays mid-band, silent.
	h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.5)))
	if len(h.sent) != 1 {
		t.Errorf("vanish handling fired: %v", h.sent)
	}

	// Back within the grace: the latch survived, no re-fire.
	h.n.handle(two)
	if len(h.sent) != 1 {
		t.Errorf("weekly latch did not survive a one-snapshot vanish: %v", h.sent)
	}

	// Absent for the full grace: the latch is dropped...
	for i := 0; i < vanishGrace; i++ {
		h.n.handle(qEvent("mm", "MiniMax", qWindow("interval", 0.5)))
	}
	// ...so the returned window trips fresh.
	h.n.handle(two)
	if len(h.sent) != 2 {
		t.Errorf("weekly latch survived the full vanish grace: %v", h.sent)
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

	t.Run("reset toggle", func(t *testing.T) {
		h := newNotifierHarness(t)
		h.reset = false

		ev := func(reset time.Time) quotaEvent {
			w := qWindow("interval", 0.5)
			w.ResetAt = &reset

			return qEvent("mm", "MiniMax", w)
		}
		first := time.Unix(1770000000, 0)

		h.n.handle(ev(first))
		h.n.handle(ev(first.Add(-5 * time.Hour))) // silenced rollover, edges re-armed
		if len(h.sent) != 0 {
			t.Fatalf("disabled reset fired: %v", h.sent)
		}

		h.reset = true
		h.n.handle(ev(first.Add(-10 * time.Hour))) // audible rollover
		if len(h.sent) != 1 || h.sent[0] != "MiniMax | interval: reset" {
			t.Errorf("enabled reset did not fire at the next rollover: %v", h.sent)
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
	// parked closes on the first Send entry: the drop arithmetic below is
	// only sound once the worker is provably parked, so the producer must
	// wait for it instead of racing the scheduler (CI flake, 2026-11-04).
	parked := make(chan struct{})
	var parkOnce sync.Once
	n := newNotifier(slog.New(slog.NewTextHandler(io.Discard, nil)), notifierConfig{
		Master:    func() bool { return true },
		Low:       func() bool { return true },
		Exhausted: func() bool { return true },
		Send: func(title, content string) {
			parkOnce.Do(func() { close(parked) })
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

	// Enqueue the first event up front, then wait for the worker to park
	// inside its Send — only then is the queue empty with all 16 slots
	// free, and the drop arithmetic exact. The remaining enqueues must all
	// return without blocking. Each event carries its own fresh window so
	// every one of them would fire — the edge-triggered latches must not
	// mask the drop being tested.
	n.Enqueue("MiniMax", "mm", []domain.QuotaWindow{qWindow("w0", 0.05)})
	select {
	case <-parked:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never parked inside Send")
	}
	filled := make(chan struct{})
	go func() {
		defer close(filled)
		for i := 1; i < notifyQueueLen+2; i++ {
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
