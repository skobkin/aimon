package updates

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/watch"
)

func newTestManager(t *testing.T, serverURL string, currentVersion string, handler func(updates.Result)) *Manager {
	t.Helper()

	return newTestManagerWithLogger(t, serverURL, currentVersion, slog.New(slog.DiscardHandler), handler)
}

func newTestManagerWithLogger(
	t *testing.T,
	serverURL string,
	currentVersion string,
	logger *slog.Logger,
	handler func(updates.Result),
) *Manager {
	t.Helper()
	manager, err := New(Options{
		ServerURL:         serverURL,
		CurrentVersion:    currentVersion,
		Interval:          20 * time.Millisecond,
		Timeout:           2 * time.Second,
		Logger:            logger,
		OnUpdateAvailable: handler,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(manager.Stop)

	return manager
}

// waitForCondition polls cond until it holds or the deadline passes.
func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func waitForHits(t *testing.T, fake *forgejoFake, want int64) {
	t.Helper()
	waitForCondition(t, func() bool { return fake.hits.Load() >= want })
}

func TestCheckNowStatuses(t *testing.T) {
	tests := []struct {
		name          string
		current       string
		releases      []fakeRelease
		wantStatus    updates.Status
		wantLatest    string
		wantLatestNil bool
		wantRepoURL   string
	}{
		{
			name:        "update available",
			current:     "0.6.0",
			releases:    []fakeRelease{stableRelease("0.9.0", "2026-09-01T00:00:00Z"), stableRelease("0.6.0", "2026-06-01T00:00:00Z")},
			wantStatus:  updates.StatusUpdateAvailable,
			wantLatest:  "0.9.0",
			wantRepoURL: "REPO",
		},
		{
			name:       "up to date",
			current:    "0.9.0",
			releases:   []fakeRelease{stableRelease("0.9.0", "2026-09-01T00:00:00Z")},
			wantStatus: updates.StatusUpToDate,
			wantLatest: "0.9.0",
		},
		{
			// Release tags are not "v"-prefixed, and installed builds may
			// report either spelling: the comparison must normalize both.
			name:        "unprefixed current version detects update",
			current:     "0.6.2",
			releases:    []fakeRelease{stableRelease("0.9.0", "2026-09-01T00:00:00Z")},
			wantStatus:  updates.StatusUpdateAvailable,
			wantLatest:  "0.9.0",
			wantRepoURL: "REPO",
		},
		{
			name:       "v-prefixed current equals unprefixed release",
			current:    "v0.9.0",
			releases:   []fakeRelease{stableRelease("0.9.0", "2026-09-01T00:00:00Z")},
			wantStatus: updates.StatusUpToDate,
			wantLatest: "0.9.0",
		},
		{
			name:        "prerelease excluded",
			current:     "0.6.0",
			releases:    []fakeRelease{stableRelease("0.9.0", "2026-08-01T00:00:00Z"), {TagName: "0.10.0-rc1", Prerelease: true, PublishedAt: "2026-08-15T00:00:00Z"}},
			wantStatus:  updates.StatusUpdateAvailable,
			wantLatest:  "0.9.0",
			wantRepoURL: "REPO",
		},
		{
			name:        "draft excluded",
			current:     "0.6.0",
			releases:    []fakeRelease{stableRelease("0.9.0", "2026-08-01T00:00:00Z"), {TagName: "0.11.0", Draft: true, PublishedAt: "2026-08-20T00:00:00Z"}},
			wantStatus:  updates.StatusUpdateAvailable,
			wantLatest:  "0.9.0",
			wantRepoURL: "REPO",
		},
		{
			name:          "empty feed",
			current:       "0.6.0",
			releases:      nil,
			wantStatus:    updates.StatusUnknown,
			wantLatestNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newForgejoFake(t, tc.releases...)
			manager := newTestManager(t, fake.url(), tc.current, nil)

			result, err := manager.CheckNow(context.Background())
			if err != nil {
				t.Fatalf("check now: %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("status = %v, want %v", result.Status, tc.wantStatus)
			}
			if tc.wantLatestNil {
				if result.Latest != nil {
					t.Fatalf("latest = %+v, want nil", result.Latest)
				}

				return
			}
			if result.Latest == nil || result.Latest.Version != tc.wantLatest {
				t.Fatalf("latest = %+v, want version %q", result.Latest, tc.wantLatest)
			}
			if tc.wantRepoURL == "REPO" && result.Feed.RepositoryURL != fake.url()+"/skobkin/aimon" {
				t.Fatalf("repository URL = %q, want %q", result.Feed.RepositoryURL, fake.url()+"/skobkin/aimon")
			}
		})
	}
}

func TestCheckNowDevVersionIsUnknown(t *testing.T) {
	fake := newForgejoFake(t, stableRelease("0.9.0", "2026-09-01T00:00:00Z"))
	manager := newTestManager(t, fake.url(), "dev", nil)

	result, err := manager.CheckNow(context.Background())
	if err != nil {
		t.Fatalf("check now: %v", err)
	}
	if result.Status != updates.StatusUnknown {
		t.Fatalf("status = %v, want unknown for dev build", result.Status)
	}
}

func TestCheckNowServerError(t *testing.T) {
	// A 500 response must surface as an error, not a zero result.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	manager := newTestManager(t, srv.URL, "0.6.0", nil)

	result, err := manager.CheckNow(context.Background())
	if err == nil {
		t.Fatalf("expected error, got result %+v", result)
	}
	if result.Status != updates.StatusUnknown {
		t.Fatalf("status = %v, want unknown on error", result.Status)
	}
}

func TestCheckNowMarksVersionNotified(t *testing.T) {
	fake := newForgejoFake(t, stableRelease("0.9.0", "2026-09-01T00:00:00Z"))

	notified := make(chan string, 4)
	manager := newTestManager(t, fake.url(), "0.6.0", func(result updates.Result) {
		notified <- result.Latest.Version
	})

	if _, err := manager.CheckNow(context.Background()); err != nil {
		t.Fatalf("manual check: %v", err)
	}

	manager.Start()
	waitForHits(t, fake, 3)
	select {
	case version := <-notified:
		t.Fatalf("unexpected notification after manual check claimed the version: %q", version)
	case <-time.After(100 * time.Millisecond):
	}

	fake.setReleases(stableRelease("0.10.0", "2026-09-02T00:00:00Z"), stableRelease("0.9.0", "2026-09-01T00:00:00Z"))
	waitForCondition(t, func() bool {
		select {
		case <-notified:
			return true
		default:
			return false
		}
	})
}

func TestAutoCheckNotifiesOncePerVersion(t *testing.T) {
	fake := newForgejoFake(t, stableRelease("0.9.0", "2026-09-01T00:00:00Z"))

	var mu sync.Mutex
	count := 0
	manager := newTestManager(t, fake.url(), "0.6.0", func(updates.Result) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	manager.Start()
	waitForHits(t, fake, 3)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("notifications = %d, want 1", count)
	}
}

func TestAutoCheckUpToDateNoNotification(t *testing.T) {
	fake := newForgejoFake(t, stableRelease("0.9.0", "2026-09-01T00:00:00Z"))

	notified := make(chan string, 4)
	manager := newTestManager(t, fake.url(), "0.9.0", func(result updates.Result) {
		notified <- result.Latest.Version
	})

	manager.Start()
	waitForHits(t, fake, 2)
	select {
	case version := <-notified:
		t.Fatalf("unexpected notification for up-to-date install: %q", version)
	case <-time.After(100 * time.Millisecond):
	}
	if !manager.Running() {
		t.Fatal("expected manager to stay running")
	}
}

func TestAutoCheckErrorLogsOnly(t *testing.T) {
	// A failing endpoint: the manager must keep running and never notify.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	notified := make(chan string, 4)
	manager := newTestManager(t, srv.URL, "0.6.0", func(result updates.Result) {
		notified <- result.Latest.Version
	})

	manager.Start()
	// Several attempts must have completed (and failed) before we assert.
	time.Sleep(200 * time.Millisecond)
	select {
	case version := <-notified:
		t.Fatalf("unexpected notification despite failures: %q", version)
	default:
	}
	if !manager.Running() {
		t.Fatal("expected manager to stay running through failures")
	}
}

func TestStartStopRestart(t *testing.T) {
	fake := newForgejoFake(t, stableRelease("0.9.0", "2026-09-01T00:00:00Z"))

	notified := make(chan string, 4)
	manager := newTestManager(t, fake.url(), "0.6.0", func(result updates.Result) {
		notified <- result.Latest.Version
	})

	manager.Start()
	if version := <-notified; version != "0.9.0" {
		t.Fatalf("first notification = %q, want 0.9.0", version)
	}
	manager.Stop()
	if manager.Running() {
		t.Fatal("expected manager to be stopped")
	}

	// A stopped watcher is single-use; restarting must build a fresh one and
	// a new upstream release must notify again.
	fake.setReleases(stableRelease("0.10.0", "2026-09-02T00:00:00Z"), stableRelease("0.9.0", "2026-09-01T00:00:00Z"))
	manager.Start()
	if version := <-notified; version != "0.10.0" {
		t.Fatalf("restart notification = %q, want 0.10.0", version)
	}
	manager.Stop()
}

func TestStopCancelsInFlightCheck(t *testing.T) {
	fake := blockingFake(t)
	manager := newTestManager(t, fake.url(), "0.6.0", nil)

	manager.Start()
	// Wait until the check attempt is in flight.
	waitForHits(t, fake, 1)

	done := make(chan struct{})
	go func() {
		manager.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return while a check was in flight")
	}
	if manager.Running() {
		t.Fatal("expected manager to be stopped")
	}
}

func TestStartStopIdempotence(t *testing.T) {
	fake := newForgejoFake(t, stableRelease("0.9.0", "2026-09-01T00:00:00Z"))
	manager := newTestManager(t, fake.url(), "0.6.0", nil)

	manager.Stop() // stop before start: no-op
	manager.Start()
	manager.Start() // double start: no-op
	if !manager.Running() {
		t.Fatal("expected manager to be running")
	}
	waitForHits(t, fake, 1)
	manager.Stop()
	manager.Stop() // double stop: no-op
	if manager.Running() {
		t.Fatal("expected manager to be stopped")
	}
}

func TestNewRejectsBadRepository(t *testing.T) {
	if _, err := New(Options{Repository: "owner/repo/extra"}); err == nil {
		t.Fatal("expected error for malformed repository")
	}
}

func TestCheckNowHonorsParentContextCancellation(t *testing.T) {
	fake := blockingFake(t)
	manager := newTestManager(t, fake.url(), "0.6.0", nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.CheckNow(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

// Start must announce itself: at the default info level it is the only
// launch-time evidence that periodic checks are active.
func TestStartLogsStartupLine(t *testing.T) {
	fake := newForgejoFake(t, stableRelease("0.9.0", "2026-09-01T00:00:00Z"))
	var buf bytes.Buffer
	manager := newTestManagerWithLogger(t, fake.url(), "0.9.0",
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), nil)

	manager.Start()
	if !strings.Contains(buf.String(), "periodic update checks started") {
		t.Fatalf("log output = %q, want the startup line", buf.String())
	}
	if !strings.Contains(buf.String(), "interval=20ms") {
		t.Fatalf("log output = %q, want the check interval", buf.String())
	}
}

// Every completed automatic check is logged at debug level, so a debug-level
// log shows the checker ran even when nothing newer was found.
func TestHandleEventLogsCompletedCheck(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	manager := newTestManagerWithLogger(t, "http://127.0.0.1:1", "0.9.0", logger, nil)

	manager.handleEvent(watch.Event{State: watch.State{Result: &updates.Result{
		Status:         updates.StatusUpToDate,
		CurrentVersion: "0.9.0",
		Latest:         &updates.Release{Version: "0.9.0"},
	}}})
	if !strings.Contains(buf.String(), "automatic update check completed") {
		t.Fatalf("log output = %q, want the completion line", buf.String())
	}
	if !strings.Contains(buf.String(), "status=up_to_date") {
		t.Fatalf("log output = %q, want the check status", buf.String())
	}
}
