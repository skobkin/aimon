package hindsight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// memStore is a fake credential store; it never touches a keychain.
type memStore struct {
	secret   string
	notFound bool
	stateErr *credentials.StateError
}

func (s *memStore) Status(ctx context.Context) credentials.Status {
	return credentials.Status{State: credentials.StateAvailable}
}

func (s *memStore) Get(ctx context.Context, id credentials.AccountID) (string, error) {
	if s.stateErr != nil {
		return "", s.stateErr
	}
	if s.notFound || s.secret == "" {
		return "", credentials.ErrNotFound
	}
	return s.secret, nil
}

func (s *memStore) Set(ctx context.Context, id credentials.AccountID, secret string) error {
	s.secret = secret
	return nil
}

func (s *memStore) Delete(ctx context.Context, id credentials.AccountID) error {
	s.secret = ""
	return nil
}

const testAccount = credentials.AccountID("source/test-instance/api-key")

// fakeClock is the adapter's injected time source, so tests drive the slow
// cache TTL and the rate intervals without sleeping.
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

type hindsightRequest struct {
	uri  string // path + query, exactly as requested
	auth string // Authorization header, "" when absent
}

// hindsightServer serves the four Hindsight endpoints and records every
// request URI, so tests can assert the exact polling budget.
type hindsightServer struct {
	mu sync.Mutex

	healthCode  int
	healthBody  string
	liveCode    int
	liveBody    string
	metricsCode int
	metricsBody string
	// banksBody serves every bank-list request; banksPages, when set,
	// keys raw bodies by the offset query value instead (pagination).
	banksCode  int
	banksBody  string
	banksPages map[string]string

	requests []hindsightRequest
}

func newHindsightServer() *hindsightServer {
	return &hindsightServer{
		healthCode:  http.StatusOK,
		healthBody:  healthBodyOK,
		liveCode:    http.StatusOK,
		liveBody:    liveBodyOK,
		metricsCode: http.StatusOK,
		metricsBody: metricsBodyA,
		banksCode:   http.StatusOK,
		banksBody:   banksPage(3, bankItem(2, "one"), bankItem(5, "two"), bankItem(0, "three")),
	}
}

func (s *hindsightServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, hindsightRequest{uri: r.URL.RequestURI(), auth: r.Header.Get("Authorization")})
		s.mu.Unlock()

		switch r.URL.Path {
		case "/health":
			s.serve(w, s.healthCode, s.healthBody)
		case "/health/live":
			s.serve(w, s.liveCode, s.liveBody)
		case "/metrics":
			s.serve(w, s.metricsCode, s.metricsBody)
		case "/v1/default/banks":
			s.mu.Lock()
			pages, body := s.banksPages, s.banksBody
			s.mu.Unlock()
			if pages != nil {
				page, ok := pages[r.URL.Query().Get("offset")]
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(page))
				return
			}
			s.serve(w, s.banksCode, body)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *hindsightServer) serve(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write([]byte(body))
}

func (s *hindsightServer) uris() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.requests))
	for i, r := range s.requests {
		out[i] = r.uri
	}
	return out
}

func (s *hindsightServer) auths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.requests))
	for i, r := range s.requests {
		out[i] = r.auth
	}
	return out
}

// Fixture payloads. Sentinels (bank ids/names/missions) exist so the
// privacy drift guard can prove they never reach a snapshot.
const (
	healthBodyOK = `{"status":"healthy","database":"connected","db_acquire_ms":1.5,"db_pool_waiting":3}`
	// healthBodyNoWaiting omits both optional diagnostics.
	healthBodyNoWaiting = `{"status":"healthy","database":"connected"}`
	liveBodyOK          = `{"status":"ok","version":"0.9.2","uptime_seconds":3600}`
)

// metricsBodyA is a first scrape: no backlog families exposed, counters at
// their starting values.
const metricsBodyA = `# HELP hindsight_operation_total Operations processed.
# TYPE hindsight_operation_total counter
hindsight_operation_total{operation="retain",bank="b-1"} 10
hindsight_operation_total{operation="recall",bank="b-1"} 4
hindsight_operation_total{operation="reflect"} 0
hindsight_operation_total{operation="consolidation"} 0
hindsight_http_requests_total{status_class="2xx",route="/v1/retain"} 100
hindsight_http_requests_total{status_class="5xx",route="/v1/retain"} 2
hindsight_db_pool_size 10
hindsight_db_pool_idle 7
hindsight_db_pool_max 10
hindsight_db_pool_waiting 7
hindsight_http_requests_in_progress 1
hindsight_llm_calls_total{provider="p"} 5
hindsight_llm_tokens_input_total{provider="p"} 1200
hindsight_llm_tokens_output_total{provider="p"} 340
`

