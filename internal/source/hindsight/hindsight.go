// Package hindsight adapts one Hindsight memory service onto a single
// aggregate domain snapshot (docs/S5_HINDSIGHT.md). Only four endpoints
// are polled:
//
//	GET /health                    fast (every refresh)
//	GET /metrics                   fast (every refresh)
//	GET /health/live               slow (adapter-local ~60s cache)
//	GET /v1/default/banks?limit=1000&offset=N   slow; paginated by the
//	    response's own total, or a single complete page when it has none
//
// No /version, no per-bank request, no LLM health probe. The source is
// deliberately deployment-summary-only: bank identity fields are never
// modeled and rates are derived in memory from Prometheus counter deltas.
package hindsight

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// Request tuning. requestTimeout bounds the WHOLE refresh, and the slow
// path chains /health + /metrics + /health/live + every bank page
// sequentially — the 3s local-daemon convention would spuriously fail
// large deployments (docs/DECISION_LOG.md, 2026-08-29).
const (
	requestTimeout = 10 * time.Second
	slowTTL        = 60 * time.Second
	bankPageSize   = 1000
)

// healthPayload is GET /health: readiness including database connectivity.
// status is required; every diagnostic is optional. Only the diagnostics
// this UI shows are modeled (db_acquire_ms, db_pool_waiting) — pool
// size/idle/max come from /metrics.
type healthPayload struct {
	Status        string   `json:"status"`
	Database      string   `json:"database"`
	DBAcquireMS   *float64 `json:"db_acquire_ms"`
	DBPoolWaiting *float64 `json:"db_pool_waiting"`
}

// livePayload is GET /health/live: liveness plus the version and uptime
// this UI shows. It does not touch the database, which is why it is slow
// data: it never changes fast enough to justify per-poll cost.
type livePayload struct {
	Status        string   `json:"status"`
	Version       string   `json:"version"`
	UptimeSeconds *float64 `json:"uptime_seconds"`
}

// slowState is the adapter-local slow-data cache. It is ephemeral — never
// persisted — and dies with the adapter, so a Base URL settings change
// resets it along with the rate baseline.
type slowState struct {
	fetchedAt time.Time
	version   string
	uptime    time.Duration
	banks     bankAggregates
}

// Adapter polls one Hindsight instance. Pointer type per the manager
// contract (replacement detected by pointer identity). The manager never
// overlaps two refreshes of one adapter, so the ephemeral state below
// needs no lock; Test runs on its own throwaway adapter and touches none
// of it.
type Adapter struct {
	baseURL  string
	account  credentials.AccountID
	creds    credentials.Store // nil => known-unauthenticated instance
	client   *http.Client
	log      *slog.Logger
	now      func() time.Time // time seam; tests inject a fake clock
	pageSize int              // bank page size; tests shrink it

	// slow is nil until the first successful slow fetch. baseline holds
	// the previous successful /metrics counters (nil before the first) so
	// the next successful refresh can derive rates. Both commit only when
	// the whole Refresh succeeds. liveBroken tracks whether the last
	// liveness fetch failed, so its warning fires on the transition into
	// the degraded state instead of on every slow cycle — a permanently
	// misrouted /health/live would otherwise repeat it every ~60s.
	// everSeen latches the activity families any successful scrape has
	// exposed, so the UI can hide a family the deployment never exports
	// instead of showing its rate row as a permanent dash.
	slow       *slowState
	baseline   *metricsCounters
	baselineAt time.Time
	liveBroken bool
	everSeen   domain.HindsightActivitySeen
}

// New creates an adapter for the configured API base URL, reading the
// optional API key (when used) from the instance-scoped keychain account
// on each refresh. The base URL is normalized here (spec §4): surrounding
// space is dropped and trailing slashes are stripped, so a user-entered
// "http://host/" joins into "http://host/health" rather than a
// double-slash path strict proxies reject.
func New(baseURL string, account credentials.AccountID, creds credentials.Store, client *http.Client, log *slog.Logger) *Adapter {
	return &Adapter{
		baseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		account:  account,
		creds:    creds,
		client:   client,
		log:      log,
		now:      time.Now,
		pageSize: bankPageSize,
	}
}

// Kind implements source.Source.
func (a *Adapter) Kind() domain.SourceKind { return domain.KindHindsight }

// RequestTimeout implements source.Source.
func (a *Adapter) RequestTimeout() time.Duration { return requestTimeout }

