package pace

import (
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// base is a fixed clock: every test time is an offset from it.
var base = time.Unix(1770000000, 0)

func at(minutes float64) time.Time {
	return base.Add(time.Duration(minutes * float64(time.Minute)))
}

func f(v float64) *float64 { return &v }

// near compares derived speeds, anchors and projections with float
// tolerance: the chosen numbers are exact in decimal, but the division
// introduces binary rounding noise.
func near(a, b float64) bool {
	return absFloat(a-b) <= 1e-9
}

// quota builds one quota window sample: used fraction plus optional counts.
func quota(id domain.InstanceID, minutes float64, used float64, remaining, limit *float64, unit string, resetAt *time.Time) (domain.InstanceID, time.Time, []domain.QuotaWindow) {
	w := domain.QuotaWindow{ID: string(id), UsedFraction: used, Unit: unit, ResetAt: resetAt}
	if remaining != nil && limit != nil {
		w.Remaining, w.Limit = remaining, limit
	}

	return id, at(minutes), []domain.QuotaWindow{w}
}

// record feeds one window reading to a fresh tracker.
func record(t *testing.T, tr *Tracker, id domain.InstanceID, minutes float64, used float64, remaining, limit *float64, unit string, resetAt *time.Time) {
	t.Helper()
	inst, ts, windows := quota(id, minutes, used, remaining, limit, unit, resetAt)
	tr.ObserveQuota(inst, ts, windows)
}

// one returns the single window's estimate.
func one(t *testing.T, tr *Tracker, id domain.InstanceID) Estimate {
	t.Helper()
	ip, ok := tr.States()[id]
	if !ok {
		t.Fatalf("no pace state for %q", id)
	}
	est, ok := ip.Windows[string(id)]
	if !ok {
		t.Fatalf("no estimate for the window of %q", id)
	}

	return est
}

func TestFirstSampleIsCollecting(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", nil)

	est := one(t, tr, "mm")
	if est.State != StateCollecting {
		t.Fatalf("state = %v, want Collecting", est.State)
	}
	if est.LastUsed != 0.10 || !est.LastAt.Equal(at(0)) {
		t.Errorf("anchor = %v@%v, want the sample itself", est.LastUsed, est.LastAt)
	}
}

// A brand-new flat pair below MinSpan stays Collecting: nothing was ever
// measured, so "idle at zero speed" would be a claim, not a measurement.
func TestInitialFlatSamplesBelowMinSpanStayCollecting(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", nil)
	record(t, tr, "mm", 2, 0.10, nil, nil, "", nil)

	if est := one(t, tr, "mm"); est.State != StateCollecting {
		t.Fatalf("flat 2m pair: state = %v, want Collecting", est.State)
	}
}

// A manual refresh records a sample seconds after the scheduled poll: the
// adjacent pair is flat for lack of time, not for lack of usage, and must
// never read as idle. The pace keeps measuring until a full ≥ MinSpan span
// ending at the newest sample is flat (DECISION_LOG 2026-10-25).
func TestManualRefreshSampleNeverReadsIdle(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	reset := at(300)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", &reset)
	record(t, tr, "mm", 5, 0.15, nil, nil, "", &reset)

	// Manual refresh 2 seconds after the scheduled poll, values unchanged.
	record(t, tr, "mm", 5+2.0/60, 0.15, nil, nil, "", &reset)
	est := one(t, tr, "mm")
	if est.State != StateActive {
		t.Fatalf("state after a manual refresh = %v, want Active", est.State)
	}
	if want := 0.05 / (302.0 / 3600); !near(est.SpeedFrac, want) {
		t.Errorf("speed = %v, want the window measurement %v", est.SpeedFrac, want)
	}

	// Flat across a full MinSpan ends the burst: the newest sample is
	// compared against the newest one at least MinSpan older (5m), not the
	// 2s-younger manual-refresh sample.
	record(t, tr, "mm", 8, 0.15, nil, nil, "", &reset)
	est = one(t, tr, "mm")
	if est.State != StateIdle || est.SpeedFrac != 0 {
		t.Fatalf("flat over 3m: state = %v speed = %v, want Idle 0", est.State, est.SpeedFrac)
	}
}

// Once coverage reaches MinSpan, a flat newest pair reports Idle with speed
// zero immediately — a finished burst must not keep projecting an overage.
func TestActiveToIdleTransition(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	reset := at(300)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", &reset)
	record(t, tr, "mm", 5, 0.15, nil, nil, "", &reset)

	est := one(t, tr, "mm")
	if est.State != StateActive || !near(est.SpeedFrac, 0.6) {
		t.Fatalf("burst state = %v speed = %v, want Active 0.6/h", est.State, est.SpeedFrac)
	}

	// The next pair is flat: idle at the very first flat pair.
	record(t, tr, "mm", 10, 0.15, nil, nil, "", &reset)
	est = one(t, tr, "mm")
	if est.State != StateIdle || est.SpeedFrac != 0 {
		t.Fatalf("flat pair: state = %v speed = %v, want Idle 0", est.State, est.SpeedFrac)
	}
	projected, ok := est.Project(at(20))
	if !ok || projected != 0.15 {
		t.Errorf("idle projection = %v (ok %v), want the anchor 0.15", projected, ok)
	}
}

// A sparse in-window history is Collecting even when flat — the span gate
// runs before the idle override.
func TestFlatSparsePairBelowMinSpanIsCollecting(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	// Two samples spanning 4m, but taken 30 minutes apart: the in-window
	// span is what counts, and it is below MinSpan for the newest pair.
	record(t, tr, "mm", 0, 0.10, nil, nil, "", nil)
	record(t, tr, "mm", 30, 0.10, nil, nil, "", nil)
	if est := one(t, tr, "mm"); est.State != StateCollecting {
		t.Fatalf("sparse flat pair: state = %v, want Collecting", est.State)
	}
}

// A count-bearing series reports its speed in counts per hour and derives
// the fraction speed from the counts. 30 minutes, known delta: 100 of 2000
// credits is 200/h and 10%/h, projecting 5h30m ahead from the last sample.
func TestCountDeltaOverThirtyMinutes(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 60*time.Minute) // the 30m delta must fit the window
	reset := at(360)                   // last sample + 5h30m
	limit := f(2000)
	record(t, tr, "mm", 0, 0.05, f(1900), limit, "credits", &reset)
	record(t, tr, "mm", 30, 0.10, f(1800), limit, "credits", &reset)

	est := one(t, tr, "mm")
	if est.State != StateActive {
		t.Fatalf("state = %v, want Active", est.State)
	}
	if !est.HasCount || est.Unit != "credits" {
		t.Fatalf("count speed not reported: HasCount=%v Unit=%q", est.HasCount, est.Unit)
	}
	if est.SpeedCount != 200 {
		t.Errorf("SpeedCount = %v, want 200 credits/h", est.SpeedCount)
	}
	if !near(est.SpeedFrac, 0.1) {
		t.Errorf("SpeedFrac = %v, want 0.1/h (100 of 2000 in 30m)", est.SpeedFrac)
	}
	if est.LastUsed != 0.10 {
		t.Errorf("anchor = %v, want the exact count ratio 200/2000", est.LastUsed)
	}
	// Projection: 0.10 + 0.1/h × (6h − 0.5h) = 0.65.
	projected, ok := est.Project(at(60))
	if !ok {
		t.Fatal("projection refused while fresh")
	}
	if !near(projected, 0.65) {
		t.Errorf("projection = %v, want 0.65", projected)
	}
}