// metricsBodyB is a later scrape: every counter advanced and the optional
// backlog families appeared.
const metricsBodyB = `hindsight_operation_total{operation="retain"} 13
hindsight_operation_total{operation="recall"} 6
hindsight_operation_total{operation="reflect"} 1
hindsight_operation_total{operation="consolidation"} 2
hindsight_http_requests_total{status_class="2xx"} 110
hindsight_http_requests_total{status_class="5xx"} 3
hindsight_db_pool_size 10
hindsight_db_pool_idle 6
hindsight_db_pool_max 10
hindsight_db_pool_waiting 7
hindsight_http_requests_in_progress 2
hindsight_llm_calls_total 7
hindsight_llm_tokens_input_total 1800
hindsight_llm_tokens_output_total 400
hindsight_async_operations{operation_type="retain",status="pending"} 4
hindsight_async_operations{operation_type="retain",status="processing"} 1
hindsight_async_operations{operation_type="consolidation",status="failed"} 2
hindsight_consolidation_backlog 6
hindsight_consolidation_failed 1
`

// metricsBodyBacklogOnly exposes exactly one backlog family, so the queue
// block exists with every other field unavailable.
const metricsBodyBacklogOnly = `hindsight_consolidation_backlog 9
`

func banksPage(total int64, items ...string) string {
	return fmt.Sprintf(`{"banks":[%s],"total":%d,"limit":%d,"offset":0}`,
		strings.Join(items, ","), total, len(items))
}

// banksPageNoTotal serves the shape observed on deployments that do not
// paginate the bank list at all: no total, and no limit/offset echo.
func banksPageNoTotal(items ...string) string {
	return fmt.Sprintf(`{"banks":[%s]}`, strings.Join(items, ","))
}

func bankItem(facts int64, name string) string {
	return fmt.Sprintf(`{"bank_id":"b-%[1]s","name":"Bank %[1]s","mission_statement":"mission %[1]s","fact_count":%d,"last_write_at":"2026-08-29T10:00:00Z"}`,
		name, facts)
}

func newAdapter(serverURL string, store credentials.Store, clk *fakeClock) *Adapter {
	a := New(serverURL, testAccount, store, &http.Client{}, testLogger())
	a.now = clk.now
	return a
}

func mustRefresh(t *testing.T, a *Adapter) domain.HindsightSnapshot {
	t.Helper()
	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	hs, ok := snap.(domain.HindsightSnapshot)
	if !ok {
		t.Fatalf("snapshot type = %T, want HindsightSnapshot", snap)
	}
	return hs
}

func requireSourceError(t *testing.T, err error, want source.Category) *source.Error {
	t.Helper()
	var serr *source.Error
	if !errors.As(err, &serr) {
		t.Fatalf("expected *source.Error, got %T %v", err, err)
	}
	if serr.Category != want {
		t.Fatalf("category = %q, want %q (detail: %s)", serr.Category, want, serr.Detail)
	}
	return serr
}

func approx(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("rate unavailable, want %v", want)
	}
	if diff := *got - want; diff < -1e-6 || diff > 1e-6 {
		t.Fatalf("rate = %v, want %v", *got, want)
	}
}

// TestRefreshFirstRefreshRequestBudget pins the exact request sequence of
// the first refresh: fast endpoints, then the slow pair, with the bank
// list using the fixed 1000-row page size. /version and per-bank paths
// must never appear.
func TestRefreshFirstRefreshRequestBudget(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}))

	want := []string{
		"/health",
		"/metrics",
		"/health/live",
		"/v1/default/banks?limit=1000&offset=0",
	}
	got := srv.uris()
	if len(got) != len(want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, uri := range got {
		if strings.Contains(uri, "/version") {
			t.Errorf("forbidden /version request: %q", uri)
		}
		if strings.HasPrefix(uri, "/v1/default/banks/") {
			t.Errorf("forbidden per-bank request: %q", uri)
		}
	}
}

