package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// secretToken is a fake token; tests assert it never leaks into errors.
const secretToken = "sk-ant-oat01-FAKE-test-token-not-real" //nolint:gosec // fake test fixture

var fixedNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func logger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func credsJSON(token string, expiresAt time.Time, plan string) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"REFRESH-SHOULD-NEVER-BE-READ","expiresAt":%d,"subscriptionType":%q},"mcpOAuth":{"x":{"accessToken":"MCP"}}}`,
		token, expiresAt.UnixMilli(), plan)
}

func writeCreds(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".credentials.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

type server struct {
	mu     sync.Mutex
	status int
	body   string
	auths  []string
	beta   string
	query  string
	srv    *httptest.Server
}

func newServer(t *testing.T, body string) *server {
	s := &server{status: 200, body: body}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		s.beta = r.Header.Get("anthropic-beta")
		s.query = r.URL.RawQuery
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.srv.Close)

	return s
}

func (s *server) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.auths)
}

func (s *server) set(status int, body string) {
	s.mu.Lock()
	s.status, s.body = status, body
	s.mu.Unlock()
}

func (s *server) adapter(path string) *Adapter {
	a := newAdapter(path, s.srv.URL+"?skip_spend=1", 5*time.Minute, &http.Client{}, logger())
	a.now = func() time.Time { return fixedNow }

	return a
}

const fullBody = `{
 "five_hour": {"utilization": 12.5, "resets_at": "2026-11-20T18:00:00Z"},
 "seven_day": {"utilization": 40, "resets_at": "2026-11-25T00:00:00Z"},
 "seven_day_opus": {"utilization": 70, "resets_at": "2026-11-25T00:00:00Z"},
 "seven_day_sonnet": null,
 "seven_day_oauth_apps": {"utilization": 99},
 "limits": [
  {"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":34,"resets_at":"2026-11-25T00:00:00Z"},
  {"kind":"weekly_scoped","scope":{"model":{"display_name":"opus"}},"percent":1},
  {"kind":"something_else","percent":5}
 ],
 "extra_usage": {"is_enabled": true, "monthly_limit": 5000, "used_credits": 320, "utilization": 6.4, "currency": "usd"}
}`

func refresh(t *testing.T, a *Adapter) domain.QuotaSnapshot {
	t.Helper()
	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	return snap.(domain.QuotaSnapshot)
}

func find(s domain.QuotaSnapshot, id string) (domain.QuotaWindow, bool) {
	for _, w := range s.Windows {
		if w.ID == id {
			return w, true
		}
	}

	return domain.QuotaWindow{}, false
}

func serr(t *testing.T, err error) *source.Error {
	t.Helper()
	var e *source.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *source.Error, got %v", err)
	}

	return e
}

func assertNoToken(t *testing.T, err error) {
	t.Helper()
	if err != nil && (strings.Contains(err.Error(), secretToken) || strings.Contains(serr(t, err).UserMessage(), secretToken)) {
		t.Errorf("token leaked into error: %v", err)
	}
}

func TestFullResponse(t *testing.T) {
	s := newServer(t, fullBody)
	path := writeCreds(t, credsJSON(secretToken, fixedNow.Add(time.Hour), "max"))
	snap := refresh(t, s.adapter(path))

	if w, _ := find(snap, idFiveHour); w.UsedFraction != 0.125 || w.ResetAt == nil || w.Name != "5-hour" {
		t.Errorf("5h: %+v", w)
	}
	if w, _ := find(snap, idWeekly); w.UsedFraction != 0.4 {
		t.Errorf("7d: %+v", w)
	}
	if w, ok := find(snap, "claude-model-opus"); !ok || w.UsedFraction != 0.7 || w.Name != "Opus weekly" {
		t.Errorf("opus: %+v %v", w, ok)
	}
	if w, ok := find(snap, "claude-model-fable"); !ok || w.UsedFraction != 0.34 {
		t.Errorf("fable: %+v %v", w, ok)
	}
	if len(snap.Windows) != 4 { // 5h, 7d, opus (limits[] "opus" deduped), fable
		t.Errorf("windows = %d: %+v", len(snap.Windows), snap.Windows)
	}
	if snap.Plan != "Max" {
		t.Errorf("plan = %q", snap.Plan)
	}
	x := snap.ExtraUsage
	if x == nil || !x.Enabled || *x.Used != 3.2 || *x.Limit != 50 || *x.Utilization != 6.4 || x.Currency != "USD" {
		t.Errorf("extra: %+v", x)
	}
	if s.auths[0] != "Bearer "+secretToken || s.beta != "oauth-2025-04-20" || s.query != "skip_spend=1" {
		t.Errorf("request: auth=%q beta=%q query=%q", s.auths[0], s.beta, s.query)
	}
}

func TestAllNullsIsInvalid(t *testing.T) {
	s := newServer(t, `{"five_hour":null,"seven_day":null,"limits":null,"extra_usage":null}`)
	path := writeCreds(t, credsJSON(secretToken, fixedNow.Add(time.Hour), "pro"))
	_, err := s.adapter(path).Refresh(context.Background())
	if serr(t, err).Category != source.CatInvalidResponse {
		t.Errorf("err = %v", err)
	}
}

func TestUtilizationAbove100IsClamped(t *testing.T) {
	s := newServer(t, `{"five_hour":{"utilization":130,"resets_at":null},"seven_day":{"utilization":5}}`)
	path := writeCreds(t, credsJSON(secretToken, fixedNow.Add(time.Hour), "pro"))
	w, _ := find(refresh(t, s.adapter(path)), idFiveHour)
	if w.UsedFraction != 1 || w.RemainingFraction != 0 || w.ResetAt != nil {
		t.Errorf("5h: %+v", w)
	}
}

func TestExpiredTokenSendsNoRequest(t *testing.T) {
	s := newServer(t, fullBody)
	path := writeCreds(t, credsJSON(secretToken, fixedNow.Add(-time.Minute), "pro"))
	_, err := s.adapter(path).Refresh(context.Background())
	e := serr(t, err)
	if e.Category != source.CatCredentials || e.Detail != msgExpired {
		t.Errorf("err = %v", err)
	}
	if s.requests() != 0 {
		t.Error("a request went out with an expired token")
	}
	assertNoToken(t, err)
}

func TestFileErrors(t *testing.T) {
	s := newServer(t, fullBody)
	cases := map[string]struct{ content, want string }{
		"invalid":  {`{not json ` + secretToken, msgInvalid},
		"no login": {`{"mcpOAuth":{}}`, msgNoLogin},
		"no token": {`{"claudeAiOauth":{"accessToken":""}}`, msgNoLogin},
	}
	for name, c := range cases {
		_, err := s.adapter(writeCreds(t, c.content)).Refresh(context.Background())
		if e := serr(t, err); e.Detail != c.want {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
		assertNoToken(t, err)
	}
	_, err := s.adapter(filepath.Join(t.TempDir(), "nope")).Refresh(context.Background())
	if serr(t, err).Detail != msgMissing {
		t.Errorf("missing: %v", err)
	}
	if s.requests() != 0 {
		t.Error("requests went out despite unusable credentials")
	}
}

func Test401SameTokenIsRejected(t *testing.T) {
	s := newServer(t, ``)
	s.set(401, ``)
	path := writeCreds(t, credsJSON(secretToken, fixedNow.Add(time.Hour), "pro"))
	_, err := s.adapter(path).Refresh(context.Background())
	if e := serr(t, err); e.Category != source.CatAuth || e.Detail != msgRejected {
		t.Errorf("err = %v", err)
	}
	if s.requests() != 1 {
		t.Errorf("requests = %d, want 1 (no retry with the same token)", s.requests())
	}
	assertNoToken(t, err)
}

func Test401ChangedTokenRetries(t *testing.T) {
	var path string
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if n == 1 {
			// Claude Code refreshes the file while the first request is
			// in flight.
			_ = os.WriteFile(path, []byte(credsJSON("new-token-value", fixedNow.Add(time.Hour), "pro")), 0o600)
			w.WriteHeader(401)

			return
		}
		if r.Header.Get("Authorization") != "Bearer new-token-value" {
			w.WriteHeader(401)

			return
		}
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":1}}`))
	}))
	defer srv.Close()
	path = writeCreds(t, credsJSON(secretToken, fixedNow.Add(time.Hour), "pro"))
	a := newAdapter(path, srv.URL, 5*time.Minute, &http.Client{}, logger())
	a.now = func() time.Time { return fixedNow }

	snap := refresh(t, a)
	if _, ok := find(snap, idFiveHour); !ok || hits != 2 {
		t.Errorf("hits = %d, windows = %+v", hits, snap.Windows)
	}
}

