package domain

import "time"

// CodexSnapshot is one observation of the ChatGPT Codex subscription's
// rate-limit windows (docs/S6_IMPLEMENTATION_SPEC.md §2). Windows reuse the
// neutral QuotaWindow shape so the shared quota presentation applies
// unchanged. Optional provider metadata is pointer/string based: a nil
// pointer or an empty string means the field was absent upstream and must
// never render as a fake zero. No account email, account ID, auth token,
// reset-credit identifier, or raw RPC payload has a field here.
type CodexSnapshot struct {
	CollectedAt time.Time

	// Windows holds the usable rate-limit windows (one or two: primary and
	// secondary, treated independently for forward compatibility).
	Windows []QuotaWindow
	// PlanType is the subscription plan name where the provider exposed one
	// ("plus", "pro", …); empty when absent.
	PlanType string
	// RateLimitReachedType names the limit that is currently reached, when
	// the provider says so; empty otherwise.
	RateLimitReachedType string
	// ResetCreditsAvailable is the count of available rate-limit reset
	// credits; nil when the provider did not report it. Only the count is
	// ever carried — never the opaque per-credit identifiers.
	ResetCreditsAvailable *int64
}

// SnapshotKind implements Snapshot.
func (s CodexSnapshot) SnapshotKind() SourceKind { return KindCodex }

// Timestamp implements Snapshot.
func (s CodexSnapshot) Timestamp() time.Time { return s.CollectedAt }
