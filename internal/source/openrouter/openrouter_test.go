package openrouter

import (
	"context"
	"errors"
	"fmt"
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

const (
	testAPIAccount  = credentials.AccountID("source/test-instance/api-key")
	testMgmtAccount = credentials.AccountID("source/test-instance/management-key")

	testAPIToken  = "sk-or-v1-test-api-token"
	testMgmtToken = "sk-or-mgmt-test-management-token"
)

// memStore is a fake credential store keyed per account — OpenRouter's two
// slots must hold independent values. It never touches a keychain.
type memStore struct {
	mu       sync.Mutex
	secrets  map[credentials.AccountID]string
	stateErr *credentials.StateError
}

func newMemStore() *memStore {
	return &memStore{secrets: map[credentials.AccountID]string{}}
}

func (s *memStore) with(account credentials.AccountID, secret string) *memStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[account] = secret

	return s
}

func (s *memStore) withState(err *credentials.StateError) *memStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateErr = err

	return s
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
	if secret, ok := s.secrets[id]; ok && secret != "" {
		return secret, nil
	}

	return "", credentials.ErrNotFound
}

func (s *memStore) Set(ctx context.Context, id credentials.AccountID, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[id] = secret

	return nil
}

func (s *memStore) Delete(ctx context.Context, id credentials.AccountID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets, id)

	return nil
}

// apiResponse / creditsResponse are the canned payloads per endpoint path.
type serverResponses struct {
	key     string
	credits string
}

// capServer serves both capability paths from one httptest server, counts
// requests PER PATH and captures each Authorization header, so tests can
// prove gating: an endpoint with no credential must see zero requests.
type capServer struct {
	mu        sync.Mutex
	responses serverResponses
	count     map[string]int
	auth      map[string][]string // path -> headers in arrival order
	status    map[string]int
}

func newCapServer(responses serverResponses) *capServer {
	return &capServer{
		responses: responses,
		count:     map[string]int{},
		auth:      map[string][]string{},
		status:    map[string]int{},
	}
}

func (s *capServer) handler() http.Handler {
	mux := http.NewServeMux()
	for _, ep := range []struct{ path, key string }{
		{"/api/v1/key", "key"},
		{"/api/v1/credits", "credits"},
	} {
		key := ep.key
		path := ep.path
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			s.count[path]++
			s.auth[path] = append(s.auth[path], r.Header.Get("Authorization"))
			code := s.status[key]
			s.mu.Unlock()
			if code != 0 {
				w.WriteHeader(code)

				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(s.responsesFor(key)))
		})
	}

	return mux
}

func (s *capServer) responsesFor(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "key" {
		return s.responses.key
	}

	return s.responses.credits
}

func (s *capServer) setStatus(key string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[key] = code
}

func (s *capServer) requests(t *testing.T, path string) int {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.count[path]
}

func (s *capServer) authHeaders(t *testing.T, path string) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.auth[path]...)
}

// start runs the server and returns a test adapter aimed at it.
func (s *capServer) start(t *testing.T, creds credentials.Store) (*Adapter, func()) {
	t.Helper()
	hs := httptest.NewServer(s.handler())
	t.Cleanup(hs.Close)
	a := &Adapter{
		apiKeyAccount:   testAPIAccount,
		mgmtKeyAccount:  testMgmtAccount,
		creds:           creds,
		client:          hs.Client(),
		log:             testLogger(),
		keyEndpoint:     hs.URL + "/api/v1/key",
		creditsEndpoint: hs.URL + "/api/v1/credits",
	}

	return a, hs.Close
}

func asSourceError(t *testing.T, err error) *source.Error {
	t.Helper()
	var serr *source.Error
	if !errors.As(err, &serr) {
		t.Fatalf("expected *source.Error, got %T: %v", err, err)
	}

	return serr
}

// minimalKeyJSON is the smallest valid documented current-key payload.
func minimalKeyJSON() string {
	return `{"data":{"usage":41.08,"usage_daily":0.42,"usage_weekly":3.81,"usage_monthly":12.47}}`
}

// minimalCreditsJSON is the smallest valid documented credits payload.
func minimalCreditsJSON() string {
	return `{"data":{"total_credits":100.5,"total_usage":25.75}}`
}

