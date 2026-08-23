package zai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/skobkin/llm-monitor/internal/credentials"
	"github.com/skobkin/llm-monitor/internal/domain"
	"github.com/skobkin/llm-monitor/internal/source"
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

// quotaServer serves the quota payload and captures request headers.
type quotaServer struct {
	body   string
	status int

	mu    sync.Mutex
	auth  []string
	langs []string
}

func newQuotaServer(body string) *quotaServer {
	return &quotaServer{body: body, status: http.StatusOK}
}

func (s *quotaServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.langs = append(s.langs, r.Header.Get("Accept-Language"))
		s.mu.Unlock()

		w.WriteHeader(s.status)
		w.Write([]byte(s.body))
	})
}

func (s *quotaServer) headers() (auth, langs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...), append([]string(nil), s.langs...)
}

func adapterAt(url string, creds credentials.Store) *Adapter {
	return New(url, creds, &http.Client{}, testLogger())
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
	if q.Source != domain.SourceZAI {
		t.Errorf("source = %q, want zai", q.Source)
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

func assertFraction(t *testing.T, name string, got, want float64) {
	t.Helper()
	diff := got - want
	if diff < -0.0001 || diff > 0.0001 {
		t.Errorf("%s fraction = %v, want %v", name, got, want)
	}
}

// Data-wrapped percent-scale payload: percentages are usage-oriented,
// so 42% used → 58% remaining.
func TestDataWrappedPercentScale(t *testing.T) {
	body := `{"code":0,"msg":"ok","data":{"limits":[
		{"type":"TOKENS_LIMIT","percentage":42,"currentValue":8400,"usage":20000,"usageDetails":{},"unit":"token","number":"plan id"},
		{"type":"TIME_LIMIT","percentage":88,"currentValue":88,"usage":100,"usageDetails":{},"unit":"calls","number":"plan id"}
	]}}`
	srv := httptest.NewServer(newQuotaServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("ztoken", false, nil)))

	if w := windowByID(q, "tokens-5h"); w == nil {
		t.Fatal("tokens-5h window missing")
	} else {
		assertFraction(t, "tokens remaining", w.RemainingFraction, 0.58)
		if w.Name != "Token usage · 5 hour" {
			t.Errorf("tokens name = %q", w.Name)
		}
		if w.ResetAt != nil || w.Remaining != nil || w.Limit != nil {
			t.Errorf("unverified fields must stay nil: %+v", w)
		}
	}
	if w := windowByID(q, "mcp-monthly"); w == nil {
		t.Fatal("mcp-monthly window missing")
	} else {
		assertFraction(t, "mcp remaining", w.RemainingFraction, 0.12)
		if w.Name != "MCP usage · 1 month" {
			t.Errorf("mcp name = %q", w.Name)
		}
	}
}

// Fraction-scale payload without counts: 0.42 used → 0.58 remaining via
// the per-value rule.
func TestFractionScaleWithoutCounts(t *testing.T) {
	body := `{"data":{"limits":[
		{"type":"TOKENS_LIMIT","percentage":0.42}
	]}}`
	srv := httptest.NewServer(newQuotaServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("ztoken", false, nil)))
	w := windowByID(q, "tokens-5h")
	if w == nil {
		t.Fatal("tokens-5h window missing")
	}
	assertFraction(t, "tokens remaining", w.RemainingFraction, 0.58)
}

// Cross-check disambiguation: percentage=1.0 with currentValue=1 and
// usage=100 reproduces ratio*100, so it is 1% used (percent scale) —
// NOT 100% used as the fraction reading would have it.
func TestScaleResolvedByCrossCheck(t *testing.T) {
	body := `{"limits":[
		{"type":"CREDIT_LIMIT","percentage":1.0,"currentValue":1,"usage":100}
	]}`
	srv := httptest.NewServer(newQuotaServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("ztoken", false, nil)))
	w := windowByID(q, "credits-5h")
	if w == nil {
		t.Fatal("credits-5h window missing")
	}
	assertFraction(t, "credits remaining", w.RemainingFraction, 0.99)
	if w.Name != "Credit usage · 5 hour" {
		t.Errorf("credits name = %q", w.Name)
	}
}

// A genuinely fraction-scale payload with counts must resolve as fraction:
// percentage 0.7 with currentValue 70 / usage 100.
func TestFractionScaleWithCounts(t *testing.T) {
	body := `{"data":{"limits":[
		{"type":"TOKENS_LIMIT","percentage":0.7,"currentValue":70,"usage":100}
	]}}`
	srv := httptest.NewServer(newQuotaServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("ztoken", false, nil)))
	w := windowByID(q, "tokens-5h")
	if w == nil {
		t.Fatal("tokens-5h window missing")
	}
	assertFraction(t, "tokens remaining", w.RemainingFraction, 0.30)
}

// All three entry types in one payload, mixed with an unknown type that
// must be skipped without breaking the rest.
func TestMixedTypesIncludingCredit(t *testing.T) {
	body := `{"data":{"limits":[
		{"type":"TOKENS_LIMIT","percentage":10,"currentValue":10,"usage":100},
		{"type":"TIME_LIMIT","percentage":20,"currentValue":20,"usage":100},
		{"type":"CREDIT_LIMIT","percentage":30,"currentValue":30,"usage":100},
		{"type":"SOMETHING_NEW","percentage":50}
	]}}`
	srv := httptest.NewServer(newQuotaServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("ztoken", false, nil)))
	if len(q.Windows) != 3 {
		t.Fatalf("windows = %d, want 3 (unknown type skipped)", len(q.Windows))
	}
	assertFraction(t, "tokens", windowByID(q, "tokens-5h").RemainingFraction, 0.90)
	assertFraction(t, "mcp", windowByID(q, "mcp-monthly").RemainingFraction, 0.80)
	assertFraction(t, "credits", windowByID(q, "credits-5h").RemainingFraction, 0.70)
}

