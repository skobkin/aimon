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
// supersedes that for exactly two edge-triggered events, each with its own
// Settings toggle: a quota window entering the red band ("low") and a
// window reaching zero ("exhausted").
//
// Privacy is structural: notification text carries the instance name, the
// window name, fixed state words and whole percentages only — never
// absolute money or token counts — so the shell's notification log cannot
// leak what the hide-sensitive preference masks. The notifier never reads
// internal/pace (no projection alerts).
//
// Delivery model: the manager loop goroutine only enqueues (non-blocking,
// bounded queue); one worker goroutine evaluates latches and posts. The
// real Send is fyne.App.SendNotification — a synchronous session-bus D-Bus
// call on Linux — so it must never run on a manager loop or the UI thread.

// notifyQueueLen bounds the event queue. Quota events arrive at poll
// cadence (tens of seconds at fastest), so sixteen absorbs any burst; an
// overflow drops the event with a warning.
const notifyQueueLen = 16

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
// fired it stays silent until the edge re-arms (the remaining fraction
// climbs back out of its band, or the window resets).
type windowLatch struct {
	low, exhausted bool
	resetAt        *time.Time
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
// goroutine only. Gating (Master/Low/Exhausted) decides audibility only:
// the latch state always tracks reality, so enabling an event mid-band
// cannot retro-fire — only the next transition is heard.
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
		if resetChanged(l.resetAt, w.ResetAt) {
			// A new window generation clears both edges, even when the
			// fresh window opens in-band (that one trip is accepted and
			// recorded in the Decision Log).
			l.low, l.exhausted = false, false
		}
		l.resetAt = w.ResetAt

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
		// Re-arm. A rising fraction in a rolling window is refill, not an
		// error: leaving the band re-arms without waiting for the reset
		// time to move (providers that expose no ResetAt would otherwise
		// latch forever).
		if remaining > components.LowThreshold {
			l.low = false
		}
		if remaining > components.CriticalThreshold {
			l.exhausted = false
		}
	}
	// A window absent from the snapshot is gone (a reset rotated the set
	// or the provider dropped it): its latches must not leak into the next
	// generation — that also covers reset-straight-back-into-band for
	// ResetAt-carrying windows whose reset time failed to move.
	for wid := range latches {
		if _, ok := seen[wid]; !ok {
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

// resetChanged reports whether the window reset time moved — the marker of
// a new window generation for providers that expose it. nil↔non-nil is a
// change; two nils mean the provider never exposes one.
func resetChanged(old, cur *time.Time) bool {
	if old == nil || cur == nil {
		return old != cur
	}

	return !old.Equal(*cur)
}
