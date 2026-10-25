// Package pace derives short-term usage speed ("pace") and an
// end-of-window projection from a rolling in-memory history of quota and
// spend measurements (S7). Pure math over retained samples: no Fyne, no
// I/O, no persistence, and no new network requests — the samples are the
// snapshots the monitor already collected.
//
// History is keyed per instance and quota window ID. Recording performs
// discontinuity handling and retention only; estimates are computed at
// read time with the instance's configured estimation window, so a window
// change takes effect on the very next read without waiting for a poll.
package pace

import (
	"sync"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// Tuning constants, named so tests pin them.
const (
	// MinSpan is the smallest in-window endpoint span a speed is reported
	// for. Below it the state is Collecting — even when the samples are
	// flat, so a brand-new series never reports "idle at zero speed"
	// before anything was actually measured.
	MinSpan = 3 * time.Minute
	// fracEpsilon absorbs float noise in used-fraction comparisons;
	// several providers quantize fractions to integer percent, so exact
	// equality is meaningless there.
	fracEpsilon = 1e-9
	// countEpsilon serves the same role for count comparisons, which are
	// authoritative when both samples carry counts (integer counts make
	// exact equality meaningful).
	countEpsilon = 1e-9
	// maxSamples caps every series regardless of its time span.
	maxSamples = 240
	// retention spans twice the largest supported pace window
	// (config.PaceWindows max is 60m), so switching to the longest window
	// finds enough samples without waiting for re-collection.
	retention = 2 * time.Hour
	// defaultWindow mirrors config.DefaultPaceWindow; the package stays
	// config-free, and the app sets the real per-instance window on every
	// apply, so this only covers reads before the first SetWindow.
	defaultWindow = 20 * time.Minute
)

// State classifies one series' estimate.
type State int

const (
	// StateCollecting: not enough covered history yet (fewer than two
	// samples, or the in-window span is below MinSpan).
	StateCollecting State = iota
	// StateActive: positive measured consumption.
	StateActive
	// StateIdle: the newest sample shows no consumption against the newest
	// sample at least MinSpan older — the speed is forced to zero so a
	// finished burst cannot keep projecting a fake overage.
	StateIdle
	// StateUnavailable: the first read after a discontinuity (usage
	// decrease, limit change). The next recorded sample clears it.
	StateUnavailable
)

// Sample is one recorded reading of a series, stamped with the snapshot's
// own collection time — never the receive time.
type Sample struct {
	At        time.Time
	Used      float64  // used fraction at At
	UsedCount *float64 // limit − remaining when both are known; nil otherwise
	Limit     *float64 // for limit-change discontinuity detection
	Unit      string   // provider count unit ("requests", "credits"); "" when unknown
	ResetAt   *time.Time
}

// Estimate is the current derived view of one series.
type Estimate struct {
	State State

	// SpeedFrac is the used-fraction consumption per hour and feeds the
	// projection. SpeedCount is count units per hour, valid when HasCount;
	// count-bearing series also derive SpeedFrac from counts, because
	// provider percentages are often quantized.
	SpeedFrac  float64
	SpeedCount float64
	HasCount   bool
	Unit       string

	// LastUsed is the projection anchor: the used fraction implied by the
	// newest sample's counts (UsedCount/Limit) when they are valid — the
	// provider's rounded fraction otherwise — and LastAt its timestamp.
	LastUsed float64
	LastAt   time.Time
	// ResetAt carries the newest sample's reset time: non-nil means the
	// window end is known and the projection is possible while fresh.
	ResetAt *time.Time
}

// Project returns the projected used fraction at the window reset:
// LastUsed + SpeedFrac × (ResetAt − LastAt).Hours(). The value is
// unclamped and may exceed 1.0 — it is constant between samples.
//
// ok is false while the projection is impossible (no reset time, reset not
// after the anchor, state Collecting/Unavailable) or stale: once at has
// reached ResetAt the projection has expired and the caller hides it until
// a fresh sample delivers a new reset time.
func (e Estimate) Project(at time.Time) (float64, bool) {
	if e.ResetAt == nil || e.State == StateCollecting || e.State == StateUnavailable {
		return 0, false
	}
	if !e.ResetAt.After(e.LastAt) || !at.Before(*e.ResetAt) {
		return 0, false
	}

	return e.LastUsed + e.SpeedFrac*e.ResetAt.Sub(e.LastAt).Hours(), true
}

// InstancePaces carries every estimate of one configured source.
type InstancePaces struct {
	// Windows maps quota window ID ("interval", "credits-5h",
	// "codex-primary", …) to its estimate.
	Windows map[string]Estimate
	// Spend is the OpenRouter per-key spend estimate ($/h) when this
	// instance is an OpenRouter key with usage; never projectable — the
	// spend series has no reset time by S4 policy.
	Spend *Estimate
}

// series is one quota window's (or the spend counter's) sample history.
type series struct {
	// samples is ordered oldest → newest; retention prunes the front.
	samples []Sample
	// reset is set when the newest recorded sample followed a
	// discontinuity; reads return StateUnavailable until the next sample
	// is recorded, then the flag clears and the normal path resumes.
	reset bool
	// unit remembers the last non-empty provider unit seen, so estimates
	// keep labeling counts when a later sample omits it.
	unit string
}

// Tracker holds the sample history of every configured source. All methods
// take one mutex: recording happens on source loop goroutines, reads on the
// UI thread, resets on the settings-save goroutine.
type Tracker struct {
	mu      sync.Mutex
	windows map[domain.InstanceID]time.Duration
	quota   map[domain.InstanceID]map[string]*series
	spend   map[domain.InstanceID]*series
}

// NewTracker creates an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{
		windows: map[domain.InstanceID]time.Duration{},
		quota:   map[domain.InstanceID]map[string]*series{},
		spend:   map[domain.InstanceID]*series{},
	}
}