// The provider's percentage is quantized to whole percent; the counts are
// exact. The anchor follows the counts, not the rounded fraction.
func TestAnchorPrefersExactCountsOverRoundedFraction(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("zai", 20*time.Minute)
	reset := at(300)
	// 149.7 of 1000 credits is 14.97% — reported as 15%.
	limit := f(1000)
	record(t, tr, "zai", 0, 0.15, f(850.3), limit, "credits", &reset)
	record(t, tr, "zai", 5, 0.20, f(800.1), limit, "credits", &reset)

	est := one(t, tr, "zai")
	if !near(est.LastUsed, 0.1999) {
		t.Errorf("anchor = %v, want the exact 199.9/1000", est.LastUsed)
	}
	// The fraction speed also comes from counts: 50.2 credits in 5 minutes
	// is 602.4/h → 0.6024/h, not the rounded 5%→5%/h.
	if !near(est.SpeedFrac, 0.6024) {
		t.Errorf("SpeedFrac = %v, want the count-derived 0.6024/h", est.SpeedFrac)
	}
}

// A usage decrease hidden by fraction quantization is still caught: the
// count delta is authoritative when both samples carry counts, and the
// read never produces a negative burn rate.
func TestCountDecreaseWithUnchangedFractionIsDiscontinuity(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	reset := at(300)
	// 10% both times by fraction, but counts roll from 200/2000 to 50/2000.
	limit := f(2000)
	record(t, tr, "mm", 0, 0.10, f(1800), limit, "credits", &reset)
	record(t, tr, "mm", 5, 0.10, f(1950), limit, "credits", &reset)

	est := one(t, tr, "mm")
	if est.State != StateUnavailable {
		t.Fatalf("state = %v, want Unavailable after the hidden rollover", est.State)
	}

	// The next sample clears the flag and the normal path resumes: the
	// recovery pair already spans 5m ≥ MinSpan, so it measures directly.
	record(t, tr, "mm", 10, 0.10, f(1900), limit, "credits", &reset)
	est = one(t, tr, "mm")
	if est.State != StateActive {
		t.Fatalf("after the recovery sample: state = %v, want Active (5m span measures the fresh window)", est.State)
	}
}