// Refresh implements source.Source. Everything is fetched into locals and
// the ephemeral state commits only on success (S5 spec §5): a failed slow
// fetch must not half-update the cache, and a failed refresh must not
// consume the counter baseline or the next interval's rates would lie.
func (a *Adapter) Refresh(ctx context.Context) (domain.Snapshot, error) {
	token, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	header := authHeader(token)
	now := a.now()

	health, err := a.fetchHealth(ctx, header)
	if err != nil {
		return nil, err
	}

	metricsBody, err := a.fetchMetrics(ctx, header)
	if err != nil {
		return nil, err
	}
	// A 200 body that is not parsable exposition is a failed metrics fetch:
	// treating it as an empty-but-successful scrape would publish a snapshot
	// and advance the counter baseline over broken data.
	samples, err := parseSelectedMetrics(metricsBody)
	if err != nil {
		return nil, err
	}
	counters, gauges, queue := aggregateMetrics(samples)

	// Slow data: fetch only when missing or past the TTL; otherwise serve
	// the cached liveness/bank aggregates. Liveness is cosmetic — version
	// and uptime only — so a broken or badly routed /health/live degrades
	// those two fields instead of failing the refresh and going dark over a
	// proxy rule (DECISION_LOG, 2026-08-29). The bank walk stays required:
	// its failure leaves no slow data worth showing.
	var slow *slowState
	if a.slowExpired(now) {
		st := &slowState{fetchedAt: now}
		if live, err := a.fetchLive(ctx, header); err != nil {
			// The warning belongs to the transition into the degraded
			// state, not to every slow cycle; staying broken keeps
			// reporting at debug level only.
			if a.liveBroken {
				a.log.Debug("hindsight liveness fetch still failing; version and uptime unavailable", "error", err)
			} else {
				a.log.Warn("hindsight liveness fetch failed; version and uptime unavailable", "error", err)
			}
			a.liveBroken = true
		} else {
			a.liveBroken = false
			st.version = live.Version
			st.uptime = live.uptime
		}
		banks, err := a.fetchBanks(ctx, header)
		if err != nil {
			return nil, err
		}
		st.banks = banks
		slow = st
	}

	// Everything fallible has passed; latch this scrape's families into the
	// sticky seen-set so the snapshot carries the union including itself.
	a.everSeen = mergeSeen(a.everSeen, activitySeenFrom(counters))

	snap := domain.HindsightSnapshot{
		CollectedAt:  now,
		Service:      health,
		Activity:     deriveActivity(a.baseline, &counters, now.Sub(a.baselineAt)),
		ActivitySeen: a.everSeen,
		Runtime:      domainRuntime(gauges),
		Queue:        domainQueue(queue),
	}
	if slow != nil {
		snap.Service.Version = slow.version
		snap.Service.Uptime = slow.uptime
		snap.Memory = &domain.HindsightMemory{
			Banks:          slow.banks.Count,
			Facts:          slow.banks.Facts,
			LastWriteAt:    slow.banks.LastWriteAt,
			LastDocumentAt: slow.banks.LastDocumentAt,
		}
	} else if a.slow != nil {
		snap.Service.Version = a.slow.version
		snap.Service.Uptime = a.slow.uptime
		snap.Memory = &domain.HindsightMemory{
			Banks:          a.slow.banks.Count,
			Facts:          a.slow.banks.Facts,
			LastWriteAt:    a.slow.banks.LastWriteAt,
			LastDocumentAt: a.slow.banks.LastDocumentAt,
		}
	}

	// Commit: the refresh succeeded, so the candidate slow state and the
	// new counter baseline replace the old ones atomically.
	if slow != nil {
		a.slow = slow
	}
	a.baseline = &counters
	a.baselineAt = now
	return snap, nil
}

// TestOutcome is the Settings connection-test result: one probe per
// configured surface, reported by the registry as the four-line
// Health/Liveness/Metrics/Banks report.
type TestOutcome struct {
	// Err is a local failure (keychain) that prevented every probe.
	Err error
	// HealthErr covers /health; LiveErr covers /health/live.
	HealthErr error
	LiveErr   error
	// MetricsErr covers /metrics parse+fetch.
	MetricsErr error
	// BanksErr covers the bank-list probe; BanksTotal is the response's
	// total for the "Banks: OK (N visible)" line.
	BanksErr   error
	BanksTotal int64
}

// OK reports whether every probe succeeded. Nothing is verified when a
// local failure prevented all probes, so that is not OK either.
func (o TestOutcome) OK() bool {
	return o.Err == nil && o.HealthErr == nil && o.LiveErr == nil &&
		o.MetricsErr == nil && o.BanksErr == nil
}

