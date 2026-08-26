package domain

import (
	"fmt"
	"math"
	"time"
)

// QuotaWindow is one usage window of a subscription plan (for example a
// 5-hour rolling interval and a weekly cap). RemainingFraction is the
// presentation primitive everything downstream consumes; raw counts are
// attached only when the provider's semantics are verified.
type QuotaWindow struct {
	// ID is a stable machine identifier ("interval", "weekly",
	// "tokens-5h", "mcp-monthly", "credits-5h").
	ID string
	// Name is the human-readable window name shown in the UI.
	Name string

	RemainingFraction float64
	UsedFraction      float64

	// Remaining and Limit carry the raw provider counts when trustworthy;
	// nil when only a percentage is known.
	Remaining *float64
	Limit     *float64
	// Unit labels the raw counts ("requests"); empty when unknown.
	Unit string
	// ResetAt is the window reset time when the provider exposes it with
	// verified semantics; nil otherwise.
	ResetAt *time.Time
}

// QuotaSnapshot is the quota state of one remote plan provider. Both quota
// kinds share this shape; Kind carries the adapter kind.
type QuotaSnapshot struct {
	Kind        SourceKind
	CollectedAt time.Time
	Plan        string
	Windows     []QuotaWindow
}

// SnapshotKind implements Snapshot.
func (s QuotaSnapshot) SnapshotKind() SourceKind { return s.Kind }

// Timestamp implements Snapshot.
func (s QuotaSnapshot) Timestamp() time.Time { return s.CollectedAt }

// Fraction validates that v is a usable quota fraction in [0, 1] and
// returns it unchanged. Validation happens before any clamping so callers
// can distinguish "provider sent a slightly-out-of-range value" (clamp it)
// from "provider sent garbage" (treat as an error).
func Fraction(v float64) (float64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("quota fraction is not a finite number: %v", v)
	}
	if v < 0 {
		return 0, fmt.Errorf("quota fraction is negative: %v", v)
	}
	if v > 1 {
		return 0, fmt.Errorf("quota fraction exceeds 1: %v", v)
	}
	return v, nil
}

// Clamp01 forces v into [0, 1]. NaN collapses to 0.
func Clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