// Unavailable is retained exactly until the first post-discontinuity
// sample — one read, not one poll interval more or less.
func TestUnavailableHeldForExactlyOneObservation(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	reset := at(300)
	record(t, tr, "mm", 0, 0.20, nil, nil, "", &reset)
	record(t, tr, "mm", 5, 0.30, nil, nil, "", &reset)

	// The rollover: fraction drops outright.
	record(t, tr, "mm", 10, 0.05, nil, nil, "", &reset)
	if est := one(t, tr, "mm"); est.State != StateUnavailable {
		t.Fatalf("first read after rollover = %v, want Unavailable", est.State)
	}
	if est := one(t, tr, "mm"); est.State != StateUnavailable {
		t.Fatalf("reads must stay Unavailable while no new sample arrives, got %v", est.State)
	}

	// Next sample: flag clears; the 5m pair meets MinSpan and measures.
	record(t, tr, "mm", 15, 0.06, nil, nil, "", &reset)
	if est := one(t, tr, "mm"); est.State != StateActive {
		t.Fatalf("first read after recovery = %v, want Active (5m span ≥ MinSpan)", est.State)
	}
	record(t, tr, "mm", 25, 0.08, nil, nil, "", &reset)
	if est := one(t, tr, "mm"); est.State != StateActive {
		t.Fatalf("recovered series = %v, want Active", est.State)
	}
}

// ResetAt jitter never discards history: MiniMax re-derives its reset stamp
// every poll and Codex rolling deadlines drift. The series keeps estimating
// and the estimate carries the latest reset time.
func TestResetAtJitterKeepsHistory(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", p(at(300)))
	record(t, tr, "mm", 5, 0.15, nil, nil, "", p(at(299)))  // jitter
	record(t, tr, "mm", 10, 0.20, nil, nil, "", p(at(302))) // jitter again

	est := one(t, tr, "mm")
	if est.State != StateActive {
		t.Fatalf("state = %v, want Active under ResetAt jitter", est.State)
	}
	if est.SpeedFrac != 0.6 && !near(est.SpeedFrac, 0.6) {
		t.Errorf("speed = %v, want 0.6/h over the full 10m span", est.SpeedFrac)
	}
	if est.ResetAt == nil || !est.ResetAt.Equal(at(302)) {
		t.Errorf("ResetAt = %v, want the latest stamp", est.ResetAt)
	}
}

func p(v time.Time) *time.Time { return &v }

// The gap rule: a failed-poll hole contributes its real elapsed time. 5%
// used over a 25-minute gap (5m of history, 20m of silence) is 12%/h, not
// a per-sample average of the sparse samples.
func TestGapYieldsTrueAverage(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	reset := at(300)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", &reset)
	record(t, tr, "mm", 5, 0.15, nil, nil, "", &reset)
	// 20 minutes of failed polls, then a successful one.
	record(t, tr, "mm", 25, 0.20, nil, nil, "", &reset)

	est := one(t, tr, "mm")
	if est.State != StateActive {
		t.Fatalf("state = %v, want Active", est.State)
	}
	// Endpoint is the oldest sample inside the 20m window (t=5m), so the
	// delta is 0.05 over 20 minutes → 0.15/h.
	if !near(est.SpeedFrac, 0.15) {
		t.Errorf("speed = %v, want 0.15/h over the in-window span", est.SpeedFrac)
	}
}