func TestTopLevelLimitsFallback(t *testing.T) {
	body := `{"limits":[{"type":"TOKENS_LIMIT","percentage":0.5}]}`
	srv := httptest.NewServer(newQuotaServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("ztoken", false, nil)))
	w := windowByID(q, "tokens-5h")
	if w == nil {
		t.Fatal("top-level limits must be honored")
	}
	assertFraction(t, "tokens remaining", w.RemainingFraction, 0.50)
}

func TestNilPercentageOmitted(t *testing.T) {
	body := `{"limits":[
		{"type":"TOKENS_LIMIT"},
		{"type":"TIME_LIMIT","percentage":25}
	]}`
	srv := httptest.NewServer(newQuotaServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("ztoken", false, nil)))
	if windowByID(q, "tokens-5h") != nil {
		t.Error("nil-percentage entry must be omitted")
	}
	if windowByID(q, "mcp-monthly") == nil {
		t.Error("valid entry must survive")
	}
}

func TestOutOfRangePercentageOmitted(t *testing.T) {
	body := `{"limits":[
		{"type":"TOKENS_LIMIT","percentage":142},
		{"type":"TIME_LIMIT","percentage":-3}
	]}`
	srv := httptest.NewServer(newQuotaServer(body).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("ztoken", false, nil)).Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatInvalidResponse {
		t.Errorf("all entries dropped category = %s, want invalid-response", serr.Category)
	}
}

func TestEmptyLimitsIsInvalidResponse(t *testing.T) {
	srv := httptest.NewServer(newQuotaServer(`{"limits":[]}`).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("ztoken", false, nil)).Refresh(context.Background())
	if serr := asSourceError(t, err); serr.Category != source.CatInvalidResponse {
		t.Errorf("empty limits category = %s, want invalid-response", serr.Category)
	}
}

func TestRawAuthorizationHeaderWithoutBearer(t *testing.T) {
	fixture := newQuotaServer(`{"limits":[{"type":"TOKENS_LIMIT","percentage":0.5}]}`)
	srv := httptest.NewServer(fixture.handler())
	defer srv.Close()

	if _, err := adapterAt(srv.URL, withStore("raw-token-1", false, nil)).Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	auth, langs := fixture.headers()
	if len(auth) != 1 {
		t.Fatalf("requests = %d, want 1", len(auth))
	}
	if auth[0] != "raw-token-1" {
		t.Errorf("Authorization = %q, want the raw token with no Bearer prefix", auth[0])
	}
	if langs[0] != "en-US,en" {
		t.Errorf("Accept-Language = %q", langs[0])
	}
}

func TestAuthFailure(t *testing.T) {
	srv := newQuotaServer(`{"msg":"unauthorized"}`)
	srv.status = http.StatusUnauthorized
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	_, err := adapterAt(server.URL, withStore("ztoken", false, nil)).Refresh(context.Background())
	if serr := asSourceError(t, err); serr.Category != source.CatAuth {
		t.Errorf("401 category = %s, want auth", serr.Category)
	}
}

func TestMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(newQuotaServer(`{not json`).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("ztoken", false, nil)).Refresh(context.Background())
	if serr := asSourceError(t, err); serr.Category != source.CatInvalidResponse {
		t.Errorf("malformed category = %s, want invalid-response", serr.Category)
	}
}

func TestMissingKeyIsCredentialError(t *testing.T) {
	srv := httptest.NewServer(newQuotaServer(`{"limits":[]}`).handler())
	defer srv.Close()

	_, err := adapterAt(srv.URL, withStore("", true, nil)).Refresh(context.Background())
	serr := asSourceError(t, err)
	if serr.Category != source.CatCredentials {
		t.Errorf("missing key category = %s, want credentials", serr.Category)
	}
	if serr.UserMessage() != "API key not set" {
		t.Errorf("message = %q, want 'API key not set'", serr.UserMessage())
	}
}

func TestKeychainLockedIsCredentialError(t *testing.T) {
	locked := &credentials.StateError{State: credentials.StateLocked, Message: "collection is locked"}
	a := New("http://127.0.0.1:1", withStore("", false, locked), &http.Client{}, testLogger())

	_, err := a.Refresh(context.Background())
	if serr := asSourceError(t, err); serr.Category != source.CatCredentials {
		t.Errorf("locked keychain category = %s, want credentials", serr.Category)
	}
}

// String-typed currentValue/usage must still feed the cross-check.
func TestStringCountsFeedScaleResolution(t *testing.T) {
	body := `{"limits":[
		{"type":"TOKENS_LIMIT","percentage":1.0,"currentValue":"1","usage":"100"}
	]}`
	srv := httptest.NewServer(newQuotaServer(body).handler())
	defer srv.Close()

	q := refresh(t, adapterAt(srv.URL, withStore("ztoken", false, nil)))
	w := windowByID(q, "tokens-5h")
	if w == nil {
		t.Fatal("tokens-5h window missing")
	}
	assertFraction(t, "tokens remaining", w.RemainingFraction, 0.99)
}
