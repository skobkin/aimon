package updates

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeRelease mirrors the Forgejo release JSON fields the source adapter
// consumes.
type fakeRelease struct {
	TagName     string      `json:"tag_name"`
	Name        string      `json:"name"`
	Body        string      `json:"body"`
	HTMLURL     string      `json:"html_url"`
	Draft       bool        `json:"draft"`
	Prerelease  bool        `json:"prerelease"`
	PublishedAt string      `json:"published_at,omitempty"`
	CreatedAt   string      `json:"created_at,omitempty"`
	Assets      []fakeAsset `json:"assets"`
}

type fakeAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// forgejoFake serves a minimal Forgejo releases API. Payloads can be swapped
// between requests to simulate a new upstream release.
type forgejoFake struct {
	server *httptest.Server
	hits   atomic.Int64

	mu       sync.Mutex
	releases []fakeRelease
}

func newForgejoFake(t *testing.T, releases ...fakeRelease) *forgejoFake {
	t.Helper()
	fake := &forgejoFake{releases: releases}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/repos/skobkin/aimon/releases", func(w http.ResponseWriter, r *http.Request) {
		fake.hits.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse query: %v", err)
			http.Error(w, "bad query", http.StatusBadRequest)

			return
		}
		if got := r.Form.Get("limit"); got == "" {
			t.Error("expected limit query parameter")
		}
		if got := r.Form.Get("page"); got == "" {
			t.Error("expected page query parameter")
		}
		fake.mu.Lock()
		payload := fake.releases
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *forgejoFake) setReleases(releases ...fakeRelease) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = releases
}

func (f *forgejoFake) url() string {
	return f.server.URL
}

// blockingFake accepts requests but never answers, so an in-flight check can
// be observed while it hangs.
func blockingFake(t *testing.T) *forgejoFake {
	t.Helper()
	fake := &forgejoFake{}
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/repos/skobkin/aimon/releases", func(w http.ResponseWriter, _ *http.Request) {
		fake.hits.Add(1)
		<-release
	})
	fake.server = httptest.NewServer(mux)
	// Cleanup order matters: the server must be closed after the handler is
	// unblocked, so unblock first (cleanups run LIFO).
	t.Cleanup(fake.server.Close)
	t.Cleanup(func() { close(release) })

	return fake
}

func stableRelease(tag, publishedAt string) fakeRelease {
	return fakeRelease{
		TagName:     tag,
		Name:        tag,
		Body:        "## Changelog\n- fix: something (#12)\n- chore: abc123def4567890abcdef1234567890abcdef12",
		HTMLURL:     "https://git.example.invalid/skobkin/aimon/releases/tag/" + tag,
		PublishedAt: publishedAt,
	}
}
