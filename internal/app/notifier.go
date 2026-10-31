package app

import (
	"fmt"
	"log/slog"
	"math"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/ui/components"
)

// The quota desktop notifier. Desktop notifications were originally out of
// scope (docs/ARCHITECTURE.md §14); the 2026-10-30 Decision Log entry
// supersedes that for edge-triggered events, each with its own Settings
// toggle: a quota window entering the red band ("low"), a window reaching
// zero ("exhausted"), and a used window rolling over ("reset", added by
// the 2026-11-01 entry).
//
// Privacy is structural: notification text carries the instance name, the
// window name, fixed state words and whole percentages only — never
// absolute money or token counts — so the shell's notification log cannot
// leak what the hide-sensitive preference masks. The notifier never reads
// internal/pace (no projection alerts).
//
// Delivery model: the manager loop goroutine only enqueues (non-blocking,
// bounded queue); one worker goroutine evaluates latches and posts. The
// real Send posts over the desktop's notification service (D-Bus on
// Linux/BSD, fyne.App.SendNotification elsewhere) — a synchronous session
// bus call on Linux — so it must never run on a manager loop or the UI
// thread.

// notifyQueueLen bounds the event queue. Quota events arrive at poll
// cadence (tens of seconds at fastest), so sixteen absorbs any burst; an
// overflow drops the event with a warning.
const notifyQueueLen = 16

// resetJitterTolerance is how far a window's ResetAt may move between two
// polls without counting as a new window generation. MiniMax reports a
// remaining-milliseconds countdown, so its ResetAt is re-derived from the
// observation time every poll and jitters by seconds; a real rollover
// moves it by the window length (hours).
const resetJitterTolerance = 5 * time.Minute

// vanishGrace is how many consecutive snapshots a window may be absent
// from before its latch state is dropped. One flaky or partially parsed
// snapshot must not resurrect the edges of a window that is still there.
const vanishGrace = 3

// notifierConfig is the notifier's view of the app: the gating preferences
// (read at event time, so toggles apply without a restart), the send
// primitive, and the keep-set of enabled instances for latch pruning.
type notifierConfig struct {
	// Master reports the master "Desktop notifications" preference.
	Master func() bool
	// Low reports the "Quota window low" event toggle.
	Low func() bool
	// Exhausted reports the "Quota window exhausted" event toggle.
	Exhausted func() bool
	// Reset reports the "Quota window reset" event toggle.
	Reset func() bool
	// Send posts one desktop notification.
	Send func(title, content string)
	// Known returns the enabled configured instances; latch state for
	// anything else is dropped.
	Known func() map[domain.InstanceID]struct{}
}

// quotaEvent is one completed quota refresh handed to the worker.
type quotaEvent struct {
	id      domain.InstanceID
	name    string
	windows []domain.QuotaWindow
}

// windowLatch is the edge state of one instance window: after an event
// fired it stays silent until the window resets (a ResetAt moved beyond
// the jitter tolerance) — or, when the provider exposes no reset time,
// until the remaining fraction climbs back out of its band.
type windowLatch struct {
	low, exhausted bool
	// used records whether any observation of the current generation saw
	// usage: a reset notification fires only for a window that was
	// actually consumed.
	used bool
	// resetAt anchors the current generation: the first ResetAt seen for
	// it, never advanced by jitter. nil when the provider exposes no
	// reset time.
	resetAt *time.Time
	// misses counts consecutive snapshots the window was absent from.
	misses int
}

// notifier turns quota snapshots into desktop notifications. All latch
// state is owned by the single worker goroutine; producers only enqueue.
type notifier struct {
	cfg notifierConfig
	log *slog.Logger

	events chan quotaEvent
	quit   chan struct{}
	done   chan struct{}

	latches map[domain.InstanceID]map[string]*windowLatch
}

// newNotifier creates the notifier and starts its worker.
func newNotifier(log *slog.Logger, cfg notifierConfig) *notifier {
	n := &notifier{
		cfg:     cfg,
		log:     log,
		events:  make(chan quotaEvent, notifyQueueLen),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
		latches: map[domain.InstanceID]map[string]*windowLatch{},
	}
	go n.run()

	return n
}

// Enqueue hands one completed quota refresh to the worker. It never
// blocks: a full queue drops the event with a warning — notifications are
// disposable, and the next poll re-evaluates every window anyway.
func (n *notifier) Enqueue(name string, id domain.InstanceID, windows []domain.QuotaWindow) {
	ev := quotaEvent{id: id, name: name, windows: windows}
	select {
	case n.events <- ev:
	default:
		n.log.Warn("notification queue full; quota event dropped", "instance", string(id))
	}
}

// Close stops the worker and waits for it to exit. Events still queued are
// dropped: the app is quitting.
func (n *notifier) Close() {
	close(n.quit)
	<-n.done
}

func (n *notifier) run() {
	defer close(n.done)
	for {
		select {
		case ev := <-n.events:
			n.handle(ev)
		case <-n.quit:
			return
		}
	}
}

