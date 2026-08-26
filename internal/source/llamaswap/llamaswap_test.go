package llamaswap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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

// testAccount is a stand-in instance-scoped account for adapter tests.
const testAccount = credentials.AccountID("source/test-instance/api-key")

func withStore(secret string, notFound bool, stateErr *credentials.StateError) credentials.Store {
	return &memStore{secret: secret, notFound: notFound, stateErr: stateErr}
}

const runningPayload = `{"running":[
	{"model":"qwen3:14b","state":"ready","cmd":"","proxy":"","ttl":300,"name":"Qwen 3 14B","description":""},
	{"model":"gemma3:4b","state":"starting","cmd":"","proxy":"","ttl":0,"name":"","description":""}
]}`

// swapServer serves /health and /running, capturing the Authorization
// header of each request.
type swapServer struct {
	healthStatus int
	healthBody   string
	runningCode  int
	runningBody  string

	mu   sync.Mutex
	auth []string // Authorization header per request, "" when absent
}

func newSwapServer() *swapServer {
	return &swapServer{healthStatus: http.StatusOK, healthBody: "OK", runningCode: http.StatusOK, runningBody: runningPayload}
}

func (s *swapServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()

		switch r.URL.Path {
		case "/health":
			w.WriteHeader(s.healthStatus)
			w.Write([]byte(s.healthBody))
		case "/running":
			w.WriteHeader(s.runningCode)
			w.Write([]byte(s.runningBody))
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *swapServer) authHeaders() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

func TestRefreshHappyPath(t *testing.T) {
	srv := newSwapServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	a := New(server.URL, testAccount, withStore("token-1", false, nil), &http.Client{}, testLogger())
	snap, err := a.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	s := snap.(domain.LlamaSwapSnapshot)
	if !s.Healthy {
		t.Error("healthy flag not set")
	}
	if len(s.RunningModels) != 2 {
		t.Fatalf("running models = %d, want 2", len(s.RunningModels))
	}
	if s.RunningModels[0].ID != "qwen3:14b" || s.RunningModels[0].Name != "Qwen 3 14B" {
		t.Errorf("model[0] = %+v", s.RunningModels[0])
	}
	if s.RunningModels[1].ID != "gemma3:4b" || s.RunningModels[1].Name != "" {
		t.Errorf("model[1] = %+v", s.RunningModels[1])
	}

	// Both requests carry the Bearer token when one is stored.
	for i, h := range srv.authHeaders() {
		if h != "Bearer token-1" {
			t.Errorf("request %d Authorization = %q", i, h)
		}
	}
}

func TestRefreshUnauthenticatedWhenNoKeyStored(t *testing.T) {
	srv := newSwapServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	a := New(server.URL, testAccount, withStore("", true, nil), &http.Client{}, testLogger())
	if _, err := a.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	for i, h := range srv.authHeaders() {
		if h != "" {
			t.Errorf("request %d must not carry Authorization, got %q", i, h)
		}
	}
}

func TestRefreshNilStoreIsUnauthenticated(t *testing.T) {
	srv := newSwapServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	a := New(server.URL, testAccount, nil, &http.Client{}, testLogger())
	if _, err := a.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	for i, h := range srv.authHeaders() {
		if h != "" {
			t.Errorf("request %d must not carry Authorization, got %q", i, h)
		}
	}
}

func TestKeychainLockedIsCredentialsError(t *testing.T) {
	server := httptest.NewServer(newSwapServer().handler())
	defer server.Close()

	locked := &credentials.StateError{State: credentials.StateLocked, Message: "collection is locked"}
	a := New(server.URL, testAccount, withStore("", false, locked), &http.Client{}, testLogger())

	_, err := a.Refresh(context.Background())
	var serr *source.Error
	if !errors.As(err, &serr) {
		t.Fatalf("expected *source.Error, got %T %v", err, err)
	}
	if serr.Category != source.CatCredentials {
		t.Errorf("category = %s, want credentials", serr.Category)
	}
}

func TestHealthAuthFailure(t *testing.T) {
	srv := newSwapServer()
	srv.healthStatus = http.StatusUnauthorized
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	_, err := New(server.URL, testAccount, nil, &http.Client{}, testLogger()).Refresh(context.Background())
	var serr *source.Error
	if !errors.As(err, &serr) || serr.Category != source.CatAuth {
		t.Fatalf("401 on /health must be auth, got %v", err)
	}
}

func TestRunningServerError(t *testing.T) {
	srv := newSwapServer()
	srv.runningCode = http.StatusInternalServerError
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	_, err := New(server.URL, testAccount, nil, &http.Client{}, testLogger()).Refresh(context.Background())
	var serr *source.Error
	if !errors.As(err, &serr) || serr.Category != source.CatProvider {
		t.Fatalf("500 on /running must be provider, got %v", err)
	}
}

func TestRunningMalformed(t *testing.T) {
	srv := newSwapServer()
	srv.runningBody = `{not json`
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	_, err := New(server.URL, testAccount, nil, &http.Client{}, testLogger()).Refresh(context.Background())
	var serr *source.Error
	if !errors.As(err, &serr) || serr.Category != source.CatInvalidResponse {
		t.Fatalf("malformed /running must be invalid-response, got %v", err)
	}
}

func TestEmptyRunningList(t *testing.T) {
	srv := newSwapServer()
	srv.runningBody = `{"running":[]}`
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	snap, err := New(server.URL, testAccount, nil, &http.Client{}, testLogger()).Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if models := snap.(domain.LlamaSwapSnapshot).RunningModels; len(models) != 0 {
		t.Errorf("expected empty model list, got %+v", models)
	}
}

func TestTestProbesHealthOnly(t *testing.T) {
	srv := newSwapServer()
	server := httptest.NewServer(srv.handler())
	defer server.Close()

	if err := New(server.URL, testAccount, nil, &http.Client{}, testLogger()).Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if got := len(srv.authHeaders()); got != 1 {
		t.Errorf("Test must hit only /health, requests = %d", got)
	}
}