func Test429BacksOffWithoutRequests(t *testing.T) {
	s := newServer(t, ``)
	s.set(429, ``)
	path := writeCreds(t, credsJSON(secretToken, fixedNow.Add(24*time.Hour), "pro"))
	a := s.adapter(path)
	clock := fixedNow
	a.now = func() time.Time { return clock }

	_, err := a.Refresh(context.Background())
	if serr(t, err).Category != source.CatRateLimited || s.requests() != 1 {
		t.Fatalf("first: %v, requests %d", err, s.requests())
	}
	clock = clock.Add(5 * time.Minute) // inside 2× interval
	_, err = a.Refresh(context.Background())
	if serr(t, err).Category != source.CatRateLimited || s.requests() != 1 {
		t.Errorf("must not request during backoff: %v, requests %d", err, s.requests())
	}
	s.set(200, `{"five_hour":{"utilization":3}}`)
	clock = clock.Add(5*time.Minute + time.Second) // past 10 min
	if _, ok := find(refresh(t, a), idFiveHour); !ok || s.requests() != 2 {
		t.Errorf("requests = %d after backoff", s.requests())
	}
}

func TestExtraUsageVariants(t *testing.T) {
	path := writeCreds(t, credsJSON(secretToken, fixedNow.Add(time.Hour), "pro"))
	base := `{"five_hour":{"utilization":1},"extra_usage":%s}`

	s := newServer(t, fmt.Sprintf(base, `{"is_enabled":false,"monthly_limit":5000}`))
	if x := refresh(t, s.adapter(path)).ExtraUsage; x == nil || x.Enabled || x.Limit != nil {
		t.Errorf("disabled: %+v", x)
	}

	s.set(200, fmt.Sprintf(base, `{"is_enabled":true,"monthly_limit":null,"used_credits":250,"utilization":null,"currency":null}`))
	x := refresh(t, s.adapter(path)).ExtraUsage
	if x == nil || !x.Enabled || x.Limit != nil || *x.Used != 2.5 || x.Utilization != nil || x.Currency != "" {
		t.Errorf("null limit: %+v", x)
	}

	s.set(200, fmt.Sprintf(base, `{"is_enabled":true,"monthly_limit":50000,"used_credits":1200,"currency":"JPY"}`))
	x = refresh(t, s.adapter(path)).ExtraUsage
	if *x.Limit != 50000 || *x.Used != 1200 {
		t.Errorf("JPY is zero-decimal: %+v", x)
	}

	s.set(200, `{"five_hour":{"utilization":1}}`)
	if refresh(t, s.adapter(path)).ExtraUsage != nil {
		t.Error("absent extra_usage must stay nil")
	}
}

func TestServerErrorNeverLeaksToken(t *testing.T) {
	s := newServer(t, ``)
	s.set(500, secretToken)
	path := writeCreds(t, credsJSON(secretToken, fixedNow.Add(time.Hour), "pro"))
	_, err := s.adapter(path).Refresh(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	assertNoToken(t, err)

	s.set(200, `{bad `+secretToken)
	_, err = s.adapter(path).Refresh(context.Background())
	assertNoToken(t, err)
}

func TestSlug(t *testing.T) {
	if got := slug("fable 5.2 (beta)"); got != "fable-5-2-beta" {
		t.Errorf("slug = %q", got)
	}
}