// ObserveQuota records one quota snapshot's windows. Samples with a
// timestamp that is not newer than the series' newest sample are ignored —
// a failed refresh notifies with the retained last-good snapshot and must
// not fabricate a flat interval.
func (t *Tracker) ObserveQuota(id domain.InstanceID, snapAt time.Time, windows []domain.QuotaWindow) {
	t.mu.Lock()
	defer t.mu.Unlock()

	byID, ok := t.quota[id]
	if !ok {
		byID = map[string]*series{}
		t.quota[id] = byID
	}
	for _, w := range windows {
		s := byID[w.ID]
		if s == nil {
			s = &series{}
			byID[w.ID] = s
		}
		s.record(quotaSample(snapAt, w))
	}
}

// ObserveSpend records one OpenRouter cumulative-spend reading. The counter
// is monotonic: a decrease is a discontinuity exactly like a quota usage
// decrease. The series never carries a reset time, so it can never project.
func (t *Tracker) ObserveSpend(id domain.InstanceID, snapAt time.Time, usageTotal float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.spend[id]
	if s == nil {
		s = &series{}
		t.spend[id] = s
	}
	total := usageTotal
	s.record(Sample{At: snapAt, UsedCount: &total, Unit: "$"})
}

// SetWindow stores one instance's estimation window; zero selects the
// default. Called from applyInstances, so a window change affects the very
// next States() read without any poll in between.
func (t *Tracker) SetWindow(id domain.InstanceID, window time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if window <= 0 {
		window = defaultWindow
	}
	t.windows[id] = window
}

// Reset drops one instance's whole history (adapter rebuild, removal). The
// next observation starts a fresh first-sample series.
func (t *Tracker) Reset(id domain.InstanceID) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.windows, id)
	delete(t.quota, id)
	delete(t.spend, id)
}

// States computes the estimates of every known instance with each one's
// current window. The result is detached: mutating the returned maps never
// reaches the tracker. Sample values are immutable once recorded, so the
// shared ResetAt pointers are safe to read.
func (t *Tracker) States() map[domain.InstanceID]InstancePaces {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make(map[domain.InstanceID]InstancePaces, len(t.quota)+len(t.spend))
	for id, byID := range t.quota {
		ip := InstancePaces{Windows: make(map[string]Estimate, len(byID))}
		for windowID, s := range byID {
			ip.Windows[windowID] = estimate(s, t.effectiveWindow(id))
		}
		out[id] = ip
	}
	for id, s := range t.spend {
		ip, ok := out[id]
		if !ok {
			ip = InstancePaces{Windows: map[string]Estimate{}}
		}
		est := estimate(s, t.effectiveWindow(id))
		ip.Spend = &est
		out[id] = ip
	}

	return out
}