// Stale and duplicate notifications (same or older snapshot timestamp) are
// never recorded.
func TestStaleAndDuplicateNotificationsIgnored(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", nil)
	record(t, tr, "mm", 5, 0.15, nil, nil, "", nil)
	// Duplicate timestamp (a second notification for the same snapshot).
	record(t, tr, "mm", 5, 0.15, nil, nil, "", nil)
	// Older timestamp (a late notification from before the newest).
	record(t, tr, "mm", 2, 0.99, nil, nil, "", nil)

	est := one(t, tr, "mm")
	if est.LastUsed != 0.15 || !est.LastAt.Equal(at(5)) {
		t.Fatalf("newest sample = %v@%v, want 0.15@t5", est.LastUsed, est.LastAt)
	}
}

// Switching the estimation window recomputes immediately from retained
// samples — no poll in between, no history loss.
func TestWindowSwitchRecomputesImmediately(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 10*time.Minute)
	reset := at(300)
	// A burst from 0 to 6 minutes, then slow consumption: 0.30→0.31 at 10m
	// and 0.31→0.32 at 20m.
	record(t, tr, "mm", 0, 0.00, nil, nil, "", &reset)
	record(t, tr, "mm", 6, 0.30, nil, nil, "", &reset)
	record(t, tr, "mm", 10, 0.31, nil, nil, "", &reset)
	record(t, tr, "mm", 20, 0.32, nil, nil, "", &reset)

	est := one(t, tr, "mm")
	if est.State != StateActive || !near(est.SpeedFrac, 0.06) {
		t.Fatalf("10m window: state=%v speed=%v, want Active 0.06/h", est.State, est.SpeedFrac)
	}

	tr.SetWindow("mm", 60*time.Minute)
	est = one(t, tr, "mm")
	// The 60m window reaches back to the burst: 0.32 over the full 20m.
	if est.State != StateActive || !near(est.SpeedFrac, 0.96) {
		t.Fatalf("60m window: state=%v speed=%v, want Active 0.96/h", est.State, est.SpeedFrac)
	}

	tr.SetWindow("mm", 20*time.Minute)
	est = one(t, tr, "mm")
	// Window start t=0 is inside the 20m span → burst included again.
	if !near(est.SpeedFrac, 0.96) {
		t.Fatalf("20m window: speed=%v, want 0.96/h", est.SpeedFrac)
	}
}

// Retention spans the largest supported window (2 × 60m), independently of
// the currently selected window, and the ring cap bounds the slice.
func TestRetentionAndRingCap(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 60*time.Minute)
	reset := at(1000)
	// Samples across 4 hours: everything older than 2h is pruned.
	for m := 0.0; m <= 240; m += 10 {
		record(t, tr, "mm", m, 0.01*m, nil, nil, "", &reset)
	}
	est := one(t, tr, "mm")
	if est.LastAt != at(240) {
		t.Fatalf("newest = %v, want t240", est.LastAt)
	}
	// In the 60m window the endpoint is t180 (used 1.8), newest t240 (used
	// 2.4): 0.6 used over one hour.
	if !near(est.SpeedFrac, 0.6) {
		t.Errorf("speed = %v, want 0.6/h", est.SpeedFrac)
	}

	// The ring cap: half-minute samples stay inside the retention span
	// until the cap itself prunes the oldest.
	tr2 := NewTracker()
	tr2.SetWindow("mm", 60*time.Minute)
	for i := 0; i < maxSamples+60; i++ {
		record(t, tr2, "mm", float64(i)/2, float64(i)/1000, nil, nil, "", &reset)
	}
	est = one(t, tr2, "mm")
	if !est.LastAt.Equal(at(float64(maxSamples+59) / 2)) {
		t.Errorf("after %d inserts the newest sample is missing", maxSamples+60)
	}
}