// TestRefreshFastPollTouchesHealthAndMetricsOnly verifies the steady-state
// budget: within the slow TTL only /health and /metrics are polled.
func TestRefreshFastPollTouchesHealthAndMetricsOnly(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, nil, clk)
	mustRefresh(t, a)

	clk.advance(5 * time.Second)
	mustRefresh(t, a)

	got := srv.uris()[4:]
	want := []string{"/health", "/metrics"}
	if len(got) != len(want) {
		t.Fatalf("fast-poll requests = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRefreshSlowDataRefetchedAfterTTL verifies the slow pair returns once
// the adapter-local cache expires.
func TestRefreshSlowDataRefetchedAfterTTL(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, nil, clk)
	mustRefresh(t, a)

	clk.advance(61 * time.Second)
	mustRefresh(t, a)

	got := srv.uris()[4:]
	want := []string{"/health", "/metrics", "/health/live", "/v1/default/banks?limit=1000&offset=0"}
	if len(got) != len(want) {
		t.Fatalf("post-TTL requests = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRefreshAggregatesBanksAndPaginates walks pages until the response's
// own total is covered, advancing by returned rows.
func TestRefreshAggregatesBanksAndPaginates(t *testing.T) {
	srv := newHindsightServer()
	srv.banksBody = ""
	srv.banksPages = map[string]string{
		"0": banksPage(5, bankItem(2, "a"), bankItem(3, "b")),
		"2": banksPage(5, bankItem(1, "c"), bankItem(4, "d")),
		"4": banksPage(5, bankItem(6, "e")),
	}
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, nil, clk)
	a.pageSize = 2
	snap := mustRefresh(t, a)

	wantURIs := []string{
		"/v1/default/banks?limit=2&offset=0",
		"/v1/default/banks?limit=2&offset=2",
		"/v1/default/banks?limit=2&offset=4",
	}
	got := srv.uris()[3:]
	if len(got) != len(wantURIs) {
		t.Fatalf("bank requests = %v, want %v", got, wantURIs)
	}
	for i := range wantURIs {
		if got[i] != wantURIs[i] {
			t.Errorf("bank request %d = %q, want %q", i, got[i], wantURIs[i])
		}
	}
	if snap.Memory.Banks != 5 {
		t.Errorf("Banks = %d, want the response total 5", snap.Memory.Banks)
	}
	if snap.Memory.Facts != 2+3+1+4+6 {
		t.Errorf("Facts = %d, want %d", snap.Memory.Facts, 2+3+1+4+6)
	}
	wantWrite := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	if snap.Memory.LastWriteAt == nil || !snap.Memory.LastWriteAt.Equal(wantWrite) {
		t.Errorf("LastWriteAt = %v, want %v", snap.Memory.LastWriteAt, wantWrite)
	}
}

// TestRefreshBankListEndsBeforeTotal rejects a walk that stalls before the
// response's total is covered.
func TestRefreshBankListEndsBeforeTotal(t *testing.T) {
	srv := newHindsightServer()
	srv.banksPages = map[string]string{
		"0": banksPage(10, bankItem(2, "a")),
		// offset 2 pages 404, so the walk cannot reach the total.
	}
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, nil, clk)
	a.pageSize = 2
	if _, err := a.Refresh(context.Background()); err == nil {
		t.Fatal("expected an error when the bank list ends before its total")
	}
}

func TestRefreshNegativeTotalInvalid(t *testing.T) {
	srv := newHindsightServer()
	srv.banksBody = banksPage(-1, bankItem(1, "a"))
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	_, err := newAdapter(server.URL, nil, &fakeClock{}).Refresh(context.Background())
	requireSourceError(t, err, source.CatInvalidResponse)
}

func TestRefreshSendsBearerTokenOnEveryRequest(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	a := newAdapter(server.URL, &memStore{secret: "hs-key"}, &fakeClock{})
	mustRefresh(t, a)

	for i, h := range srv.auths() {
		if h != "Bearer hs-key" {
			t.Errorf("request %d Authorization = %q, want the stored key", i, h)
		}
	}
}

func TestRefreshUnauthenticatedWithoutStoredKey(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	// Nil store (known-unauthenticated instance) and empty store must both
	// omit Authorization entirely.
	for _, store := range []credentials.Store{nil, &memStore{notFound: true}} {
		srv.requests = nil
		if _, err := newAdapter(server.URL, store, &fakeClock{}).Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		for i, h := range srv.auths() {
			if h != "" {
				t.Errorf("request %d must not carry Authorization, got %q", i, h)
			}
		}
	}
}

func TestKeychainLockedIsCredentialsError(t *testing.T) {
	server := httptest.NewServer(newHindsightServer().handler())
	defer server.Close()

	locked := &credentials.StateError{State: credentials.StateLocked, Message: "collection is locked"}
	_, err := newAdapter(server.URL, &memStore{stateErr: locked}, &fakeClock{}).Refresh(context.Background())
	requireSourceError(t, err, source.CatCredentials)
}

func TestHealthAuthFailure(t *testing.T) {
	srv := newHindsightServer()
	srv.healthCode = http.StatusUnauthorized
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	_, err := newAdapter(server.URL, nil, &fakeClock{}).Refresh(context.Background())
	requireSourceError(t, err, source.CatAuth)
}

func TestHealthMissingStatusInvalid(t *testing.T) {
	srv := newHindsightServer()
	srv.healthBody = `{"database":"connected"}`
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	_, err := newAdapter(server.URL, nil, &fakeClock{}).Refresh(context.Background())
	requireSourceError(t, err, source.CatInvalidResponse)
}

// TestFirstSampleRatesUnavailable: the first successful scrape has no
// baseline, so every derived rate is unavailable rather than zero.
func TestFirstSampleRatesUnavailable(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap := mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	act := snap.Activity
	if act.RetainPerMin != nil || act.RecallPerMin != nil || act.ReflectPerMin != nil ||
		act.ConsolidatePerMin != nil || act.LLMCallsPerMin != nil || act.InputTokensPerMin != nil ||
		act.OutputTokensPerMin != nil || act.HTTP5xxPercent != nil {
		t.Errorf("first sample must leave every rate unavailable, got %+v", act)
	}
}

// TestDerivesRatesAnd5xxPercent derives one interval of counter deltas,
// per-minute rates, and the 5xx share.
func TestDerivesRatesAnd5xxPercent(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, nil, clk)
	mustRefresh(t, a)

	clk.advance(60 * time.Second)
	srv.metricsBody = metricsBodyB
	snap := mustRefresh(t, a)

	act := snap.Activity
	approx(t, act.RetainPerMin, 3)
	approx(t, act.RecallPerMin, 2)
	approx(t, act.ReflectPerMin, 1)
	approx(t, act.ConsolidatePerMin, 2)
	approx(t, act.LLMCallsPerMin, 2)
	approx(t, act.InputTokensPerMin, 600)
	approx(t, act.OutputTokensPerMin, 60)
	approx(t, act.HTTP5xxPercent, 1.0/11.0*100)
}

// TestCounterResetSkipsOneIntervalThenRecovers: a counter restart makes
// that interval unavailable; the next interval derives normally against
// the new baseline.
func TestCounterResetSkipsOneIntervalThenRecovers(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, nil, clk)
	mustRefresh(t, a)

	clk.advance(60 * time.Second)
	srv.metricsBody = strings.Replace(metricsBodyB, `hindsight_operation_total{operation="retain"} 13`,
		`hindsight_operation_total{operation="retain"} 2`, 1)
	snap := mustRefresh(t, a)
	if snap.Activity.RetainPerMin != nil {
		t.Fatalf("reset interval must be unavailable, got %v", *snap.Activity.RetainPerMin)
	}
	// Recall had no reset and must still be derived from the same interval.
	approx(t, snap.Activity.RecallPerMin, 2)

	clk.advance(60 * time.Second)
	srv.metricsBody = strings.Replace(metricsBodyB, `hindsight_operation_total{operation="retain"} 13`,
		`hindsight_operation_total{operation="retain"} 5`, 1)
	snap = mustRefresh(t, a)
	approx(t, snap.Activity.RetainPerMin, 3)
}

