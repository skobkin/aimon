package kimi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

const testAccount = credentials.AccountID("source/test-instance/api-key")

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type memStore struct {
	secret   string
	notFound bool
	stateErr *credentials.StateError
}

func (s *memStore) Status(context.Context) credentials.Status {
	return credentials.Status{State: credentials.StateAvailable}
}

func (s *memStore) Get(context.Context, credentials.AccountID) (string, error) {
	if s.stateErr != nil {
		return "", s.stateErr
	}
	if s.notFound || s.secret == "" {
		return "", credentials.ErrNotFound
	}

	return s.secret, nil
}
func (s *memStore) Set(context.Context, credentials.AccountID, string) error { return nil }
func (s *memStore) Delete(context.Context, credentials.AccountID) error      { return nil }

// fixture is the sanitized live shape from the research comment.
const fixture = `{
  "limits": [
    {"window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"},
     "detail": {"limit": "100", "used": "40", "resetTime": "2026-09-22T12:22:15.593402Z"}}
  ],
  "usages": {
    "limit_5h":          {"used_ratio": 0.9, "reset_time": "2026-09-22T12:22:15Z"},
    "limit_month_total": {"used_ratio": 0.0795, "reset_time": "2026-10-22T00:00:00Z"},
    "limit_month_code":  {"used_ratio": 0.5, "reset_time": "2026-10-22T00:00:00Z"}
  },
  "user": {"membership": {"level": "LEVEL_INTERMEDIATE"}}
}`

type server struct {
	t        *testing.T
	usages   string
	usageSt  int
	me       string
	meSt     int
	mu       sync.Mutex
	auths    []string
	meCalls  int
	usageHit int
	srv      *httptest.Server
}

func newServer(t *testing.T, usages string) *server {
	s := &server{t: t, usages: usages, usageSt: 200, me: `{"user_level_name":"Allegretto"}`, meSt: 200}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.auths = append(s.auths, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case usagesPath:
			s.usageHit++
			w.WriteHeader(s.usageSt)
			_, _ = w.Write([]byte(s.usages))
		case mePath:
			s.meCalls++
			w.WriteHeader(s.meSt)
			_, _ = w.Write([]byte(s.me))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.srv.Close)

	return s
}

func (s *server) adapter(creds credentials.Store) *Adapter {
	return New(s.srv.URL, testAccount, creds, &http.Client{}, testLogger())
}

func (s *server) calls() (usage, me int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.usageHit, s.meCalls
}

func refresh(t *testing.T, a *Adapter) domain.QuotaSnapshot {
	t.Helper()
	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	return snap.(domain.QuotaSnapshot)
}

func win(t *testing.T, s domain.QuotaSnapshot, id string) domain.QuotaWindow {
	t.Helper()
	for _, w := range s.Windows {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("window %q missing in %+v", id, s.Windows)

	return domain.QuotaWindow{}
}

func serr(t *testing.T, err error) *source.Error {
	t.Helper()
	var e *source.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *source.Error, got %v", err)
	}

	return e
}

func TestLiveShape(t *testing.T) {
	s := newServer(t, fixture)
	snap := refresh(t, s.adapter(&memStore{secret: "k"}))

	if len(snap.Windows) != 2 {
		t.Fatalf("want 5h + monthly only, got %+v", snap.Windows)
	}
	b := win(t, snap, idBurst)
	if b.UsedFraction != 0.4 || b.Limit == nil || *b.Limit != 100 || *b.Remaining != 60 || b.ResetAt == nil {
		t.Errorf("burst: %+v", b) // limit_5h (0.9) must be ignored
	}
	m := win(t, snap, idMonthly)
	if m.UsedFraction != 0.0795 || m.Limit != nil || m.ResetAt == nil {
		t.Errorf("monthly: %+v", m)
	}
	if snap.Plan != "Allegretto" {
		t.Errorf("plan = %q", snap.Plan)
	}
	if s.auths[0] != "Bearer k" {
		t.Errorf("auth = %q", s.auths[0])
	}
}

func TestRemainingInsteadOfUsed(t *testing.T) {
	s := newServer(t, `{"limits":[{"window":{"duration":5,"timeUnit":"TIME_UNIT_HOUR"},"detail":{"limit":200,"remaining":50}}]}`)
	w := win(t, refresh(t, s.adapter(&memStore{secret: "k"})), idBurst)
	if w.UsedFraction != 0.75 {
		t.Errorf("used = %v", w.UsedFraction)
	}
}

func TestBurstSelectedByDurationNotPosition(t *testing.T) {
	s := newServer(t, `{"limits":[
	 {"window":{"duration":1,"timeUnit":"TIME_UNIT_DAY"},"detail":{"limit":10,"used":10}},
	 {"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":10,"used":1}}]}`)
	w := win(t, refresh(t, s.adapter(&memStore{secret: "k"})), idBurst)
	if w.UsedFraction != 0.1 {
		t.Errorf("used = %v", w.UsedFraction)
	}
}