// A fraction-only series falls back to the epsilon-compared fraction pair
// for the idle check and reports %/h.
func TestFractionOnlySeries(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("codex", 20*time.Minute)
	reset := at(300)
	record(t, tr, "codex", 0, 0.50, nil, nil, "", &reset)
	record(t, tr, "codex", 6, 0.52, nil, nil, "", &reset)

	est := one(t, tr, "codex")
	if est.HasCount {
		t.Error("a fraction-only series must not claim count speeds")
	}
	if est.State != StateActive || !near(est.SpeedFrac, 0.2) {
		t.Fatalf("state=%v speed=%v, want Active 0.2/h", est.State, est.SpeedFrac)
	}
	if est.Unit != "" {
		t.Errorf("unit = %q, want empty", est.Unit)
	}
}

// The unit falls back to the last non-empty one in the series.
func TestUnitFallback(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	reset := at(300)
	limit := f(1000)
	record(t, tr, "mm", 0, 0.10, f(900), limit, "credits", &reset)
	record(t, tr, "mm", 6, 0.20, f(800), limit, "", &reset)

	est := one(t, tr, "mm")
	if est.Unit != "credits" {
		t.Errorf("unit = %q, want the series' last non-empty \"credits\"", est.Unit)
	}
}

// Project expiry: the projection is valid strictly before ResetAt and
// refuses at and after it.
func TestProjectExpiresAtReset(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	reset := at(100)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", &reset)
	record(t, tr, "mm", 6, 0.16, nil, nil, "", &reset)

	est := one(t, tr, "mm")
	if _, ok := est.Project(at(99)); !ok {
		t.Error("projection refused before ResetAt")
	}
	if _, ok := est.Project(at(100)); ok {
		t.Error("projection accepted exactly at ResetAt")
	}
	if _, ok := est.Project(at(101)); ok {
		t.Error("projection accepted after ResetAt")
	}
}

// Project refuses the impossible: no reset time, reset behind the anchor,
// and non-estimable states.
func TestProjectRefusals(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", nil)
	record(t, tr, "mm", 6, 0.16, nil, nil, "", nil)
	est := one(t, tr, "mm")
	if _, ok := est.Project(at(5)); ok {
		t.Error("projection accepted without a reset time")
	}

	// ResetAt not after the anchor: the window is already over.
	past := at(3)
	tr2 := NewTracker()
	tr2.SetWindow("mm", 20*time.Minute)
	record(t, tr2, "mm", 0, 0.10, nil, nil, "", &past)
	record(t, tr2, "mm", 6, 0.16, nil, nil, "", &past)
	est = one(t, tr2, "mm")
	if _, ok := est.Project(at(5)); ok {
		t.Error("projection accepted with ResetAt ≤ LastAt")
	}

	// Collecting and Unavailable never project.
	tr3 := NewTracker()
	tr3.SetWindow("mm", 20*time.Minute)
	reset := at(300)
	record(t, tr3, "mm", 0, 0.10, nil, nil, "", &reset)
	est = one(t, tr3, "mm")
	if _, ok := est.Project(at(5)); ok {
		t.Error("Collecting state projected")
	}
	record(t, tr3, "mm", 6, 0.05, nil, nil, "", &reset) // rollover
	est = one(t, tr3, "mm")
	if _, ok := est.Project(at(5)); ok {
		t.Error("Unavailable state projected")
	}
}

// The spend series behaves like a monotonic counter series: speed in $/h,
// decrease is a discontinuity, and it can never project.
func TestSpendSeries(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("or", 60*time.Minute) // the 30m delta must fit the window
	tr.ObserveSpend("or", at(0), 10.0)
	tr.ObserveSpend("or", at(30), 11.0)

	ip := tr.States()["or"]
	if ip.Spend == nil {
		t.Fatal("no spend estimate")
	}
	est := *ip.Spend
	if est.State != StateActive || !est.HasCount || est.Unit != "$" {
		t.Fatalf("spend state=%v HasCount=%v Unit=%q, want Active/$", est.State, est.HasCount, est.Unit)
	}
	if est.SpeedCount != 2.0 {
		t.Errorf("spend speed = %v, want $2.00/h", est.SpeedCount)
	}
	if _, ok := est.Project(at(60)); ok {
		t.Error("spend projected without a reset time")
	}

	// A spend decrease (refund, correction) is a discontinuity.
	tr.ObserveSpend("or", at(40), 5.0)
	if est := *tr.States()["or"].Spend; est.State != StateUnavailable {
		t.Errorf("after spend decrease: state = %v, want Unavailable", est.State)
	}
}