func TestBacklogAbsentLeavesQueueNil(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap := mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	if snap.Queue != nil {
		t.Errorf("no backlog families exposed, Queue = %+v, want nil", snap.Queue)
	}
}

func TestBacklogAggregatedByStatus(t *testing.T) {
	srv := newHindsightServer()
	srv.metricsBody = metricsBodyB
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap := mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	q := snap.Queue
	if q == nil {
		t.Fatal("backlog families exposed, Queue = nil")
	}
	approx(t, q.Pending, 4)
	approx(t, q.Processing, 1)
	approx(t, q.Failed, 2)
	approx(t, q.ConsolidationBacklog, 6)
	approx(t, q.ConsolidationFailed, 1)
}

// TestBacklogPartialFamilyStillYieldsQueue covers a deployment exposing
// only some backlog families: the block exists and missing values stay
// unavailable.
func TestBacklogPartialFamilyStillYieldsQueue(t *testing.T) {
	srv := newHindsightServer()
	srv.metricsBody = metricsBodyBacklogOnly
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap := mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	if snap.Queue == nil {
		t.Fatal("one backlog family exposed, Queue = nil")
	}
	if snap.Queue.ConsolidationBacklog == nil || *snap.Queue.ConsolidationBacklog != 9 {
		t.Errorf("ConsolidationBacklog = %v, want 9", snap.Queue.ConsolidationBacklog)
	}
	if snap.Queue.Pending != nil || snap.Queue.Processing != nil || snap.Queue.Failed != nil {
		t.Errorf("absent async statuses must stay nil, got %+v", snap.Queue)
	}
}