// fullAdapter is API+Management configured against the shared server.
func fullSetup(t *testing.T, responses serverResponses) (*capServer, *Adapter) {
	t.Helper()
	srv := newCapServer(responses)
	a, _ := srv.start(t, newMemStore().
		with(testAPIAccount, testAPIToken).
		with(testMgmtAccount, testMgmtToken))

	return srv, a
}

// --- Capability gating (the hard acceptance conditions) ---

func TestRefreshAPIKeyOnlyNeverCallsCredits(t *testing.T) {
	srv := newCapServer(serverResponses{key: minimalKeyJSON()})
	a, _ := srv.start(t, newMemStore().with(testAPIAccount, testAPIToken))

	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	os, ok := snap.(domain.OpenRouterSnapshot)
	if !ok {
		t.Fatalf("snapshot type %T, want domain.OpenRouterSnapshot", snap)
	}
	if os.Key == nil {
		t.Fatal("key capability missing from snapshot")
	}
	if os.Account != nil {
		t.Fatal("account capability must be nil without a Management key")
	}
	if got := srv.requests(t, "/api/v1/key"); got != 1 {
		t.Errorf("/key requests = %d, want exactly 1", got)
	}
	if got := srv.requests(t, "/api/v1/credits"); got != 0 {
		t.Errorf("/credits requests = %d, want ZERO without a Management key", got)
	}
	if got := srv.authHeaders(t, "/api/v1/key"); len(got) != 1 || got[0] != "Bearer "+testAPIToken {
		t.Errorf("/key Authorization = %v, want only the API-key bearer token", got)
	}
}

func TestRefreshManagementKeyOnlyNeverCallsKey(t *testing.T) {
	srv := newCapServer(serverResponses{credits: minimalCreditsJSON()})
	a, _ := srv.start(t, newMemStore().with(testMgmtAccount, testMgmtToken))

	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	os := snap.(domain.OpenRouterSnapshot)
	if os.Key != nil {
		t.Fatal("key capability must be nil without an API key")
	}
	if os.Account == nil {
		t.Fatal("account capability missing from snapshot")
	}
	if got := srv.requests(t, "/api/v1/key"); got != 0 {
		t.Errorf("/key requests = %d, want ZERO without an API key", got)
	}
	if got := srv.requests(t, "/api/v1/credits"); got != 1 {
		t.Errorf("/credits requests = %d, want exactly 1", got)
	}
	if got := srv.authHeaders(t, "/api/v1/credits"); len(got) != 1 || got[0] != "Bearer "+testMgmtToken {
		t.Errorf("/credits Authorization = %v, want only the Management bearer token", got)
	}
}

func TestRefreshWithBothCredentials(t *testing.T) {
	srv, a := fullSetup(t, serverResponses{key: minimalKeyJSON(), credits: minimalCreditsJSON()})

	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	os := snap.(domain.OpenRouterSnapshot)
	if os.Key == nil || os.Account == nil {
		t.Fatalf("combined snapshot must hold both capabilities: %+v", os)
	}
	if got := srv.requests(t, "/api/v1/key"); got != 1 {
		t.Errorf("/key requests = %d, want 1", got)
	}
	if got := srv.requests(t, "/api/v1/credits"); got != 1 {
		t.Errorf("/credits requests = %d, want 1", got)
	}
	apiAuth := srv.authHeaders(t, "/api/v1/key")
	mgmtAuth := srv.authHeaders(t, "/api/v1/credits")
	if len(apiAuth) != 1 || apiAuth[0] != "Bearer "+testAPIToken {
		t.Errorf("/key token = %v, want the API key only", apiAuth)
	}
	if len(mgmtAuth) != 1 || mgmtAuth[0] != "Bearer "+testMgmtToken {
		t.Errorf("/credits token = %v, want the Management key only", mgmtAuth)
	}
	// No substitution either way.
	if strings.Contains(apiAuth[0], testMgmtToken) || strings.Contains(mgmtAuth[0], testAPIToken) {
		t.Error("credential tokens must never cross slots")
	}
	if os.SnapshotKind() != domain.KindOpenRouter {
		t.Errorf("kind = %q, want %q", os.SnapshotKind(), domain.KindOpenRouter)
	}
}