func (t *Tracker) effectiveWindow(id domain.InstanceID) time.Duration {
	if w := t.windows[id]; w > 0 {
		return w
	}

	return defaultWindow
}

// quotaSample converts one normalized quota window into a sample. Counts
// are derived as limit − remaining: the adapters only attach counts whose
// semantics are verified, and both fields travel together or not at all.
func quotaSample(at time.Time, w domain.QuotaWindow) Sample {
	var usedCount, limit *float64
	if w.Remaining != nil && w.Limit != nil {
		c := *w.Limit - *w.Remaining
		usedCount, limit = &c, w.Limit
	}

	return Sample{
		At:        at,
		Used:      w.UsedFraction,
		UsedCount: usedCount,
		Limit:     limit,
		Unit:      w.Unit,
		ResetAt:   w.ResetAt,
	}
}

// record applies one sample to a series: discontinuity handling first, then
// the normal append with retention.
func (s *series) record(smp Sample) {
	if len(s.samples) > 0 {
		newest := s.samples[len(s.samples)-1]
		if !smp.At.After(newest.At) {
			return // stale or duplicate notification; never recorded
		}
		if s.discontinuous(newest, smp) {
			// Keep only the new sample as the fresh baseline. The reset
			// flag makes the next read Unavailable (the interval that
			// contained the break has no meaningful rate); the following
			// sample clears it and the normal path resumes.
			s.samples = []Sample{smp}
			s.reset = true
			s.rememberUnit(smp)

			return
		}
		// A normal sample is the first one after a discontinuity: the
		// unavailable interval is over.
		s.reset = false
	}
	s.append(smp)
}

// discontinuous reports whether smp breaks the series against the previous
// newest sample. A usage decrease is authoritative from counts when both
// samples carry them — a window rollover can hide inside an unchanged
// quantized fraction. A limit change resets too. ResetAt movement alone is
// never a discontinuity: MiniMax re-derives its reset stamp from a relative
// duration on every poll and Codex rolling deadlines drift between
// requests, so dropping history on those jitters would leave pace
// permanently Collecting.
func (s *series) discontinuous(prev, smp Sample) bool {
	if prev.UsedCount != nil && smp.UsedCount != nil {
		if *smp.UsedCount < *prev.UsedCount-countEpsilon {
			return true
		}
	} else if smp.Used < prev.Used-fracEpsilon {
		return true
	}
	if !sameCount(prev.Limit, smp.Limit) {
		return true
	}

	return false
}

// append stores one sample and applies retention: samples older than the
// retention span go, then the ring cap bounds the slice regardless of span.
func (s *series) append(smp Sample) {
	s.samples = append(s.samples, smp)
	s.rememberUnit(smp)

	cutoff := smp.At.Add(-retention)
	drop := 0
	for drop < len(s.samples) && s.samples[drop].At.Before(cutoff) {
		drop++
	}
	if drop > 0 {
		s.samples = append(s.samples[:0], s.samples[drop:]...)
	}
	if over := len(s.samples) - maxSamples; over > 0 {
		s.samples = s.samples[over:]
	}
}

func (s *series) rememberUnit(smp Sample) {
	if smp.Unit != "" {
		s.unit = smp.Unit
	}
}

func sameCount(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	return absFloat(*a-*b) <= countEpsilon
}

