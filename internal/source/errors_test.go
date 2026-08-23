package source

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUserMessages(t *testing.T) {
	cases := []struct {
		err  *Error
		want string
	}{
		{&Error{Category: CatConnection}, "Connection failed"},
		{&Error{Category: CatTimeout}, "Request timed out"},
		{&Error{Category: CatAuth}, "Authentication failed"},
		{&Error{Category: CatRateLimited}, "Rate limited"},
		{&Error{Category: CatInvalidResponse}, "Invalid provider response"},
		{&Error{Category: CatProvider}, "Provider error (HTTP 5xx)"},
		{&Error{Category: CatHTTP, StatusCode: 404}, "Provider returned HTTP 404"},
		{&Error{Category: CatCredentials, Detail: "API key not set"}, "API key not set"},
		{&Error{Category: CatConfig, Detail: "No GPUs discovered"}, "No GPUs discovered"},
	}
	for _, tc := range cases {
		if got := tc.err.UserMessage(); got != tc.want {
			t.Errorf("UserMessage(%s) = %q, want %q", tc.err.Category, got, tc.want)
		}
	}
}

func TestClassifyStatus(t *testing.T) {
	cases := map[int]Category{
		401:           CatAuth,
		403:           CatAuth,
		429:           CatRateLimited,
		500:           CatProvider,
		503:           CatProvider,
		404:           CatHTTP,
		418:           CatHTTP,
		http.StatusOK: CatHTTP, // callers never ask for 2xx, but classify anyway
	}
	for code, want := range cases {
		if got := classifyStatus(code); got != want {
			t.Errorf("classifyStatus(%d) = %s, want %s", code, got, want)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestClassifyTransport(t *testing.T) {
	if got := classifyTransport(timeoutErr{}); got != CatTimeout {
		t.Errorf("net.Error timeout classified as %s", got)
	}
	if got := classifyTransport(context.DeadlineExceeded); got != CatTimeout {
		t.Errorf("deadline classified as %s", got)
	}
	if got := classifyTransport(errors.New("connection refused")); got != CatConnection {
		t.Errorf("generic error classified as %s", got)
	}
	// A *url.Error wrapping a timeout must still read as timeout.
	wrapped := &urlErrorWrap{op: "Get", err: timeoutErr{}}
	if got := classifyTransport(wrapped); got != CatTimeout {
		t.Errorf("wrapped timeout classified as %s", got)
	}
	_ = net.ErrClosed
}

type urlErrorWrap struct {
	op  string
	err error
}

func (u *urlErrorWrap) Error() string { return fmt.Sprintf("%s: %v", u.op, u.err) }
func (u *urlErrorWrap) Unwrap() error { return u.err }

func TestGetJSONClassifications(t *testing.T) {
	type payload struct {
		Value int `json:"value"`
	}
	client := &http.Client{Timeout: 2 * time.Second}

	// 401 -> auth
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	var p payload
	if err := GetJSON(context.Background(), client, srv.URL, nil, &p); err == nil || err.Category != CatAuth {
		t.Errorf("401: got %v", err)
	}
	srv.Close()

	// 429 -> rate limited
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	if err := GetJSON(context.Background(), client, srv.URL, nil, &p); err == nil || err.Category != CatRateLimited {
		t.Errorf("429: got %v", err)
	}
	srv.Close()

	// 503 -> provider
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	if err := GetJSON(context.Background(), client, srv.URL, nil, &p); err == nil || err.Category != CatProvider {
		t.Errorf("503: got %v", err)
	}
	srv.Close()

	// 404 -> plain http
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	if err := GetJSON(context.Background(), client, srv.URL, nil, &p); err == nil || err.Category != CatHTTP {
		t.Errorf("404: got %v", err)
	}
	srv.Close()

	// malformed body -> invalid response
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{not json`))
	}))
	if err := GetJSON(context.Background(), client, srv.URL, nil, &p); err == nil || err.Category != CatInvalidResponse {
		t.Errorf("malformed: got %v", err)
	}
	srv.Close()

	// closed port -> connection
	if err := GetJSON(context.Background(), client, "http://127.0.0.1:1", nil, &p); err == nil || err.Category != CatConnection {
		t.Errorf("closed port: got %v", err)
	}

	// request timeout -> timeout
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte(`{}`))
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := GetJSON(ctx, client, slow.URL, nil, &p); err == nil || err.Category != CatTimeout {
		t.Errorf("timeout: got %v", err)
	}
	slow.Close()
}

func TestGetJSONHeadersAndBody(t *testing.T) {
	var gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept-Language")
		w.Write([]byte(`{"value":3}`))
	}))
	defer srv.Close()

	header := http.Header{}
	header.Set("Authorization", "raw-token")
	header.Set("Accept-Language", "en-US,en")

	type payload struct {
		Value int `json:"value"`
	}
	var p payload
	if err := GetJSON(context.Background(), srv.Client(), srv.URL, header, &p); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if gotAuth != "raw-token" || gotAccept != "en-US,en" {
		t.Errorf("headers not forwarded: auth=%q accept=%q", gotAuth, gotAccept)
	}
	if p.Value != 3 {
		t.Errorf("decoded value = %d", p.Value)
	}
}

func TestGetText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	}))
	defer srv.Close()

	body, err := GetText(context.Background(), srv.Client(), srv.URL, nil)
	if err != nil || body != "OK" {
		t.Fatalf("GetText = (%q, %v)", body, err)
	}
}