func TestRefreshWithNeitherCredentialMakesZeroRequests(t *testing.T) {
	srv := newCapServer(serverResponses{key: minimalKeyJSON(), credits: minimalCreditsJSON()})
	a, _ := srv.start(t, newMemStore())

	snap, err := a.Refresh(context.Background())
	if snap != nil {
		t.Fatalf("no-credential refresh returned a snapshot %+v", snap)
	}
	serr := asSourceError(t, err)
	if serr.Category != source.CatCredentials {
		t.Errorf("category = %q, want credentials", serr.Category)
	}
	if msg := serr.UserMessage(); msg == "" || strings.Contains(strings.ToLower(msg), "sk-or") {
		t.Errorf("user message %q must be local and sanitized", msg)
	}
	if got := srv.requests(t, "/api/v1/key") + srv.requests(t, "/api/v1/credits"); got != 0 {
		t.Errorf("total requests = %d, want ZERO when no credential exists", got)
	}
}

func TestFetchersReportAbsentCapabilityAsNilNotError(t *testing.T) {
	srv := newCapServer(serverResponses{key: minimalKeyJSON(), credits: minimalCreditsJSON()})
	a, _ := srv.start(t, newMemStore())
	ctx := context.Background()

	key, err := a.FetchKeyUsage(ctx)
	if err != nil || key != nil {
		t.Errorf("absent api-key slot = (%v, %v), want (nil, nil)", key, err)
	}
	account, err := a.FetchAccountCredits(ctx)
	if err != nil || account != nil {
		t.Errorf("absent management-key slot = (%v, %v), want (nil, nil)", account, err)
	}
	if got := srv.requests(t, "/api/v1/key") + srv.requests(t, "/api/v1/credits"); got != 0 {
		t.Errorf("capability fetches without credentials issued %d requests, want 0", got)
	}
}

// --- HTTP failure handling ---

func TestAuthFailuresAreCategorizedAndSanitized(t *testing.T) {
	codes := []int{http.StatusUnauthorized, http.StatusForbidden}
	for _, code := range codes {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := newCapServer(serverResponses{key: `{"error":"some provider message"}`})
			srv.setStatus("key", code)
			a, _ := srv.start(t, newMemStore().with(testAPIAccount, testAPIToken))

			_, err := a.Refresh(context.Background())
			serr := asSourceError(t, err)
			if serr.Category != source.CatAuth {
				t.Errorf("category = %q, want auth", serr.Category)
			}
			msg := serr.UserMessage()
			if msg != "Authentication failed" {
				t.Errorf("user message = %q, want the fixed sanitized text", msg)
			}
			if strings.Contains(msg+serr.Detail, testAPIToken) || strings.Contains(msg+serr.Detail, "provider message") {
				t.Error("token or provider payload leaked into the error")
			}
		})
	}
}

func TestServerErrorCategory(t *testing.T) {
	srv := newCapServer(serverResponses{credits: `{}`, key: `{}`})
	srv.setStatus("credits", http.StatusInternalServerError)
	a, _ := srv.start(t, newMemStore().with(testMgmtAccount, testMgmtToken))

	_, err := a.FetchAccountCredits(context.Background())
	if serr := asSourceError(t, err); serr.Category != source.CatProvider {
		t.Errorf("category = %v, want provider", serr.Category)
	}
}

func TestMalformedJSONIsInvalidResponse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses serverResponses
		fetch     func(*Adapter, context.Context) (any, error)
	}{
		{
			name:      "key",
			responses: serverResponses{key: "{not json"},
			fetch:     func(a *Adapter, ctx context.Context) (any, error) { return a.FetchKeyUsage(ctx) },
		},
		{
			name:      "credits",
			responses: serverResponses{credits: "{not json"},
			fetch:     func(a *Adapter, ctx context.Context) (any, error) { return a.FetchAccountCredits(ctx) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore().
				with(testAPIAccount, testAPIToken).
				with(testMgmtAccount, testMgmtToken)
			srv := newCapServer(tc.responses)
			a, _ := srv.start(t, store)

			if _, err := tc.fetch(a, context.Background()); err == nil {
				t.Fatal("malformed JSON must fail")
			} else if serr := asSourceError(t, err); serr.Category != source.CatInvalidResponse {
				t.Errorf("category = %v, want invalid-response", serr.Category)
			}
		})
	}
}