// estimate computes the current view of a series over the trailing window.
// The endpoint is the oldest sample inside the window, so a gap (failed
// polls, suspend, disable) contributes its real elapsed time to the average
// — rates are deltas over timestamps, never per-sample averages.
func estimate(s *series, window time.Duration) Estimate {
	if s.reset {
		return Estimate{State: StateUnavailable, Unit: s.unit}
	}

	n := len(s.samples)
	if n == 0 {
		return Estimate{State: StateCollecting, Unit: s.unit}
	}
	newest := s.samples[n-1]
	est := Estimate{
		Unit:     s.unitFor(newest),
		LastUsed: anchor(newest),
		LastAt:   newest.At,
		ResetAt:  newest.ResetAt,
	}

	if n < 2 {
		est.State = StateCollecting

		return est
	}

	// Endpoint selection before every verdict — including the idle
	// override — so a flat brand-new pair and a sparse in-window history
	// both stay Collecting.
	cutoff := newest.At.Add(-window)
	start := 0
	for start < n-1 && s.samples[start].At.Before(cutoff) {
		start++
	}
	endpoint := s.samples[start]
	span := newest.At.Sub(endpoint.At)
	if span < MinSpan {
		est.State = StateCollecting

		return est
	}

	dt := span.Hours() // > 0 by the MinSpan gate

	// Idle override: consumption has stopped when the newest sample equals
	// the newest sample at least MinSpan older — not merely the adjacent
	// pair. A sample recorded seconds after its predecessor (a manual
	// refresh landing between polls) is flat for lack of time, not for
	// lack of usage, and reading it as idle zeroed the speed and collapsed
	// every projection until the next poll (DECISION_LOG 2026-10-25). The
	// comparison anchor always exists: the endpoint gate above guarantees
	// samples[start] is at least MinSpan older than newest. Counts compare
	// exactly; fractions need the epsilon because providers quantize them
	// to integer percent. The fallback only ever compares a consistently
	// fraction-only pair: quotaSample carries UsedCount and Limit together
	// or not at all, so a poll that loses its counts loses its limit too
	// and reads as a limit change — a discontinuity that resets the series
	// before estimation ever sees a mixed pair.
	idlePrevIdx := n - 2
	for idlePrevIdx >= 0 && newest.At.Sub(s.samples[idlePrevIdx].At) < MinSpan {
		idlePrevIdx--
	}
	idlePrev := s.samples[idlePrevIdx]
	idle := false
	if newest.UsedCount != nil && idlePrev.UsedCount != nil {
		idle = absFloat(*newest.UsedCount-*idlePrev.UsedCount) <= countEpsilon
	} else {
		idle = absFloat(newest.Used-idlePrev.Used) <= fracEpsilon
	}
	if idle {
		est.State = StateIdle
		// The pair carried counts: report "0 units/h", not "0%/h", so the
		// display stays consistent with the Active state's unit.
		est.HasCount = newest.UsedCount != nil && idlePrev.UsedCount != nil

		return est
	}

	est.State = StateActive
	speedFrac := (newest.Used - endpoint.Used) / dt
	// A decrease between endpoint and newest cannot survive recording (it
	// would have been a discontinuity), so a negative here is noise only.
	if speedFrac < 0 {
		speedFrac = 0
	}
	est.SpeedFrac = speedFrac

	if newest.UsedCount != nil && endpoint.UsedCount != nil {
		delta := *newest.UsedCount - *endpoint.UsedCount
		est.HasCount = true
		est.SpeedCount = delta / dt
		if est.SpeedCount < 0 {
			est.SpeedCount = 0
		}
		if newest.Limit != nil && *newest.Limit > 0 {
			// The count-derived fraction keeps the projection consistent
			// with the count speed; fraction deltas are quantized on some
			// providers.
			est.SpeedFrac = delta / *newest.Limit / dt
			if est.SpeedFrac < 0 {
				est.SpeedFrac = 0
			}
		}
	}

	return est
}

// unitFor prefers the sample's own unit and falls back to the series' last
// non-empty one.
func (s *series) unitFor(smp Sample) string {
	if smp.Unit != "" {
		return smp.Unit
	}

	return s.unit
}

// anchor is the projection's starting used fraction: exact counts when they
// are valid and the limit is positive, the provider's reported fraction
// otherwise. Only the pace projection consumes this — the ordinary quota
// display keeps using the reported fraction.
func anchor(smp Sample) float64 {
	if smp.UsedCount != nil && smp.Limit != nil && *smp.Limit > 0 {
		return *smp.UsedCount / *smp.Limit
	}

	return smp.Used
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}

	return v
}
