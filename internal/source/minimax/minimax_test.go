package minimax

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
	mu       sync.Mutex
	secret   string
	notFound bool
	stateErr *credentials.StateError
}

func (s *memStore) Status(ctx context.Context) credentials.Status {
	return credentials.Status{State: credentials.StateAvailable}
}

func (s *memStore) Get(ctx context.Context, id credentials.AccountID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateErr != nil {
		return "", s.stateErr
	}
	if s.notFound || s.secret == "" {
		return "", credentials.ErrNotFound
	}
	return s.secret, nil
}

func (s *memStore) Set(ctx context.Context, id credentials.AccountID, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = secret
	return nil
}

func (s *memStore) Delete(ctx context.Context, id credentials.AccountID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secret = ""
	return nil
}

func withStore(secret string, notFound bool, stateErr *credentials.StateError) credentials.Store {
	return &memStore{secret: secret, notFound: notFound, stateErr: stateErr}
}

// planServer serves the token-plan payload and captures Authorization.
type planServer struct {
	body   string
	status int

	mu   sync.Mutex
	auth []string
}

func newPlanServer(body string) *planServer {
	return &planServer{body: body, status: http.StatusOK}
}

func (s *planServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()

		w.WriteHeader(s.status)
		w.Write([]byte(s.body))
	})
}

func (s *planServer) authHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

func adapterAt(url string, creds credentials.Store) *Adapter {
	return &Adapter{creds: creds, client: &http.Client{}, log: testLogger(), endpoint: url}
}

func asSourceError(t *testing.T, err error) *source.Error {
	t.Helper()
	var serr *source.Error
	if !errors.As(err, &serr) {
		t.Fatalf("expected *source.Error, got %T: %v", err, err)
	}
	return serr
}

func refresh(t *testing.T, a *Adapter) domain.QuotaSnapshot {
	t.Helper()
	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	q, ok := snap.(domain.QuotaSnapshot)
	if !ok {
		t.Fatalf("snapshot type = %T", snap)
	}
	if q.Source != domain.SourceMiniMax {
		t.Errorf("source = %q, want minimax", q.Source)
	}
	return q
}

func windowByID(q domain.QuotaSnapshot, id string) *domain.QuotaWindow {
	for i := range q.Windows {
		if q.Windows[i].ID == id {
			return &q.Windows[i]
		}
	}
	return nil
}