// TestHealthDBPoolWaitingPreferredWithGaugeFallback pins the sourcing
// rule: the /health db_pool_waiting diagnostic is the preferred value for
// the DB Waiting row and lands on Service, while the Prometheus gauge
// lands on Runtime as the optional fallback.
func TestHealthDBPoolWaitingPreferredWithGaugeFallback(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap := mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	if snap.Service.DBPoolWaiting == nil || *snap.Service.DBPoolWaiting != 3 {
		t.Errorf("Service.DBPoolWaiting = %v, want the /health value 3", snap.Service.DBPoolWaiting)
	}
	if snap.Runtime.DBPoolWaiting == nil || *snap.Runtime.DBPoolWaiting != 7 {
		t.Errorf("Runtime.DBPoolWaiting = %v, want the gauge 7", snap.Runtime.DBPoolWaiting)
	}
	if snap.Service.DBAcquireMS == nil || *snap.Service.DBAcquireMS != 1.5 {
		t.Errorf("Service.DBAcquireMS = %v, want 1.5", snap.Service.DBAcquireMS)
	}

	srv.healthBody = healthBodyNoWaiting
	snap = mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	if snap.Service.DBPoolWaiting != nil {
		t.Errorf("absent /health diagnostic must stay nil, got %v", *snap.Service.DBPoolWaiting)
	}
	if snap.Runtime.DBPoolWaiting == nil || *snap.Runtime.DBPoolWaiting != 7 {
		t.Errorf("gauge fallback = %v, want 7", snap.Runtime.DBPoolWaiting)
	}
	if snap.Service.DBAcquireMS != nil {
		t.Errorf("absent db_acquire_ms must stay nil, got %v", *snap.Service.DBAcquireMS)
	}
}

func TestVersionAndUptimeArriveWithSlowData(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap := mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	if snap.Service.Version != "0.9.2" {
		t.Errorf("Version = %q, want 0.9.2", snap.Service.Version)
	}
	if snap.Service.Uptime != time.Hour {
		t.Errorf("Uptime = %v, want 1h", snap.Service.Uptime)
	}
	if snap.Service.Status != "healthy" || snap.Service.Database != "connected" {
		t.Errorf("health block = %+v", snap.Service)
	}
}

// TestFailedRefreshLeavesBaselineAndSlowCacheUncommitted: a refresh that
// fails partway must consume neither the counter baseline (the next good
// interval derives against the last good sample) nor replace the slow
// cache with a half-updated candidate.
func TestFailedRefreshLeavesBaselineAndSlowCacheUncommitted(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, nil, clk)
	mustRefresh(t, a) // baseline: retain=10

	// Past the TTL the slow pair is refetched; the bank list fails after
	// /metrics already offered advanced counters (retain=13 in body B).
	clk.advance(61 * time.Second)
	srv.metricsBody = metricsBodyB
	srv.banksCode = http.StatusInternalServerError
	if _, err := a.Refresh(context.Background()); err == nil {
		t.Fatal("expected the failing bank page to fail the refresh")
	}
	if a.baseline == nil || *a.baseline.RetainTotal != 10 {
		t.Fatalf("failed refresh must keep the old baseline, got %+v", a.baseline)
	}

	// Recovery: banks serve again with the same advanced counters. The
	// delta derives against the pre-failure baseline (13-10), proving the
	// failed attempt consumed nothing.
	clk.advance(1 * time.Second)
	srv.banksCode = http.StatusOK
	snap := mustRefresh(t, a)
	approx(t, snap.Activity.RetainPerMin, 3.0/62.0*60)
	if snap.Service.Version != "0.9.2" {
		t.Errorf("Version = %q, want the cached slow value", snap.Service.Version)
	}
	if a.baseline == nil || *a.baseline.RetainTotal != 13 {
		t.Errorf("committed baseline = %+v, want the recovered sample", a.baseline)
	}
}