func TestTimeoutClassifiedAndSanitized(t *testing.T) {
	block := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer hs.Close()
	defer close(block)

	a := &Adapter{
		apiKeyAccount:   testAPIAccount,
		mgmtKeyAccount:  testMgmtAccount,
		creds:           newMemStore().with(testAPIAccount, testAPIToken),
		client:          hs.Client(),
		log:             testLogger(),
		keyEndpoint:     hs.URL,
		creditsEndpoint: CreditsEndpoint,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := a.FetchKeyUsage(ctx)
	serr := asSourceError(t, err)
	if serr.Category != source.CatTimeout {
		t.Errorf("category = %v, want timeout", serr.Category)
	}
}

// --- DTO defensiveness: required fields, wrong key types, optionals ---

func TestMissingRequiredUsageFieldsRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing total usage", `{"data":{"usage_daily":1,"usage_weekly":1,"usage_monthly":1}}`},
		{"missing daily", `{"data":{"usage":1,"usage_weekly":1,"usage_monthly":1}}`},
		{"null monthly", `{"data":{"usage":1,"usage_daily":1,"usage_weekly":1,"usage_monthly":null}}`},
		{"all absent", `{"data":{"label":"k"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newCapServer(serverResponses{key: c.body})
			a, _ := srv.start(t, newMemStore().with(testAPIAccount, testAPIToken))

			key, err := a.FetchKeyUsage(context.Background())
			if err == nil {
				t.Fatalf("missing required usage values must fail, got %+v", key)
			}
			if serr := asSourceError(t, err); serr.Category != source.CatInvalidResponse {
				t.Errorf("category = %v, want invalid-response", serr.Category)
			}
		})
	}
}

func TestMissingRequiredCreditTotalsRejected(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing total_credits", `{"data":{"total_usage":25}}`},
		{"null total_usage", `{"data":{"total_credits":100,"total_usage":null}}`},
		{"empty data object", `{"data":{}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newCapServer(serverResponses{credits: c.body})
			a, _ := srv.start(t, newMemStore().with(testMgmtAccount, testMgmtToken))

			acct, err := a.FetchAccountCredits(context.Background())
			if err == nil {
				t.Fatalf("missing required totals must fail, got %+v", acct)
			}
			if serr := asSourceError(t, err); serr.Category != source.CatInvalidResponse {
				t.Errorf("category = %v, want invalid-response", serr.Category)
			}
		})
	}
}