// Case 1: legacy count-based envelope. *_usage_count carries REMAINING
// (issue #99): 690/1000 → 69% remaining, never 31%.
func TestCase1LegacyCountsAreRemaining(t *testing.T) {
	body := `{
		"code": 0,
		"data": {
			"current_interval_total_count": 1000,
			"current_interval_usage_count": 690,
			"current_interval_reset_time": "2026-08-23T12:00:00Z",
			"current_weekly_total_count": 20000,
			"current_weekly_usage_count": 14800,
			"current_weekly_reset_time": "2026-08-24T00:00:00Z"
		}
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	if len(q.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(q.Windows))
	}
	if q.Windows[0].ID != "interval" || q.Windows[1].ID != "weekly" {
		t.Errorf("window order = %q, %q", q.Windows[0].ID, q.Windows[1].ID)
	}

	interval := windowByID(q, "interval")
	if interval.RemainingFraction < 0.689 || interval.RemainingFraction > 0.691 {
		t.Errorf("interval fraction = %v, want ~0.69 (usage_count IS remaining)", interval.RemainingFraction)
	}
	if interval.Remaining == nil || *interval.Remaining != 690 {
		t.Errorf("interval remaining = %v, want 690", interval.Remaining)
	}
	if interval.Limit == nil || *interval.Limit != 1000 {
		t.Errorf("interval limit = %v, want 1000", interval.Limit)
	}
	if interval.Unit != "requests" {
		t.Errorf("interval unit = %q", interval.Unit)
	}
	wantReset := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if interval.ResetAt == nil || !interval.ResetAt.Equal(wantReset) {
		t.Errorf("interval reset = %v, want %v", interval.ResetAt, wantReset)
	}

	weekly := windowByID(q, "weekly")
	if weekly.RemainingFraction < 0.739 || weekly.RemainingFraction > 0.741 {
		t.Errorf("weekly fraction = %v, want ~0.74", weekly.RemainingFraction)
	}
	wantWeeklyReset := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	if weekly.ResetAt == nil || !weekly.ResetAt.Equal(wantWeeklyReset) {
		t.Errorf("weekly reset = %v, want %v", weekly.ResetAt, wantWeeklyReset)
	}
}

// Case 2: current top-level shape, time-based plan with 0/0 counts.
// Windows come from the remaining percents; raw counts stay nil;
// remains_time (9 000 000 ms = 2.5 h) overrides end_time for interval;
// weekly_remains_time (403 200 000 ms ≈ 4.67 d) provides the weekly reset.
func TestCase2TimeBasedPlanUsesPercents(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0, "status_msg": "success"},
		"model_remains": [{
			"model_name": "MiniMax-M2.7-coding",
			"current_interval_remaining_percent": 42,
			"current_weekly_remaining_percent": 87,
			"remains_time": 9000000,
			"weekly_remains_time": 403200000,
			"start_time": 1770000000000,
			"end_time": 1770018000000
		}]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	interval := windowByID(q, "interval")
	if interval == nil {
		t.Fatal("interval window missing")
	}
	if interval.RemainingFraction != 0.42 {
		t.Errorf("interval fraction = %v, want 0.42", interval.RemainingFraction)
	}
	if interval.Remaining != nil || interval.Limit != nil {
		t.Errorf("time-based plan must not carry raw counts: %v/%v", interval.Remaining, interval.Limit)
	}
	// remains_time (9 000 000 ms) takes precedence over end_time.
	wantInterval := q.CollectedAt.Add(9000 * time.Second)
	if interval.ResetAt == nil || !interval.ResetAt.Equal(wantInterval) {
		t.Errorf("interval reset = %v, want %v (CollectedAt + remains_time)", interval.ResetAt, wantInterval)
	}

	weekly := windowByID(q, "weekly")
	if weekly == nil {
		t.Fatal("weekly window missing")
	}
	if weekly.RemainingFraction != 0.87 {
		t.Errorf("weekly fraction = %v, want 0.87", weekly.RemainingFraction)
	}
	// weekly_remains_time (403 200 000 ms ≈ 4.67 d) provides weekly reset.
	wantWeekly := q.CollectedAt.Add(403200 * time.Second)
	if weekly.ResetAt == nil || !weekly.ResetAt.Equal(wantWeekly) {
		t.Errorf("weekly reset = %v, want %v (CollectedAt + weekly_remains_time)", weekly.ResetAt, wantWeekly)
	}
}

// Case 3: counts and percents both present. total−usage (1000−690=310 →
// 31%) disagrees with the percent (69%), while usage/total agrees — the
// adapter must report 69%, never 31%.
func TestCase3UsageCountIsRemainingNotUsed(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0},
		"model_remains": [{
			"model_name": "MiniMax-M2.7-coding",
			"current_interval_remaining_percent": 69,
			"remains": 690,
			"total": 1000
		}]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	interval := windowByID(q, "interval")
	if interval == nil {
		t.Fatal("interval window missing")
	}
	if interval.RemainingFraction < 0.689 || interval.RemainingFraction > 0.691 {
		t.Errorf("fraction = %v, want ~0.69 (issue #99), not 0.31", interval.RemainingFraction)
	}
	if interval.Remaining == nil || *interval.Remaining != 690 {
		t.Errorf("raw remaining = %v, want 690 attached", interval.Remaining)
	}
}

// Case 3b: percents and counts genuinely disagree. The percent wins.
func TestCase3PercentWinsOverDisagreeingCounts(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0},
		"model_remains": [{
			"model_name": "MiniMax-M2.7-coding",
			"current_interval_remaining_percent": 80,
			"remains": 200,
			"total": 1000
		}]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	interval := windowByID(q, "interval")
	if interval == nil {
		t.Fatal("interval window missing")
	}
	if interval.RemainingFraction != 0.8 {
		t.Errorf("fraction = %v, want 0.8 (percent wins)", interval.RemainingFraction)
	}
}

// Case 4: duration fields provide ResetAt and override end_time.
// remains_time (3 600 000 ms = 1 h) wins over end_time for interval;
// weekly_remains_time (7 200 000 ms = 2 h) provides the weekly reset.
// Fractions and counts are unaffected by reset fields.
func TestCase4DurationFieldsBecomeResetAt(t *testing.T) {
	withDurations := `{
		"base_resp": {"status_code": 0},
		"model_remains": [{
			"model_name": "MiniMax-M2.7-coding",
			"current_interval_remaining_percent": 55,
			"current_weekly_remaining_percent": 66,
			"remains_time": 3600000,
			"weekly_remains_time": 7200000,
			"start_time": 1770000000000,
			"end_time": 1770018000000
		}]
	}`
	srv := httptest.NewServer(newPlanServer(withDurations).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	// Fractions come from percents, unchanged by reset fields.
	if interval := windowByID(q, "interval"); interval.RemainingFraction != 0.55 {
		t.Errorf("interval fraction = %v, want 0.55", interval.RemainingFraction)
	}
	if weekly := windowByID(q, "weekly"); weekly.RemainingFraction != 0.66 {
		t.Errorf("weekly fraction = %v, want 0.66", weekly.RemainingFraction)
	}

	// remains_time (1 h) overrides end_time.
	interval := windowByID(q, "interval")
	wantInterval := q.CollectedAt.Add(3600 * time.Second)
	if interval.ResetAt == nil || !interval.ResetAt.Equal(wantInterval) {
		t.Errorf("interval reset = %v, want %v (CollectedAt + 1 h)", interval.ResetAt, wantInterval)
	}

	// weekly_remains_time (2 h) provides weekly reset.
	weekly := windowByID(q, "weekly")
	wantWeekly := q.CollectedAt.Add(7200 * time.Second)
	if weekly.ResetAt == nil || !weekly.ResetAt.Equal(wantWeekly) {
		t.Errorf("weekly reset = %v, want %v (CollectedAt + 2 h)", weekly.ResetAt, wantWeekly)
	}
}

// Case 4b: malformed/negative/overflow duration fields are ignored;
// the verified end_time fallback is used instead.
func TestCase4bMalformedDurationFallsBack(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0},
		"model_remains": [{
			"model_name": "MiniMax-M2.7-coding",
			"current_interval_remaining_percent": 50,
			"remains_time": -1,
			"weekly_remains_time": 0,
			"end_time": 1770018000000
		}]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	// Quota usage is unaffected by bad reset data.
	interval := windowByID(q, "interval")
	if interval == nil || interval.RemainingFraction != 0.50 {
		t.Errorf("interval fraction = %v, want 0.50", interval)
	}

	// Negative/zero durations rejected; end_time fallback used.
	wantReset := time.UnixMilli(1770018000000)
	if interval.ResetAt == nil || !interval.ResetAt.Equal(wantReset) {
		t.Errorf("interval reset = %v, want end_time fallback %v", interval.ResetAt, wantReset)
	}
}

// Case 4c: overflow duration (larger than time.Duration can represent)
// is rejected; quota usage remains valid.
func TestCase4cOverflowDurationRejected(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0},
		"model_remains": [{
			"model_name": "MiniMax-M2.7-coding",
			"current_interval_remaining_percent": 80,
			"remains_time": 99999999999999
		}]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	interval := windowByID(q, "interval")
	if interval == nil {
		t.Fatal("interval window missing")
	}
	// Quota usage is valid despite overflow.
	if interval.RemainingFraction != 0.80 {
		t.Errorf("interval fraction = %v, want 0.80", interval)
	}
	// Overflow duration rejected; no reset time set.
	if interval.ResetAt != nil {
		t.Errorf("interval reset should be nil for overflow duration, got %v", interval.ResetAt)
	}
}

// Case 4d: both windows use the same captured observation time.
func TestCase4dBothWindowsUseSameObservedAt(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0},
		"model_remains": [{
			"model_name": "MiniMax-M2.7-coding",
			"current_interval_remaining_percent": 50,
			"current_weekly_remaining_percent": 60,
			"remains_time": 3600000,
			"weekly_remains_time": 7200000
		}]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	observed := q.CollectedAt
	interval := windowByID(q, "interval")
	weekly := windowByID(q, "weekly")

	wantInterval := observed.Add(3600 * time.Second)
	if interval.ResetAt == nil || !interval.ResetAt.Equal(wantInterval) {
		t.Errorf("interval reset = %v, want %v", interval.ResetAt, wantInterval)
	}
	wantWeekly := observed.Add(7200 * time.Second)
	if weekly.ResetAt == nil || !weekly.ResetAt.Equal(wantWeekly) {
		t.Errorf("weekly reset = %v, want %v", weekly.ResetAt, wantWeekly)
	}
}

// Case 5: several entries with the coding entry NOT first. Selection is
// by name semantics, never array position.
func TestCase5CodingEntrySelectedByName(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0},
		"model_remains": [
			{
				"model_name": "MiniMax-Text-01",
				"current_interval_remaining_percent": 11,
				"current_weekly_remaining_percent": 12
			},
			{
				"model_name": "MiniMax-M2.7-coding",
				"current_interval_remaining_percent": 42,
				"current_weekly_remaining_percent": 87
			}
		]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	interval := windowByID(q, "interval")
	if interval == nil || interval.RemainingFraction != 0.42 {
		t.Errorf("interval = %+v, want the coding entry's 42%%", interval)
	}
	if weekly := windowByID(q, "weekly"); weekly == nil || weekly.RemainingFraction != 0.87 {
		t.Errorf("weekly = %+v, want the coding entry's 87%%", weekly)
	}
}

// Real payloads expose the text/chat plan as the literal "general" bucket
// alongside unrelated lanes; it must be selected by name even when the
// entry names carry no coding/text hint (the failure this guards against
// read "cannot identify plan entry among 2 models").
func TestGeneralBucketSelectedByName(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0},
		"model_remains": [
			{
				"model_name": "MiniMax-M2.5",
				"current_interval_remaining_percent": 10,
				"current_weekly_remaining_percent": 20
			},
			{
				"model_name": "general",
				"current_interval_remaining_percent": 42,
				"current_weekly_remaining_percent": 87
			}
		]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	interval := windowByID(q, "interval")
	if interval == nil || interval.RemainingFraction != 0.42 {
		t.Errorf("interval = %+v, want the general entry's 42%%", interval)
	}
	if weekly := windowByID(q, "weekly"); weekly == nil || weekly.RemainingFraction != 0.87 {
		t.Errorf("weekly = %+v, want the general entry's 87%%", weekly)
	}
}

// The general bucket is the verified plan lane; it outranks the coding
// name heuristic when a payload offers both.
func TestGeneralBucketWinsOverCodingEntry(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0},
		"model_remains": [
			{
				"model_name": "MiniMax-M2.7-coding",
				"current_interval_remaining_percent": 10,
				"current_weekly_remaining_percent": 20
			},
			{
				"model_name": "general",
				"current_interval_remaining_percent": 55,
				"current_weekly_remaining_percent": 66
			}
		]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))

	interval := windowByID(q, "interval")
	if interval == nil || interval.RemainingFraction != 0.55 {
		t.Errorf("interval = %+v, want the general entry's 55%%", interval)
	}
	if weekly := windowByID(q, "weekly"); weekly == nil || weekly.RemainingFraction != 0.66 {
		t.Errorf("weekly = %+v, want the general entry's 66%%", weekly)
	}
}

func TestBearerTokenSent(t *testing.T) {
	fixture := newPlanServer(`{"base_resp":{"status_code":0},"model_remains":[{"model_name":"MiniMax-M2.7-coding","current_interval_remaining_percent":50}]}`)
	srv := httptest.NewServer(fixture.handler())
	defer srv.Close()

	a := adapterAt(srv.URL, withStore("secret-token", false, nil))
	if _, err := a.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	for i, h := range fixture.authHeaders() {
		if h != "Bearer secret-token" {
			t.Errorf("request %d Authorization = %q", i, h)
		}
	}
}

func TestBaseRespErrorOverHTTPOKIsAuth(t *testing.T) {
	srv := httptest.NewServer(newPlanServer(`{"base_resp":{"status_code":1004,"status_msg":"login fail: invalid api key"}}`).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("key-1", false, nil)).Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatAuth {
		t.Errorf("1004 category = %s, want auth", serr.Category)
	}
	if msg := serr.UserMessage(); msg == "login fail: invalid api key" {
		t.Error("status_msg must never surface in the user message")
	}
}

func TestLegacyErrorCodeNonZero(t *testing.T) {
	srv := httptest.NewServer(newPlanServer(`{"code":1004,"message":"login fail"}`).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("key-1", false, nil)).Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatAuth {
		t.Errorf("legacy code 1004 category = %s, want auth", serr.Category)
	}
}

func TestProviderCodeNonAuthIsInvalidResponse(t *testing.T) {
	srv := httptest.NewServer(newPlanServer(`{"base_resp":{"status_code":1027,"status_msg":"internal"}}`).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("key-1", false, nil)).Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatInvalidResponse {
		t.Errorf("code 1027 category = %s, want invalid-response", serr.Category)
	}
}

func TestUsageAboveTotalDroppedOtherKept(t *testing.T) {
	body := `{
		"code": 0,
		"data": {
			"current_interval_total_count": 100,
			"current_interval_usage_count": 150,
			"current_weekly_total_count": 1000,
			"current_weekly_usage_count": 700
		}
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))
	if windowByID(q, "interval") != nil {
		t.Error("nonsensical interval window (usage>total) must be dropped")
	}
	if weekly := windowByID(q, "weekly"); weekly == nil || weekly.RemainingFraction != 0.7 {
		t.Errorf("weekly = %+v, want kept at 0.7", weekly)
	}
}