// handle evaluates one snapshot's windows against the latches. Worker
// goroutine only. Gating (Master/Low/Exhausted/Reset) decides audibility
// only: the latch state always tracks reality, so enabling an event
// mid-band cannot retro-fire — only the next transition is heard.
func (n *notifier) handle(ev quotaEvent) {
	if n.cfg.Known != nil {
		known := n.cfg.Known()
		if _, ok := known[ev.id]; !ok {
			// A disabled or removed instance must not hold state that
			// could misfire when it comes back.
			delete(n.latches, ev.id)

			return
		}
		n.pruneInstances(known)
	}
	latches := n.latches[ev.id]
	if latches == nil {
		latches = map[string]*windowLatch{}
		n.latches[ev.id] = latches
	}

	seen := make(map[string]struct{}, len(ev.windows))
	for _, w := range ev.windows {
		wid := w.ID
		if wid == "" {
			wid = w.Name
		}
		seen[wid] = struct{}{}
		l := latches[wid]
		if l == nil {
			l = &windowLatch{}
			latches[wid] = l
		}
		l.misses = 0
		if newGen, used := l.advanceGeneration(w.ResetAt); newGen && used {
			// The ended generation was actually consumed: announce that
			// its quota is back. The fresh generation is judged below —
			// one that opens in-band trips on its own (accepted, Decision
			// Log 2026-10-30).
			n.send(ev, w, n.cfg.Reset, "reset")
		}

		remaining := domain.Clamp01(w.RemainingFraction)
		switch {
		case remaining <= 0:
			if !l.exhausted {
				// Exhausted subsumes low: a window falling straight
				// through the band into zero pings once, not twice.
				l.exhausted, l.low = true, true
				n.send(ev, w, n.cfg.Exhausted, "exhausted")
			}
		case remaining <= components.CriticalThreshold:
			if !l.low && !l.exhausted {
				l.low = true
				n.send(ev, w, n.cfg.Low, percentRemaining(remaining))
			}
		}
		// Re-arm. An anchored window (the provider exposes a reset time)
		// fires once per generation: its edges clear only in
		// advanceGeneration. A window without a reset time has no other
		// rollover marker, so leaving the band re-arms it — a rising
		// fraction in a rolling window is refill, not an error.
		if l.resetAt == nil {
			if remaining > components.LowThreshold {
				l.low = false
			}
			if remaining > components.CriticalThreshold {
				l.exhausted = false
			}
		}
		l.used = l.used || domain.Clamp01(w.UsedFraction) > 0
	}
	// A window absent from the snapshot is on its way out (a reset
	// rotated the set, or the provider dropped it), but one flaky
	// snapshot must not resurrect its edges: the latch survives a grace
	// of consecutive absences before the state is dropped.
	for wid, l := range latches {
		if _, ok := seen[wid]; ok {
			continue
		}
		l.misses++
		if l.misses >= vanishGrace {
			delete(latches, wid)
		}
	}
}

// pruneInstances drops latch maps of instances outside the known set.
func (n *notifier) pruneInstances(known map[domain.InstanceID]struct{}) {
	for id := range n.latches {
		if _, ok := known[id]; !ok {
			delete(n.latches, id)
		}
	}
}

// send posts one notification unless its event toggle or the master
// switch silences it. The caller has already armed the edge: a disabled
// event still latches, so toggling it on mid-band stays quiet until the
// next transition.
func (n *notifier) send(ev quotaEvent, w domain.QuotaWindow, eventEnabled func() bool, state string) {
	if n.cfg.Send == nil {
		return
	}
	if eventEnabled != nil && !eventEnabled() {
		return
	}
	if n.cfg.Master != nil && !n.cfg.Master() {
		return
	}
	title := ev.name
	if title == "" {
		title = string(ev.id)
	}
	window := w.Name
	if window == "" {
		window = w.ID
	}
	if window == "" {
		window = "window"
	}
	n.cfg.Send(title, window+": "+state)
}

// percentRemaining renders the "low" state word as a whole percentage —
// never an absolute token or money count. Fractions below one percent say
// "<1%" instead of rounding up to a lying "1%".
func percentRemaining(remaining float64) string {
	if remaining < 0.01 {
		return "<1% remaining"
	}

	return fmt.Sprintf("%d%% remaining", int(math.Round(remaining*100)))
}

// advanceGeneration folds an observed ResetAt into the latch and reports
// whether a new window generation began (the old anchor and the
// observation both known, moved apart beyond the jitter tolerance) and
// whether the ended generation saw usage. A provider gaining or losing a
// reset time is not a reset: the anchor is adopted or dropped and the
// edges keep their state — there is nothing to distinguish that from the
// same generation reported less completely.
func (l *windowLatch) advanceGeneration(cur *time.Time) (newGen, used bool) {
	if cur == nil {
		// No reset time observed (or the provider stopped exposing one):
		// drop the anchor so band-exit re-arming takes over.
		l.resetAt = nil

		return false, l.used
	}
	if l.resetAt == nil {
		// The provider started exposing a reset time: adopt it as the
		// anchor without declaring a reset.
		l.resetAt = cur

		return false, l.used
	}
	if jittered(*l.resetAt, *cur) {
		return false, l.used // same generation; the anchor stays put
	}
	// A real rollover: the old generation's edges die with it, even when
	// the fresh window opens in-band (accepted, Decision Log 2026-10-30).
	used = l.used
	l.resetAt = cur
	l.low, l.exhausted, l.used = false, false, false

	return true, used
}

// jittered reports whether an observed reset time still belongs to the
// anchored generation. Providers that derive ResetAt from a remaining-time
// countdown re-derive it every poll, so within one generation it moves by
// seconds; a real rollover moves it by the window length.
func jittered(anchor, cur time.Time) bool {
	d := cur.Sub(anchor)
	if d < 0 {
		d = -d
	}

	return d <= resetJitterTolerance
}