// TestSnapshotMarshalContainsNoBankIdentity is the privacy drift guard:
// the JSON shape of a snapshot must not carry any bank identifier, name,
// mission, or memory content even when the fixtures contain them.
func TestSnapshotMarshalContainsNoBankIdentity(t *testing.T) {
	srv := newHindsightServer()
	const (
		sentinelID      = "b-priv-7f3a"
		sentinelName    = "Project Atlassium"
		sentinelMission = "Classified Atlassium mission text"
	)
	srv.banksBody = fmt.Sprintf(
		`{"banks":[{"bank_id":%q,"name":%q,"mission_statement":%q,"fact_count":12,"last_write_at":"2026-08-29T10:00:00Z"}],"total":1,"limit":1,"offset":0}`,
		sentinelID, sentinelName, sentinelMission)
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap := mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	blob, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, sentinel := range []string{sentinelID, sentinelName, sentinelMission} {
		if strings.Contains(string(blob), sentinel) {
			t.Errorf("snapshot JSON leaks %q:\n%s", sentinel, blob)
		}
	}
	if snap.Memory.Facts != 12 || snap.Memory.Banks != 1 {
		t.Errorf("aggregates = %+v, want banks 1 facts 12", snap.Memory)
	}
}

// TestTestProbesFourEndpointsWithoutMutatingState: Test Connection asks
// each configured surface once — with a one-row bank probe — and leaves
// the adapter's ephemeral state untouched.
func TestTestProbesFourEndpointsWithoutMutatingState(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, &memStore{secret: "hs-key"}, clk)

	out := a.Test(context.Background())
	if !out.OK() {
		t.Fatalf("Test = %+v, want all probes OK", out)
	}
	if out.BanksTotal != 3 {
		t.Errorf("BanksTotal = %d, want the fixture total 3", out.BanksTotal)
	}

	want := []string{
		"/health",
		"/health/live",
		"/metrics",
		"/v1/default/banks?limit=1&offset=0",
	}
	got := srv.uris()
	if len(got) != len(want) {
		t.Fatalf("Test requests = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Test request %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, h := range srv.auths() {
		if h != "Bearer hs-key" {
			t.Errorf("Authorization = %q, want the stored key", h)
		}
	}
	if a.slow != nil || a.baseline != nil {
		t.Errorf("Test must not touch the ephemeral state (slow=%v baseline=%v)", a.slow, a.baseline)
	}

	// A Refresh afterwards still runs the full first-refresh sequence
	// (4 Test + 4 Refresh requests).
	mustRefresh(t, a)
	if n := len(srv.uris()); n != 2*len(want) {
		t.Errorf("requests after Test+Refresh = %d, want %d", n, 2*len(want))
	}
}

func TestTestReportsPerSurfaceFailures(t *testing.T) {
	srv := newHindsightServer()
	srv.metricsCode = http.StatusNotFound
	srv.banksBody = banksPage(-5)
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	out := newAdapter(server.URL, nil, &fakeClock{}).Test(context.Background())
	if out.OK() {
		t.Fatal("Test must not be OK with failing probes")
	}
	if out.HealthErr != nil || out.LiveErr != nil {
		t.Errorf("health probes should pass, got %v / %v", out.HealthErr, out.LiveErr)
	}
	if out.MetricsErr == nil {
		t.Error("MetricsErr = nil, want the 404 failure")
	}
	if out.BanksErr == nil {
		t.Error("BanksErr = nil, want the invalid bank list failure")
	}
}