func TestWrongCredentialTypeInAPISlotRejected(t *testing.T) {
	cases := []struct {
		name string
		flag string
		want string
	}{
		{"management key", `"is_management_key":true`, "Management"},
		{"provisioning key", `"is_provisioning_key":true`, "provisioning"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"data":{%s,"usage":1,"usage_daily":1,"usage_weekly":1,"usage_monthly":1}}`, c.flag)
			srv := newCapServer(serverResponses{key: body})
			a, _ := srv.start(t, newMemStore().with(testAPIAccount, testAPIToken))

			key, err := a.FetchKeyUsage(context.Background())
			if err == nil {
				t.Fatalf("wrong key type must be rejected, got %+v", key)
			}
			serr := asSourceError(t, err)
			if serr.Category != source.CatCredentials {
				t.Errorf("category = %v, want credentials", serr.Category)
			}
			if !strings.Contains(serr.Detail, c.want) {
				t.Errorf("detail %q should identify the wrong type", serr.Detail)
			}
			if strings.Contains(serr.Detail, testAPIToken) {
				t.Error("token material leaked into the detail")
			}
			// Refusal happens on the single already-made request; nothing else.
			if got := srv.requests(t, "/api/v1/key"); got != 1 {
				t.Errorf("/key requests = %d, probing extra requests forbidden", got)
			}
			if got := srv.requests(t, "/api/v1/credits"); got != 0 {
				t.Error("credential classification must not probe other endpoints")
			}
		})
	}
}

func TestOptionalFieldsParseDefensively(t *testing.T) {
	body := `{"data":{
		"usage":41.08,"usage_daily":0.42,"usage_weekly":3.81,"usage_monthly":12.47,
		"byok_usage":17.38,"byok_usage_daily":1.1,"byok_usage_weekly":2.2,"byok_usage_monthly":3.3,
		"include_byok_in_limit":true,
		"limit":25.00,"limit_remaining":12.47,"limit_reset":"monthly",
		"rate_limit":{"requests":200},"label":"kept-out","expires_at":null
	}}`
	srv := newCapServer(serverResponses{key: body})
	a, _ := srv.start(t, newMemStore().with(testAPIAccount, testAPIToken))

	key, err := a.FetchKeyUsage(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if key.UsageTotal != 41.08 || key.UsageDaily != 0.42 || key.UsageWeekly != 3.81 || key.UsageMonthly != 12.47 {
		t.Errorf("usage mismatch: %+v", key)
	}
	if key.BYOKTotal != 17.38 || key.BYOKDaily != 1.1 || key.BYOKWeekly != 2.2 || key.BYOKMonthly != 3.3 {
		t.Errorf("BYOK retention mismatch: %+v", key.BYOKTotal)
	}
	if !key.IncludeBYOKInLimit {
		t.Error("include_byok_in_limit lost")
	}
	if key.Limit == nil {
		t.Fatal("usable limit dropped")
	}
	if key.Limit.Amount != 25 || key.Limit.Remaining != 12.47 || key.Limit.Cadence != domain.CadenceMonthly {
		t.Errorf("limit mismatch: %+v", key.Limit)
	}
}

// The deprecated rate_limit block and undocumented fields are ignored, not
// errors; unknown JSON keys simply do not survive decoding.
func TestRateLimitIgnoredWithoutError(t *testing.T) {
	body := `{"data":{"usage":1,"usage_daily":0,"usage_weekly":0,"usage_monthly":0,"rate_limit":{"is_free_tier":false}}}`
	srv := newCapServer(serverResponses{key: body})
	a, _ := srv.start(t, newMemStore().with(testAPIAccount, testAPIToken))

	key, err := a.FetchKeyUsage(context.Background())
	if err != nil {
		t.Fatalf("deprecated rate_limit must be ignored: %v", err)
	}
	if key.UsageDaily != 0 {
		t.Errorf("explicit zero usage lost: %+v", key)
	}
}

// --- Limit normalization ---

func TestLimitNormalization(t *testing.T) {
	usage := `"usage":10,"usage_daily":1,"usage_weekly":2,"usage_monthly":3`
	cases := []struct {
		name       string
		body       string
		wantLimit  bool
		wantAmount float64
		wantRemain float64
		wantCad    string
	}{
		{"valid daily", fmt.Sprintf(`{"data":{%s,"limit":5,"limit_remaining":4.9,"limit_reset":"daily"}}`, usage), true, 5, 4.9, domain.CadenceDaily},
		{"weekly cadence", fmt.Sprintf(`{"data":{%s,"limit":5,"limit_remaining":1,"limit_reset":"weekly"}}`, usage), true, 5, 1, domain.CadenceWeekly},
		{"unknown cadence keeps limit", fmt.Sprintf(`{"data":{%s,"limit":5,"limit_remaining":1,"limit_reset":"biweekly"}}`, usage), true, 5, 1, ""},
		{"null cadence keeps limit", fmt.Sprintf(`{"data":{%s,"limit":5,"limit_remaining":1,"limit_reset":null}}`, usage), true, 5, 1, ""},
		{"noise remaining clamps to zero", fmt.Sprintf(`{"data":{%s,"limit":5,"limit_remaining":-1e-15}}`, usage), true, 5, 0, ""},
		{"over-limit noise clamps", fmt.Sprintf(`{"data":{%s,"limit":5,"limit_remaining":5.0000000000001}}`, usage), true, 5, 5, ""},
		{"null limit drops graph", fmt.Sprintf(`{"data":{%s,"limit":null,"limit_remaining":1}}`, usage), false, 0, 0, ""},
		{"absent remaining drops graph", fmt.Sprintf(`{"data":{%s,"limit":5}}`, usage), false, 0, 0, ""},
		{"zero limit drops graph", fmt.Sprintf(`{"data":{%s,"limit":0,"limit_remaining":0}}`, usage), false, 0, 0, ""},
		{"negative limit drops graph", fmt.Sprintf(`{"data":{%s,"limit":-5,"limit_remaining":0}}`, usage), false, 0, 0, ""},
		{"materially negative remaining drops graph", fmt.Sprintf(`{"data":{%s,"limit":5,"limit_remaining":-1}}`, usage), false, 0, 0, ""},
		{"remaining above limit drops graph", fmt.Sprintf(`{"data":{%s,"limit":5,"limit_remaining":6}}`, usage), false, 0, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newCapServer(serverResponses{key: c.body})
			a, _ := srv.start(t, newMemStore().with(testAPIAccount, testAPIToken))

			key, err := a.FetchKeyUsage(context.Background())
			if err != nil {
				// Invalid-response-only cases are those whose REQUIRED fields
				// are intact; dropping a limit must never fail the refresh.
				t.Fatalf("unusable limit must degrade, not fail: %v", err)
			}
			if !c.wantLimit {
				if key.Limit != nil {
					t.Fatalf("limit = %+v, want nil", key.Limit)
				}
				// Usage survives any optional-field degradation.
				if key.UsageTotal != 10 {
					t.Errorf("usage lost while degrading the limit: %+v", key)
				}

				return
			}
			if key.Limit == nil {
				t.Fatal("usable limit was dropped")
			}
			if key.Limit.Amount != c.wantAmount || key.Limit.Remaining != c.wantRemain {
				t.Errorf("limit amounts = (%v, %v), want (%v, %v)",
					key.Limit.Amount, key.Limit.Remaining, c.wantAmount, c.wantRemain)
			}
			if key.Limit.Cadence != c.wantCad {
				t.Errorf("cadence = %q, want %q", key.Limit.Cadence, c.wantCad)
			}
		})
	}
}

// --- Account balance derivation ---

func TestBalanceDerivationAndNoiseClamp(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantBal float64
	}{
		{"derived", `{"data":{"total_credits":100.5,"total_usage":25.75}}`, 74.75},
		{"fully spent", `{"data":{"total_credits":25,"total_usage":25}}`, 0},
		{"negative noise clamps to zero", `{"data":{"total_credits":25.0,"total_usage":25.00000000000001}}`, 0},
		{"materially negative stays visible", `{"data":{"total_credits":10,"total_usage":42}}`, -32},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newCapServer(serverResponses{credits: c.body})
			a, _ := srv.start(t, newMemStore().with(testMgmtAccount, testMgmtToken))

			acct, err := a.FetchAccountCredits(context.Background())
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if acct.Balance != c.wantBal {
				t.Errorf("balance = %v, want %v", acct.Balance, c.wantBal)
			}
		})
	}
}

// --- Keychain failures ---

func TestKeychainFailureIsCategorized(t *testing.T) {
	state := &credentials.StateError{State: credentials.StateUnavailable, Message: "Secret Service died"}
	srv := newCapServer(serverResponses{key: minimalKeyJSON(), credits: minimalCreditsJSON()})
	a, _ := srv.start(t, newMemStore().with(testAPIAccount, testAPIToken).withState(state))

	_, err := a.Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatCredentials {
		t.Errorf("category = %v, want credentials", serr.Category)
	}
	if !strings.HasPrefix(serr.Detail, "Keychain unavailable") {
		t.Errorf("detail = %q, want the keychain prefix", serr.Detail)
	}
	if got := srv.requests(t, "/api/v1/key") + srv.requests(t, "/api/v1/credits"); got != 0 {
		t.Errorf("a broken keychain must stop before network, got %d requests", got)
	}
}

// An empty stored secret behaves like an absent credential rather than being
// sent as Bearer "".
func TestEmptyStoredSecretMeansAbsentCapability(t *testing.T) {
	srv := newCapServer(serverResponses{key: minimalKeyJSON(), credits: minimalCreditsJSON()})
	a, _ := srv.start(t, newMemStore().
		with(testAPIAccount, "").
		with(testMgmtAccount, testMgmtToken))

	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	os := snap.(domain.OpenRouterSnapshot)
	if os.Key != nil || os.Account == nil {
		t.Errorf("empty api-key slot must skip the key capability: %+v", os)
	}
	if got := srv.requests(t, "/api/v1/key"); got != 0 {
		t.Errorf("/key requests = %d, want 0 for an empty secret", got)
	}
}

func TestRequestTimeoutIsRemoteStandard(t *testing.T) {
	if got := (time.Duration)(requestTimeout); got == 0 || got > 30*time.Second {
		t.Errorf("request timeout = %v, expected the remote convention magnitude", got)
	}
	if got := (&Adapter{}).Kind(); got != domain.KindOpenRouter {
		t.Errorf("Kind() = %q", got)
	}
}
