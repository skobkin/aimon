// Pace view models: the pure-data bridge between the derived estimates of
// internal/pace and the rendered quota rows, donuts and spend cards (S7).
// Everything here is plain data; the only time-relative method,
// ProjectionExpired, feeds the monitor's per-second ticker so the ticker
// never recomputes and never rebuilds.
package ui

import (
	"fmt"
	"math"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/pace"
)

// PaceVM carries one series' derived pace. SpeedText is empty until an
// estimate exists (Collecting/Unavailable render placeholders in the
// widgets). The projection inputs are carried alongside the precomputed
// text so a fresh rebuild is all it takes to refresh the row — the
// projection is constant between samples (DECISION_LOG 2026-10-24).
type PaceVM struct {
	State pace.State
	// SpeedText renders the speed ("3.4%/h", "12.4k credits/h", "$1.24/h");
	// empty while no estimate exists.
	SpeedText string
	// CanProject reports that a fresh projection was computable at build
	// time. ProjectedText then reads "≈68% at reset" (unclamped:
	// "≈137% at reset") and Projected carries the unclamped fraction for
	// the ProjectionBar.
	CanProject    bool
	ProjectedText string
	Projected     float64

	SpeedFrac float64
	LastUsed  float64
	LastAt    time.Time
	ResetAt   *time.Time
}

// ProjectionExpired reports whether the projection has gone stale: once the
// current time has reached the window reset, the projected value no longer
// describes the running window and the widgets hide it until the next
// fresh sample delivers a new reset time. The 1-second ticker calls this;
// nothing recalculates.
func (vm PaceVM) ProjectionExpired(now time.Time) bool {
	return vm.ResetAt != nil && !now.Before(*vm.ResetAt)
}

// buildPaceVM derives the pace VM of one quota window estimate. now stamps
// the projection's freshness: a projection already past its reset renders
// as non-projectable and the ticker has nothing to hide.
func buildPaceVM(est pace.Estimate, now time.Time) *PaceVM {
	vm := &PaceVM{
		State:     est.State,
		SpeedFrac: est.SpeedFrac,
		LastUsed:  est.LastUsed,
		LastAt:    est.LastAt,
		ResetAt:   est.ResetAt,
	}
	switch est.State {
	case pace.StateActive, pace.StateIdle:
		if est.HasCount {
			vm.SpeedText = FormatCountPerHour(est.SpeedCount, est.Unit)
		} else {
			// SpeedFrac is a used-fraction per hour (0.2 ⇒ 20%/h); the
			// formatter speaks percent.
			vm.SpeedText = FormatPctPerHour(est.SpeedFrac * 100)
		}
	default:
		return vm
	}

	if projected, ok := est.Project(now); ok {
		vm.CanProject = true
		vm.Projected = projected
		vm.ProjectedText = "≈" + formatProjectedPct(projected) + " at reset"
	}

	return vm
}

// Caption renders the detail row's pace line (S7_UI_UX): the speed with
// the projection when fresh, or a state placeholder while estimating.
func (vm PaceVM) Caption() string {
	switch vm.State {
	case pace.StateCollecting:
		return "Pace collecting…"
	case pace.StateUnavailable:
		return "Pace —"
	}
	if vm.CanProject {
		return vm.SpeedCaption() + " · " + vm.ProjectedText
	}

	return vm.SpeedCaption()
}

// SpeedCaption is the caption without the projection part — the line
// shown once the projection has expired at the window reset (the speed
// itself stays meaningful).
func (vm PaceVM) SpeedCaption() string {
	switch vm.State {
	case pace.StateCollecting:
		return "Pace collecting…"
	case pace.StateUnavailable:
		return "Pace —"
	}

	return "Pace " + vm.SpeedText
}

// buildSpendPaceVM derives the OpenRouter spend pace. The spend series has
// no reset time by S4 policy, so it can never project — by construction,
// not by convention.
func buildSpendPaceVM(est pace.Estimate) *PaceVM {
	vm := &PaceVM{State: est.State}
	switch est.State {
	case pace.StateActive, pace.StateIdle:
		vm.SpeedText = FormatMoneyPerHour(est.SpeedCount)
	}

	return vm
}

// FormatPctPerHour renders a percent-per-hour speed: "0%/h",
// "<0.1%/h", "3.4%/h", "124%/h" — the per-minute rate grammar stretched to
// the pace horizon. The argument is already in percent (multiply a
// used-fraction-per-hour by 100 before calling).
func FormatPctPerHour(v float64) string {
	switch {
	case v == 0:
		return "0%/h"
	case v < 0.1:
		return "<0.1%/h"
	case v < 10:
		if v == math.Trunc(v) {
			return fmt.Sprintf("%.0f%%/h", v)
		}

		return fmt.Sprintf("%.1f%%/h", v)
	default:
		return FormatCount(v) + "%/h"
	}
}

// FormatCountPerHour renders a count-unit speed ("12.4k credits/h"); an
// empty unit falls back to a bare "/h". It stretches FormatCount with the
// same grammar FormatPctPerHour uses for percent speeds: sub-unit burns
// render as "<0.1" and sub-ten speeds keep one decimal, so a slow burn
// never reads as zero the way "0 credits/h" would.
func FormatCountPerHour(v float64, unit string) string {
	s := ""
	switch {
	case v > 0 && v < 0.1:
		s = "<0.1"
	case v > 0 && v < 10 && v != math.Trunc(v):
		s = fmt.Sprintf("%.1f", v)
	default:
		s = FormatCount(v)
	}
	if unit == "" {
		return s + "/h"
	}

	return s + " " + unit + "/h"
}

// FormatMoneyPerHour renders a spend speed ("$1.24/h"); sub-cent burns keep
// their decimals the way FormatMoney does, so slow spend never reads as
// "$0.00/h" while positive.
func FormatMoneyPerHour(v float64) string {
	return FormatMoney(v) + "/h"
}

// formatProjectedPct renders the unclamped projected percentage: real usage
// never displays as zero ("<1%"), and an over-100% projection keeps its
// true value ("137%") — only the rendered bar segments are capped.
func formatProjectedPct(fraction float64) string {
	pct := int(math.Round(fraction * 100))
	if pct == 0 && fraction > 0 {
		return "<1%"
	}

	return fmt.Sprintf("%d%%", pct)
}