// A base URL with a trailing slash is valid per the settings validator and
// must be normalized before joining paths: "http://host/" must request
// "http://host/health", not "http://host//health" (spec §4).
func TestNewNormalizesTrailingSlashBaseURL(t *testing.T) {
	srv := newHindsightServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	a := newAdapter(server.URL+"/", nil, &fakeClock{})
	if a.baseURL != server.URL {
		t.Fatalf("normalized base = %q, want %q", a.baseURL, server.URL)
	}
	mustRefresh(t, a)
	for _, uri := range srv.uris() {
		if strings.Contains(uri, "//") {
			t.Errorf("request %q contains a doubled slash", uri)
		}
	}
	if got := srv.uris()[0]; got != "/health" {
		t.Errorf("first request = %q, want /health", got)
	}
}

// A 200 /metrics body that is not valid exposition fails the refresh like
// any other broken fetch — and before the slow requests run, so neither
// the counter baseline nor the slow cache commits (the next successful
// interval must derive rates against the last good sample, not over a
// broken scrape).
func TestRefreshMalformedMetricsFailsWithoutCommitting(t *testing.T) {
	srv := newHindsightServer()
	srv.metricsBody = "<html>502 Bad Gateway</html>"
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{}
	a := newAdapter(server.URL, nil, clk)
	_, err := a.Refresh(context.Background())
	requireSourceError(t, err, source.CatInvalidResponse)

	for _, uri := range srv.uris() {
		if uri == "/health/live" || strings.HasPrefix(uri, "/v1/default/banks") {
			t.Errorf("slow request %q ran after the metrics parse failed", uri)
		}
	}
	if a.baseline != nil || a.slow != nil {
		t.Errorf("state committed on a malformed scrape (baseline=%v slow=%v)", a.baseline, a.slow)
	}

	// The connection test reports the same failure on the metrics surface.
	out := a.Test(context.Background())
	if out.MetricsErr == nil {
		t.Error("Test.MetricsErr = nil, want the malformed-exposition failure")
	}
}

// A broken liveness endpoint — e.g. a proxy routing /health/live to the
// web UI, which answers with HTML — must not fail the refresh: liveness
// carries only version and uptime, so the refresh degrades to a snapshot
// without them while health, metrics and the bank aggregates still land.
func TestRefreshToleratesBrokenLiveness(t *testing.T) {
	srv := newHindsightServer()
	srv.liveCode = http.StatusOK
	srv.liveBody = "<!doctype html><html><body>control plane</body></html>"
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, nil, clk)
	snap := mustRefresh(t, a)

	if snap.Service.Version != "" || snap.Service.Uptime != 0 {
		t.Errorf("version/uptime = %q/%v, want absent on a broken liveness fetch", snap.Service.Version, snap.Service.Uptime)
	}
	if snap.Service.Status != "healthy" {
		t.Errorf("health block = %+v, want the /health data", snap.Service)
	}
	if snap.Memory == nil || snap.Memory.Banks != 3 {
		t.Errorf("Memory = %+v, want the bank aggregate despite the broken liveness fetch", snap.Memory)
	}

	// The slow cache still commits, so the steady-state budget is unchanged.
	clk.advance(5 * time.Second)
	snap = mustRefresh(t, a)
	if got := srv.uris()[4:]; len(got) != 2 {
		t.Errorf("fast-poll requests = %v, want only /health and /metrics", got)
	}
	if snap.Service.Version != "" {
		t.Errorf("Version = %q, want the cached absence", snap.Service.Version)
	}
}