func TestLimitsNullAndLegacyWeekly(t *testing.T) {
	s := newServer(t, `{"limits":null,"usages":{"limit_month_total":{"used_ratio":0.2}},"usage":{"limit":"10","used":"5","resetAt":"2026-10-01T00:00:00Z"}}`)
	snap := refresh(t, s.adapter(&memStore{secret: "k"}))
	if len(snap.Windows) != 2 {
		t.Fatalf("windows: %+v", snap.Windows)
	}
	if w := win(t, snap, idWeekly); w.UsedFraction != 0.5 || w.ResetAt == nil {
		t.Errorf("weekly: %+v", w)
	}
}

func TestNoUsableWindows(t *testing.T) {
	s := newServer(t, `{"limits":null}`)
	_, err := s.adapter(&memStore{secret: "k"}).Refresh(context.Background())
	if serr(t, err).Category != source.CatInvalidResponse {
		t.Errorf("err = %v", err)
	}
}

func TestAuthFailureAndMalformed(t *testing.T) {
	s := newServer(t, fixture)
	s.usageSt = 401
	_, err := s.adapter(&memStore{secret: "k"}).Refresh(context.Background())
	if serr(t, err).Category != source.CatAuth {
		t.Errorf("err = %v", err)
	}

	s2 := newServer(t, `{not json`)
	_, err = s2.adapter(&memStore{secret: "k"}).Refresh(context.Background())
	if serr(t, err).Category != source.CatInvalidResponse {
		t.Errorf("err = %v", err)
	}
}

func TestCredentialErrors(t *testing.T) {
	s := newServer(t, fixture)
	_, err := s.adapter(&memStore{notFound: true}).Refresh(context.Background())
	if e := serr(t, err); e.Category != source.CatCredentials || e.Detail != "API key not set" {
		t.Errorf("err = %v", err)
	}
	_, err = s.adapter(&memStore{stateErr: &credentials.StateError{State: credentials.StateLocked, Message: "locked"}}).Refresh(context.Background())
	if serr(t, err).Category != source.CatCredentials {
		t.Errorf("err = %v", err)
	}
	if u, _ := s.calls(); u != 0 {
		t.Errorf("no request may go out without a key, got %d", u)
	}
}

func TestPlanCachedAcrossRefreshes(t *testing.T) {
	s := newServer(t, fixture)
	a := s.adapter(&memStore{secret: "k"})
	for i := 0; i < 3; i++ {
		refresh(t, a)
	}
	if _, me := s.calls(); me != 1 {
		t.Errorf("/me calls = %d, want 1", me)
	}
}

func TestPlanRefetchedOnLevelChangeAndTTL(t *testing.T) {
	s := newServer(t, fixture)
	a := s.adapter(&memStore{secret: "k"})
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	refresh(t, a)

	s.mu.Lock()
	s.usages = `{"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":1,"used":0}}],"user":{"membership":{"level":"LEVEL_ADVANCED"}}}`
	s.mu.Unlock()
	refresh(t, a)
	if _, me := s.calls(); me != 2 {
		t.Errorf("level change: /me calls = %d, want 2", me)
	}

	clock = clock.Add(planTTL + time.Minute)
	refresh(t, a)
	if _, me := s.calls(); me != 3 {
		t.Errorf("TTL: /me calls = %d, want 3", me)
	}
}

func TestPlanFailureFallsBackAndBacksOff(t *testing.T) {
	s := newServer(t, fixture)
	s.meSt = 500
	a := s.adapter(&memStore{secret: "k"})
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }

	snap := refresh(t, a)
	if snap.Plan != "Intermediate" || len(snap.Windows) == 0 {
		t.Errorf("fallback: plan %q windows %d", snap.Plan, len(snap.Windows))
	}
	clock = clock.Add(10 * time.Minute)
	refresh(t, a)
	if _, me := s.calls(); me != 1 {
		t.Errorf("must not retry within the backoff, calls = %d", me)
	}
	clock = clock.Add(planRetryAfter)
	refresh(t, a)
	if _, me := s.calls(); me != 2 {
		t.Errorf("retry after backoff, calls = %d", me)
	}
}

func TestPrettyLevel(t *testing.T) {
	for in, want := range map[string]string{"LEVEL_INTERMEDIATE": "Intermediate", "": "", "LEVEL_PRO_PLUS": "Pro plus"} {
		if got := prettyLevel(in); got != want {
			t.Errorf("prettyLevel(%q) = %q, want %q", in, got, want)
		}
	}
}
