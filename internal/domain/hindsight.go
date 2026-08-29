package domain

import "time"

// HindsightSnapshot is one aggregate observation of a whole Hindsight
// deployment (docs/S5_IMPLEMENTATION_SPEC.md §3). It is deliberately
// deployment-wide: no bank ID, name, mission, or memory content ever has a
// field here — the adapter aggregates the bank list and sums Prometheus
// series before anything reaches this type. Nil pointer fields mean the
// upstream metric or field was absent, which the UI renders as
// unavailable — never as zero.
type HindsightSnapshot struct {
	CollectedAt time.Time

	Service HindsightService
	// Memory is nil until the first slow refresh lands the bank aggregate.
	// The pointer, not the zero value, marks availability: a valid empty
	// deployment aggregates to Banks 0 / Facts 0, which must render as real
	// counts, not as "no data yet".
	Memory   *HindsightMemory
	Activity HindsightActivity
	Runtime  HindsightRuntime
	// Queue is nil when the deployment exposed no backlog metric family at
	// all (they are disabled by default upstream); any populated field
	// inside is independently optional.
	Queue *HindsightQueue
}

// SnapshotKind implements Snapshot.
func (s HindsightSnapshot) SnapshotKind() SourceKind { return KindHindsight }

// Timestamp implements Snapshot.
func (s HindsightSnapshot) Timestamp() time.Time { return s.CollectedAt }

// HindsightService is the service identity block from /health and
// (slow-cached) /health/live. Version and uptime arrive only after the
// first slow refresh.
type HindsightService struct {
	// Status is the /health status string ("healthy" on a working
	// deployment); required by the adapter, so always non-empty on a
	// landed snapshot.
	Status string
	// Database is the /health database state ("connected").
	Database string
	// Version is the /health/live version string.
	Version string
	// Uptime is the /health/live process uptime; 0 when unknown.
	Uptime time.Duration
	// DBAcquireMS is the optional /health db_acquire_ms diagnostic.
	DBAcquireMS *float64
	// DBPoolWaiting is the optional /health db_pool_waiting readiness
	// diagnostic. Upstream documents it here rather than guaranteeing a
	// Prometheus waiting gauge, so it is the preferred source for the DB
	// Waiting row; HindsightRuntime.DBPoolWaiting is the fallback.
	DBPoolWaiting *float64
}

// HindsightMemory aggregates every bank of the deployment. Banks is the
// list response's total; Facts sums fact_count across all pages. The
// timestamps are the max non-null values seen.
type HindsightMemory struct {
	Banks          int64
	Facts          int64
	LastWriteAt    *time.Time
	LastDocumentAt *time.Time
}

// HindsightActivity carries counter-delta rates per minute. A nil rate
// means "not derivable yet" (first sample, counter reset, or absent
// family) — availability, not zero.
type HindsightActivity struct {
	RetainPerMin      *float64
	RecallPerMin      *float64
	ReflectPerMin     *float64
	ConsolidatePerMin *float64

	LLMCallsPerMin     *float64
	InputTokensPerMin  *float64
	OutputTokensPerMin *float64

	HTTP5xxPercent *float64
}

// HindsightRuntime holds the current gauges read from /metrics. DB pool
// active is derived by the view model as max(size-idle, 0); the raw
// values stay here.
type HindsightRuntime struct {
	DBPoolSize    *float64
	DBPoolIdle    *float64
	DBPoolMax     *float64
	DBPoolWaiting *float64 // fallback for Service.DBPoolWaiting

	HTTPInProgress *float64
}

// HindsightQueue aggregates the optional backlog gauges. Each field is
// independent: a deployment exposing some but not all families leaves the
// missing ones nil.
type HindsightQueue struct {
	Pending    *float64
	Processing *float64
	Failed     *float64

	ConsolidationBacklog *float64
	ConsolidationFailed  *float64
}