// OTel-based deployments spell the token families with an extra _tokens
// segment; both spellings must pass the family filter and feed the same
// aggregate counters.
func TestOTelTokenFamilySpellingsAggregate(t *testing.T) {
	body := `hindsight_llm_tokens_input_tokens_total{model="m"} 1200
hindsight_llm_tokens_output_tokens_total{model="m"} 340
hindsight_llm_tokens_input_total 10
hindsight_llm_tokens_output_total 4
`
	samples, err := parseSelectedMetrics(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	counters, _, _ := aggregateMetrics(samples)
	if counters.InputTokensTotal == nil || *counters.InputTokensTotal != 1210 {
		t.Errorf("InputTokensTotal = %v, want 1210 from both spellings", counters.InputTokensTotal)
	}
	if counters.OutputTokensTotal == nil || *counters.OutputTokensTotal != 344 {
		t.Errorf("OutputTokensTotal = %v, want 344 from both spellings", counters.OutputTokensTotal)
	}
}

// A bank list without pagination metadata is the complete list: the count
// is the returned row count and the walk must not advance the offset —
// servers that ignore offset would hand back the same rows and the facts
// would be counted twice.
func TestRefreshUnpaginatedBankListIsComplete(t *testing.T) {
	srv := newHindsightServer()
	srv.banksBody = banksPageNoTotal(bankItem(649, "a"), bankItem(907, "b"))
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	clk := &fakeClock{t: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}
	a := newAdapter(server.URL, nil, clk)
	snap := mustRefresh(t, a)

	if snap.Memory == nil || snap.Memory.Banks != 2 || snap.Memory.Facts != 649+907 {
		t.Fatalf("Memory = %+v, want Banks 2 Facts %d", snap.Memory, 649+907)
	}

	// Exactly one bank request: there is nothing to walk.
	var bankReqs int
	for _, uri := range srv.uris() {
		if strings.HasPrefix(uri, "/v1/default/banks") {
			bankReqs++
		}
	}
	if bankReqs != 1 {
		t.Errorf("bank requests = %d, want the single unpaginated page", bankReqs)
	}

	// The count survives the TTL the same way the facts do.
	clk.advance(61 * time.Second)
	snap = mustRefresh(t, a)
	if snap.Memory.Banks != 2 || snap.Memory.Facts != 649+907 {
		t.Errorf("Memory after TTL = %+v, want the same complete list", snap.Memory)
	}
}

// The Settings test reports the row count for a deployment without
// pagination metadata instead of a misleading 0.
func TestTestReportsUnpaginatedBankTotal(t *testing.T) {
	srv := newHindsightServer()
	srv.banksBody = banksPageNoTotal(bankItem(1, "a"), bankItem(2, "b"), bankItem(3, "c"))
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	out := newAdapter(server.URL, nil, &fakeClock{}).Test(context.Background())
	if out.BanksErr != nil {
		t.Fatalf("BanksErr = %v, want nil", out.BanksErr)
	}
	if out.BanksTotal != 3 {
		t.Errorf("BanksTotal = %d, want the 3 returned rows", out.BanksTotal)
	}
}

// An unpaginated empty list is a real empty deployment: zeros land, not
// an absent Memory block.
func TestRefreshUnpaginatedEmptyBankListLandsZeroMemory(t *testing.T) {
	srv := newHindsightServer()
	srv.banksBody = banksPageNoTotal()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap := mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	if snap.Memory == nil || snap.Memory.Banks != 0 || snap.Memory.Facts != 0 {
		t.Fatalf("Memory = %+v, want landed zeros", snap.Memory)
	}
}

// A valid empty deployment (total 0) must land a populated Memory block —
// nil memory means "slow data not fetched yet", not "no banks" — so the UI
// can show the real zeros instead of waiting forever.
func TestRefreshEmptyDeploymentLandsZeroMemory(t *testing.T) {
	srv := newHindsightServer()
	srv.banksBody = banksPage(0)
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap := mustRefresh(t, newAdapter(server.URL, nil, &fakeClock{}))
	if snap.Memory == nil {
		t.Fatal("Memory = nil, want a landed aggregate of an empty deployment")
	}
	if snap.Memory.Banks != 0 || snap.Memory.Facts != 0 {
		t.Errorf("Memory = %+v, want Banks 0 Facts 0", snap.Memory)
	}
}

// Each page's fact sum fits int64 while their deployment-wide total may
// not: the cross-page accumulation carries the same overflow guard as the
// within-page one.
func TestRefreshCrossPageFactOverflowIsInvalid(t *testing.T) {
	srv := newHindsightServer()
	srv.banksPages = map[string]string{
		"0": banksPage(4,
			fmt.Sprintf(`{"fact_count":%d,"last_write_at":"2026-08-29T10:00:00Z"}`, int64(math.MaxInt64)),
			bankItem(0, "b")),
		"2": banksPage(4, bankItem(1, "c"), bankItem(0, "d")),
	}
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	a := newAdapter(server.URL, nil, &fakeClock{})
	a.pageSize = 2
	_, err := a.Refresh(context.Background())
	requireSourceError(t, err, source.CatInvalidResponse)
}