// Test probes the configured surface without retrieving per-bank detail:
// the bank probe asks for one row. It uses only the stateless fetch
// helpers, so it never mutates the rate baseline or slow cache (S5 spec
// §15).
func (a *Adapter) Test(ctx context.Context) TestOutcome {
	var out TestOutcome
	token, err := a.token(ctx)
	if err != nil {
		out.Err = err
		return out
	}
	header := authHeader(token)

	if _, err := a.fetchHealth(ctx, header); err != nil {
		out.HealthErr = err
	}
	if _, err := a.fetchLive(ctx, header); err != nil {
		out.LiveErr = err
	}
	if body, err := a.fetchMetrics(ctx, header); err != nil {
		out.MetricsErr = err
	} else if _, err := parseSelectedMetrics(body); err != nil {
		out.MetricsErr = err
	}
	banks, err := a.fetchBanksPage(ctx, header, 1, 0)
	if err != nil {
		out.BanksErr = err
	} else {
		out.BanksTotal = banks.agg.Count
	}
	return out
}

// slowExpired reports whether the slow data needs a refresh at now.
func (a *Adapter) slowExpired(now time.Time) bool {
	return a.slow == nil || now.Sub(a.slow.fetchedAt) >= slowTTL
}

// fetchMetrics reads the Prometheus exposition text from /metrics. GetText
// reports *source.Error, so the result is unwrapped here: returning the
// pointer directly from a function whose result type is error would box a
// successful nil into a non-nil interface.
func (a *Adapter) fetchMetrics(ctx context.Context, header http.Header) (string, error) {
	body, err := source.GetText(ctx, a.client, a.endpoint("/metrics"), header)
	if err != nil {
		return "", err
	}
	return body, nil
}

// fetchHealth reads /health. A missing status is an invalid response; the
// optional diagnostics pass through as absence. Like every helper here it
// returns error (not *source.Error), so a success can never box a typed-nil
// pointer into the Refresh error return.
func (a *Adapter) fetchHealth(ctx context.Context, header http.Header) (domain.HindsightService, error) {
	var p healthPayload
	if err := source.GetJSON(ctx, a.client, a.endpoint("/health"), header, &p); err != nil {
		return domain.HindsightService{}, err
	}
	if p.Status == "" {
		return domain.HindsightService{}, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "health response missing status",
		}
	}
	return domain.HindsightService{
		Status:        p.Status,
		Database:      p.Database,
		DBAcquireMS:   p.DBAcquireMS,
		DBPoolWaiting: p.DBPoolWaiting,
	}, nil
}

// live aggregates the /health/live payload into slow-data values.
type live struct {
	Version string
	uptime  time.Duration
}

// fetchLive reads /health/live. A missing version is tolerated (older
// deployments); a missing uptime simply leaves 0.
func (a *Adapter) fetchLive(ctx context.Context, header http.Header) (live, error) {
	var p livePayload
	if err := source.GetJSON(ctx, a.client, a.endpoint("/health/live"), header, &p); err != nil {
		return live{}, err
	}
	var uptime time.Duration
	if p.UptimeSeconds != nil && *p.UptimeSeconds > 0 {
		uptime = time.Duration(*p.UptimeSeconds * float64(time.Second))
	}
	return live{Version: p.Version, uptime: uptime}, nil
}

// endpoint joins the configured base URL with an API path.
func (a *Adapter) endpoint(path string) string {
	return a.baseURL + path
}

// token resolves the credential for this request batch. No stored key is
// a normal state (unauthenticated deployments are first-class); a keychain
// problem is not (llama-swap semantics).
func (a *Adapter) token(ctx context.Context) (string, error) {
	if a.creds == nil {
		return "", nil
	}
	token, err := a.creds.Get(ctx, a.account)
	if err == nil {
		return token, nil
	}
	if errors.Is(err, credentials.ErrNotFound) {
		return "", nil
	}
	var stateErr *credentials.StateError
	if errors.As(err, &stateErr) {
		return "", &source.Error{
			Category: source.CatCredentials,
			Detail:   "Keychain unavailable: " + stateErr.Message,
			Err:      stateErr,
		}
	}
	return "", &source.Error{
		Category: source.CatCredentials,
		Detail:   "Keychain read failed",
		Err:      err,
	}
}

// authHeader builds the Bearer header only when a token exists; an empty
// token means the request is made without Authorization at all.
func authHeader(token string) http.Header {
	if token == "" {
		return nil
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}
