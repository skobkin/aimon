// Package domain defines the source-independent snapshot types and state
// model shared by adapters, the source manager, and the UI layer.
// Provider-specific DTOs never cross into this package.
package domain

import "time"

// Category groups sources by where the monitored service runs.
type Category string

// Source categories: local daemons on this machine vs. remote SaaS plans.
const (
	CategoryLocal  Category = "local"
	CategoryRemote Category = "remote"
)

// SourceKind identifies one compiled source adapter kind, such as
// "amdgputop-web". It distinguishes adapter implementations, not configured
// source instances, and never serves as a unique configured identity.
type SourceKind string

// The four compiled source kinds.
const (
	KindAMDGPUWeb SourceKind = "amdgputop-web"
	KindLlamaSwap SourceKind = "llama-swap"
	KindMiniMax   SourceKind = "minimax"
	KindZAI       SourceKind = "zai"
)

// Category reports where the source runs. Source kinds are compile-time
// constants in this application; any other value groups with remote.
func (kind SourceKind) Category() Category {
	switch kind {
	case KindAMDGPUWeb, KindLlamaSwap:
		return CategoryLocal
	default:
		return CategoryRemote
	}
}

// DisplayName returns the kind's human-readable default name used in cards
// and settings.
func (kind SourceKind) DisplayName() string {
	switch kind {
	case KindAMDGPUWeb:
		return "amdgputop-web"
	case KindLlamaSwap:
		return "llama-swap"
	case KindMiniMax:
		return "MiniMax"
	case KindZAI:
		return "Z.AI / GLM"
	}
	return string(kind)
}

// Snapshot is one successful observation from a source. Implementations are
// immutable once Refresh returns; the manager hands them to the UI as-is.
type Snapshot interface {
	// SnapshotKind reports which source produced this snapshot.
	SnapshotKind() SourceKind
	// Timestamp reports when the data was collected.
	Timestamp() time.Time
}

// State is the coarse per-source state shown in the UI (DECISIONS D6).
type State string

// Per-source states. Disabled sources are omitted from the UI entirely;
// enabled-but-failing sources stay visible as Unavailable/Stale.
const (
	StateDisabled    State = "Disabled"
	StateLoading     State = "Loading"
	StateHealthy     State = "Healthy"
	StateStale       State = "Stale"
	StateUnavailable State = "Unavailable"
)

// Runtime is everything the manager knows about one source right now.
// Values are copied out on notification; fields are never mutated in place
// after publication.
type Runtime struct {
	Enabled       bool
	Refreshing    bool
	LastAttemptAt time.Time
	LastSuccessAt time.Time
	LastError     error
	// Snapshot is nil until the first successful refresh; a later failure
	// never clears it (last-good data is retained, DECISIONS D6).
	Snapshot Snapshot
}

// DeriveState maps a runtime onto the UI state model. A source with no
// snapshot yet is Loading until an attempt fails (then Unavailable); once a
// snapshot exists it is Healthy while refreshes succeed and Stale after a
// failure, with the previous data still on display.
func (r Runtime) DeriveState() State {
	if !r.Enabled {
		return StateDisabled
	}
	if r.Snapshot == nil {
		if r.LastError != nil {
			return StateUnavailable
		}
		return StateLoading
	}
	if r.LastError != nil {
		return StateStale
	}
	return StateHealthy
}