func TestBothWindowsUnusableIsInvalidResponse(t *testing.T) {
	srv := httptest.NewServer(newPlanServer(`{"base_resp":{"status_code":0},"model_remains":[{"model_name":"MiniMax-M2.7-coding"}]}`).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("key-1", false, nil)).Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatInvalidResponse {
		t.Errorf("no usable windows category = %s, want invalid-response", serr.Category)
	}
}

func TestNoMatchingEntryAmongMany(t *testing.T) {
	body := `{
		"base_resp": {"status_code": 0},
		"model_remains": [
			{"model_name": "MiniMax-abab6.5s-chat", "current_interval_remaining_percent": 50},
			{"model_name": "MiniMax-abab5.5", "current_interval_remaining_percent": 60}
		]
	}`
	srv := httptest.NewServer(newPlanServer(body).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("key-1", false, nil)).Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatInvalidResponse {
		t.Errorf("no coding/text entry category = %s, want invalid-response", serr.Category)
	}
	// The detail must name the entries so an unmatched real payload is
	// diagnosable from the error alone.
	for _, want := range []string{"MiniMax-abab6.5s-chat", "MiniMax-abab5.5"} {
		if !strings.Contains(serr.Detail, want) {
			t.Errorf("detail %q does not list entry %q", serr.Detail, want)
		}
	}
}