// Multiple quota windows are independent series; removing the tracker's
// instance drops everything including its window setting.
func TestPerWindowSeriesAndReset(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("inst", 60*time.Minute)
	reset := at(300)
	a := domain.QuotaWindow{ID: "interval", UsedFraction: 0.10, ResetAt: &reset}
	b := domain.QuotaWindow{ID: "weekly", UsedFraction: 0.50, ResetAt: &reset}
	tr.ObserveQuota("inst", at(0), []domain.QuotaWindow{a, b})
	a.UsedFraction, b.UsedFraction = 0.20, 0.60
	tr.ObserveQuota("inst", at(6), []domain.QuotaWindow{a, b})

	ip := tr.States()["inst"]
	if len(ip.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(ip.Windows))
	}
	if !near(ip.Windows["interval"].SpeedFrac, 1.0) || !near(ip.Windows["weekly"].SpeedFrac, 1.0) {
		t.Errorf("speeds = %v/%v, want 1.0/1.0", ip.Windows["interval"].SpeedFrac, ip.Windows["weekly"].SpeedFrac)
	}

	// A rollover in one window leaves the other untouched.
	a.UsedFraction = 0.01
	tr.ObserveQuota("inst", at(12), []domain.QuotaWindow{a})
	ip = tr.States()["inst"]
	if ip.Windows["interval"].State != StateUnavailable {
		t.Errorf("rolled window = %v, want Unavailable", ip.Windows["interval"].State)
	}
	if ip.Windows["weekly"].State != StateActive {
		t.Errorf("untouched window = %v, want Active", ip.Windows["weekly"].State)
	}

	tr.Reset("inst")
	if ip, ok := tr.States()["inst"]; ok {
		t.Fatalf("state survived Reset: %+v", ip)
	}
	// The window setting is gone too: fresh samples 30m apart fit the old
	// 60m window but not the default 20m one, so the in-window span is 0
	// and the state is Collecting until enough new history accumulates.
	a.UsedFraction = 0.01
	tr.ObserveQuota("inst", at(20), []domain.QuotaWindow{a})
	a.UsedFraction = 0.05
	tr.ObserveQuota("inst", at(50), []domain.QuotaWindow{a})
	if est := tr.States()["inst"].Windows["interval"]; est.State != StateCollecting {
		t.Errorf("after reset with the default window: state = %v, want Collecting", est.State)
	}
	tr.SetWindow("inst", 60*time.Minute)
	if est := tr.States()["inst"].Windows["interval"]; est.State != StateActive {
		t.Errorf("with the long window restored: state = %v, want Active", est.State)
	}
}

// States() returns detached maps: mutating the result must not reach the
// tracker.
func TestStatesCopyIsDetached(t *testing.T) {
	tr := NewTracker()
	tr.SetWindow("mm", 20*time.Minute)
	reset := at(300)
	record(t, tr, "mm", 0, 0.10, nil, nil, "", &reset)
	record(t, tr, "mm", 6, 0.16, nil, nil, "", &reset)

	states := tr.States()
	states["mm"].Windows[string("mm")] = Estimate{State: StateUnavailable}
	delete(states, "mm")

	est := one(t, tr, "mm")
	if est.State != StateActive {
		t.Errorf("tracker state changed through the copy: %+v", est)
	}
}

// The tracker is shared by loop goroutines, the UI thread, and the save
// goroutine; a concurrent hammer must not race (run with -race).
func TestConcurrentAccess(t *testing.T) {
	tr := NewTracker()
	reset := at(300)
	// Same IDs as before, without int→rune conversions (gosec G115);
	// "c" stays deliberately shared with the Reset/SetWindow goroutine.
	ids := [...]domain.InstanceID{"a", "b", "c", "d"}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := ids[n]
			tr.SetWindow(id, 20*time.Minute)
			for i := 0; i < 200; i++ {
				rec := domain.QuotaWindow{ID: "interval", UsedFraction: float64(i) / 1000, ResetAt: &reset}
				tr.ObserveQuota(id, at(float64(i)), []domain.QuotaWindow{rec})
				tr.ObserveSpend(id, at(float64(i)), float64(i))
				_ = tr.States()
			}
		}(w)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			tr.Reset("c")
			tr.SetWindow("c", 10*time.Minute)
		}
	}()
	wg.Wait()
}
