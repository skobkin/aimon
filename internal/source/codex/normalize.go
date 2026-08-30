package codex

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// Codex quota window IDs. They are stable machine identifiers in the same
// spirit as the other quota kinds' ("interval", "credits-5h"); the UI uses
// them for headline selection and short donut labels.
const (
	windowIDPrimary   = "codex-primary"
	windowIDSecondary = "codex-secondary"
)

// errInvalidResponse marks a response that decoded but carries no usable
// quota. It never carries provider payloads into the UI or the log.
var errInvalidResponse = errors.New("no usable rate-limit window in the response")

// percentDriftTolerance is the float drift around 0/100 that is clamped
// instead of rejected; anything beyond it is a value this monitor refuses
// to present.
const percentDriftTolerance = 0.5

// selectSnapshot picks the limit family: the "codex" entry of the by-ID map
// when present, the top-level snapshot as fallback, and otherwise an error
// (docs/S6_AGENT_RULES.md §11).
func selectSnapshot(result *rateLimitsReadResult) (*rateLimitSnapshot, *resetCreditsSummary, error) {
	if result == nil {
		return nil, nil, errInvalidResponse
	}
	if result.RateLimitsByID != nil {
		if snap, ok := result.RateLimitsByID["codex"]; ok && snap != nil {
			return snap, result.ResetCredits, nil
		}
	}
	if result.RateLimits != nil {
		return result.RateLimits, result.ResetCredits, nil
	}

	return nil, nil, errInvalidResponse
}

// buildSnapshot normalizes the decoded result into the neutral domain
// snapshot. One usable window is enough; zero usable windows is an invalid
// response. Optional metadata stays optional: absent fields never become
// fake zeros.
func buildSnapshot(result *rateLimitsReadResult, now time.Time) (domain.CodexSnapshot, error) {
	snap, credits, err := selectSnapshot(result)
	if err != nil {
		return domain.CodexSnapshot{}, err
	}

	out := domain.CodexSnapshot{CollectedAt: now}
	positions := []struct {
		w        *window
		id       string
		fallback string
	}{
		{snap.Primary, windowIDPrimary, "Primary"},
		{snap.Secondary, windowIDSecondary, "Secondary"},
	}
	for _, p := range positions {
		if p.w == nil {
			continue
		}
		w, err := buildWindow(p.w, p.id, p.fallback)
		if err != nil {
			// A malformed window is skipped, not fatal: the sibling window
			// may still be perfectly usable.
			continue
		}
		out.Windows = append(out.Windows, w)
	}
	if len(out.Windows) == 0 {
		return domain.CodexSnapshot{}, errInvalidResponse
	}

	out.PlanType = trimOptional(snap.PlanType)
	out.RateLimitReachedType = trimOptional(snap.RateLimitReachedType)
	if credits != nil {
		out.ResetCreditsAvailable = credits.AvailableCount
	}

	return out, nil
}

// buildWindow normalizes one window: a sane used percent is required, the
// reset timestamp is only adopted when it is a plausible Unix-seconds
// value, and the duration label is purely presentational.
func buildWindow(w *window, id, fallbackName string) (domain.QuotaWindow, error) {
	if w.UsedPercent == nil {
		return domain.QuotaWindow{}, errInvalidResponse
	}
	pct := *w.UsedPercent
	if math.IsNaN(pct) || math.IsInf(pct, 0) {
		return domain.QuotaWindow{}, errInvalidResponse
	}
	// Materially out-of-range values are rejected; tiny drift at the
	// boundaries is clamped (docs/S6_CODEX.md).
	if pct < -percentDriftTolerance || pct > 100+percentDriftTolerance {
		return domain.QuotaWindow{}, fmt.Errorf("used percent %v out of range", pct)
	}
	pct = math.Min(100, math.Max(0, pct))
	used := pct / 100

	out := domain.QuotaWindow{
		ID:                id,
		Name:              windowName(w.WindowDurationMins, fallbackName),
		UsedFraction:      used,
		RemainingFraction: 1 - used,
	}
	if w.ResetsAt != nil && *w.ResetsAt > 0 {
		at := time.Unix(*w.ResetsAt, 0)
		out.ResetAt = &at
	}

	return out, nil
}

// windowName derives the human window name from the duration when the
// provider exposed one, falling back to the stable positional name. The
// friendly duration spellings mirror the other quota kinds' names
// ("5-hour", "Weekly"); nothing here is a protocol assumption — unknown
// durations get a stable generated label.
func windowName(mins *int64, fallback string) string {
	if mins == nil || *mins <= 0 {
		return fallback
	}
	if name, ok := knownDurationNames[*mins]; ok {
		return name
	}

	return genericDurationName(*mins)
}

// knownDurationNames are the special-cased spellings (docs/S6_CODEX.md):
// 300 minutes -> 5-hour, 10080 -> Weekly, 60 -> 1-hour.
var knownDurationNames = map[int64]string{
	60:    "1-hour",
	300:   "5-hour",
	10080: "Weekly",
}

// genericDurationName renders any other duration as a stable human label:
// whole days first, then whole hours, then minutes.
func genericDurationName(mins int64) string {
	switch {
	case mins%1440 == 0:
		return fmt.Sprintf("%dd", mins/1440)
	case mins%60 == 0:
		return fmt.Sprintf("%dh", mins/60)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

func trimOptional(s *string) string {
	if s == nil {
		return ""
	}

	return strings.TrimSpace(*s)
}