func TestSingleEntryTakenWithoutNameMatch(t *testing.T) {
	srv := httptest.NewServer(newPlanServer(`{"base_resp":{"status_code":0},"model_remains":[{"model_name":"MiniMax-abab6.5s","current_interval_remaining_percent":30,"current_weekly_remaining_percent":40}]}`).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))
	if interval := windowByID(q, "interval"); interval == nil || interval.RemainingFraction != 0.3 {
		t.Errorf("single entry must be used directly: %+v", interval)
	}
}

func TestEmptyResponseIsInvalidResponse(t *testing.T) {
	srv := httptest.NewServer(newPlanServer(`{}`).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("key-1", false, nil)).Refresh(context.Background())
	if serr := asSourceError(t, err); serr.Category != source.CatInvalidResponse {
		t.Errorf("empty payload category = %s, want invalid-response", serr.Category)
	}
}

func TestRateLimited(t *testing.T) {
	srv := newPlanServer(`{"base_resp":{"status_code":0}}`)
	srv.status = http.StatusTooManyRequests
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	_, err := adapterAt(server.URL, withStore("key-1", false, nil)).Refresh(context.Background())
	if serr := asSourceError(t, err); serr.Category != source.CatRateLimited {
		t.Errorf("429 category = %s, want rate-limited", serr.Category)
	}
}

func TestMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(newPlanServer(`{not json`).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("key-1", false, nil)).Refresh(context.Background())
	if serr := asSourceError(t, err); serr.Category != source.CatInvalidResponse {
		t.Errorf("malformed category = %s, want invalid-response", serr.Category)
	}
}

func TestMissingKeyIsCredentialError(t *testing.T) {
	fixture := newPlanServer(`{}`)
	srv := httptest.NewServer(fixture.handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("", true, nil)).Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatCredentials {
		t.Errorf("missing key category = %s, want credentials", serr.Category)
	}
	if serr.UserMessage() != "API key not set" {
		t.Errorf("message = %q, want 'API key not set'", serr.UserMessage())
	}
	if got := len(fixture.authHeaders()); got != 0 {
		t.Errorf("no request may be made without a key, got %d", got)
	}
}

func TestKeychainLockedIsCredentialError(t *testing.T) {
	locked := &credentials.StateError{State: credentials.StateLocked, Message: "collection is locked"}
	a := &Adapter{creds: withStore("", false, locked), client: &http.Client{}, log: testLogger(), endpoint: "http://127.0.0.1:1"}

	_, err := a.Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatCredentials {
		t.Errorf("locked keychain category = %s, want credentials", serr.Category)
	}
}

func TestOutOfRangePercentDropsWindow(t *testing.T) {
	srv := httptest.NewServer(newPlanServer(`{"base_resp":{"status_code":0},"model_remains":[{"model_name":"MiniMax-M2.7-coding","current_interval_remaining_percent":142,"current_weekly_remaining_percent":50}]}`).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("key-1", false, nil)))
	if windowByID(q, "interval") != nil {
		t.Error("percent >100 must be rejected")
	}
	if windowByID(q, "weekly") == nil {
		t.Error("valid weekly window must survive")
	}
}
