package source

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// countingListener counts accepted connections so tests can tell a reused
// keep-alive connection from a fresh dial.
type countingListener struct {
	net.Listener
	accepts *atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}

	return c, err
}

// newCountingServer starts a test server whose handler is h and reports how
// many TCP connections it accepted.
func newCountingServer(t *testing.T, h http.Handler) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	accepts := &atomic.Int32{}
	srv := &httptest.Server{
		Listener: &countingListener{Listener: ln, accepts: accepts},
		Config:   &http.Server{Handler: h, ReadHeaderTimeout: time.Second},
	}
	srv.Start()
	t.Cleanup(srv.Close)

	return srv, accepts
}

// A non-2xx body must be drained before Close, or the Transport cannot
// reuse the keep-alive connection and every failed poll dials anew.
func TestGetJSONReusesConnectionAfterErrorStatus(t *testing.T) {
	srv, accepts := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited, with a body"))
	}))

	type payload struct {
		Value int `json:"value"`
	}
	client := &http.Client{}
	for i := 0; i < 3; i++ {
		var p payload
		if err := GetJSON(context.Background(), client, srv.URL, nil, &p); err == nil || err.Category != CatRateLimited {
			t.Fatalf("poll %d: got %v, want rate-limit error", i, err)
		}
	}
	if n := accepts.Load(); n != 1 {
		t.Errorf("dialed %d connections for 3 failed polls, want 1 (keep-alive reuse)", n)
	}
}

// A body larger than maxBodyBytes is truncated by the LimitReader and the
// JSON decode fails; the unread remainder must still be drained so the
// next poll reuses the connection.
func TestGetJSONReusesConnectionAfterOversizedBody(t *testing.T) {
	big := make([]byte, maxBodyBytes+64)
	for i := range big {
		big[i] = 'x'
	}
	srv, accepts := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))

	type payload struct {
		Value int `json:"value"`
	}
	client := &http.Client{}
	for i := 0; i < 2; i++ {
		var p payload
		if err := GetJSON(context.Background(), client, srv.URL, nil, &p); err == nil || err.Category != CatInvalidResponse {
			t.Fatalf("poll %d: got %v, want invalid-response error", i, err)
		}
	}
	if n := accepts.Load(); n != 1 {
		t.Errorf("dialed %d connections for 2 oversized polls, want 1 (keep-alive reuse)", n)
	}
}

// GetText shares the drain on its non-2xx path (the llama-swap /health
// probe polls it continuously).
func TestGetTextReusesConnectionAfterErrorStatus(t *testing.T) {
	srv, accepts := newCountingServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}))

	client := &http.Client{}
	for i := 0; i < 3; i++ {
		if _, err := GetText(context.Background(), client, srv.URL, nil); err == nil || err.Category != CatProvider {
			t.Fatalf("poll %d: got %v, want provider error", i, err)
		}
	}
	if n := accepts.Load(); n != 1 {
		t.Errorf("dialed %d connections for 3 failed probes, want 1 (keep-alive reuse)", n)
	}
}
