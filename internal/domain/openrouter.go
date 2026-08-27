package domain

import "time"

// OpenRouterSnapshot is one OpenRouter observation combining the two
// independent capabilities an instance may have configured
// (docs/S4_IMPLEMENTATION_SPEC.md §3):
//
//   - Key: per-key usage and the optional per-key spending limit, obtained
//     with the ordinary API key;
//   - Account: purchased credits, total account usage and the derived
//     balance, obtained with the Management key.
//
// A nil field means that capability was not configured for this snapshot
// (its credential slot is empty) — it is absence, not an error, and the UI
// simply omits the corresponding section. OpenRouter data is spend- and
// account-level money data rather than a subscription quota, so it does not
// force into QuotaSnapshot; there is deliberately no reset timestamp here —
// the current-key endpoint exposes only a reset cadence label.
type OpenRouterSnapshot struct {
	CollectedAt time.Time

	Key     *OpenRouterKeyUsage
	Account *OpenRouterAccountCredits
}

// SnapshotKind implements Snapshot.
func (s OpenRouterSnapshot) SnapshotKind() SourceKind { return KindOpenRouter }

// Timestamp implements Snapshot.
func (s OpenRouterSnapshot) Timestamp() time.Time { return s.CollectedAt }

// Cadence labels for the key spending-limit reset window
// (docs/S4_OPENROUTER.md §5). The empty value means "no recurring reset"
// or an unknown label; both render as a plain "Key limit".
const (
	CadenceDaily   = "daily"
	CadenceWeekly  = "weekly"
	CadenceMonthly = "monthly"
)

// OpenRouterLimit is the single optional per-key spending cap. It is one
// limit with a reset cadence label — never three separate period quotas.
type OpenRouterLimit struct {
	// Amount is the raw cap in credit/USD units; always finite and > 0.
	Amount float64
	// Remaining is the provider-reported remaining amount when present;
	// it is the source of the limit visualization (S4 spec §6).
	Remaining float64
	// Cadence is daily|weekly|monthly or "" when the key has no
	// recurring reset / reports an unknown label. No ResetAt exists:
	// inventing one from the local clock is forbidden (S4 rules §5).
	Cadence string
}

// OpenRouterKeyUsage is the usage block of the key used to authenticate the
// request. Spend values are credit/USD-denominated.
type OpenRouterKeyUsage struct {
	UsageTotal   float64
	UsageDaily   float64
	UsageWeekly  float64
	UsageMonthly float64

	// BYOK spend values are retained but not displayed in S4.
	BYOKTotal   float64
	BYOKDaily   float64
	BYOKWeekly  float64
	BYOKMonthly float64
	// IncludeBYOKInLimit mirrors include_byok_in_limit.
	IncludeBYOKInLimit bool

	Limit *OpenRouterLimit
}

// OpenRouterAccountCredits holds the account-level totals a Management key
// can read. Balance is derived at the adapter boundary as
// TotalCredits - TotalUsage (tiny negative noise clamped to zero).
type OpenRouterAccountCredits struct {
	TotalCredits float64
	TotalUsage   float64
	Balance      float64
}
