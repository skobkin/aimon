// Package updates wires the go4updates library into the application: it owns
// the Forgejo release source, one-shot checks, and the periodic watcher, and
// notifies the caller exactly once per new version.
//
// The package is UI-free so it can be tested with a plain httptest server.
// Every external dependency (server URL, HTTP client, interval, timeout,
// logger) is injected through Options, which doubles as the test seam — there
// are no package-level tunables.
package updates

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	updates "github.com/skobkin/go4updates"
	"github.com/skobkin/go4updates/source/forgejo"
	"github.com/skobkin/go4updates/watch"
)

const (
	// ServerURL is the Forgejo instance hosting the application releases.
	ServerURL = "https://git.skobk.in"
	// Repository is the owner/name path of the application repository.
	Repository = "skobkin/aimon"
	// DefaultInterval is the period between automatic checks.
	DefaultInterval = 12 * time.Hour
	// DefaultTimeout bounds every single check attempt.
	DefaultTimeout = 15 * time.Second
	// releaseLimit caps how many releases are fetched per check.
	releaseLimit = 10
)

// Options configures a Manager. Zero values resolve to the documented
// defaults.
type Options struct {
	// ServerURL overrides the Forgejo instance (tests, proxies).
	ServerURL string
	// Repository overrides the owner/name repository path (tests).
	Repository string
	// APIURL overrides the derived "{server}/api/v1" API base (tests,
	// API proxies).
	APIURL string
	// CurrentVersion is the running build's version string. Values the semver
	// comparator cannot parse (e.g. "dev") yield StatusUnknown, never a false
	// update prompt.
	CurrentVersion string
	// Interval is the period between automatic checks; <=0 uses
	// DefaultInterval.
	Interval time.Duration
	// Timeout bounds every check attempt, automatic and manual; <=0 uses
	// DefaultTimeout.
	Timeout time.Duration
	// HTTPClient performs release feed requests; nil uses a client with a
	// 30 s timeout, following the app's bounded-request idiom.
	HTTPClient *http.Client
	// Logger receives check failures and notifications; nil uses
	// slog.Default().
	Logger *slog.Logger
	// OnUpdateAvailable is called from a Manager goroutine the first time a
	// given version is seen with StatusUpdateAvailable. It receives a copy of
	// the result and must not call Stop (it runs on the goroutine Stop waits
	// for); marshal any UI work onto the UI thread instead.
	OnUpdateAvailable func(updates.Result)
}

// Manager owns the update source, the checker, and the periodic watcher
// lifecycle. It is safe for concurrent use.
type Manager struct {
	source         updates.Source
	checker        *updates.Checker
	currentVersion string
	interval       time.Duration
	timeout        time.Duration
	handler        func(updates.Result)

	// logger receives check failures and notifications. It is immutable for
	// the Manager's lifetime: the application never reconfigures logging.
	logger *slog.Logger

	// mu guards the watcher generation and the dedupe state.
	mu sync.Mutex
	// watcher is the current generation. watch.Watcher is single-use, so
	// every Start builds a fresh one; nil when stopped.
	watcher *watch.Watcher
	cancel  context.CancelFunc
	// done is closed when the generation's goroutines have exited.
	done    chan struct{}
	running bool
	// notifiedVersion is the last version passed to the handler or claimed
	// by CheckNow; it deduplicates notifications per process.
	notifiedVersion string
}

// New builds a Manager. It performs no network I/O and starts no goroutines.
func New(opts Options) (*Manager, error) {
	serverURL := opts.ServerURL
	if serverURL == "" {
		serverURL = ServerURL
	}
	repository := opts.Repository
	if repository == "" {
		repository = Repository
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	sourceOpts := []forgejo.Option{forgejo.WithHTTPClient(client)}
	if opts.APIURL != "" {
		sourceOpts = append(sourceOpts, forgejo.WithAPIURL(opts.APIURL))
	}
	source, err := forgejo.New(serverURL, repository, sourceOpts...)
	if err != nil {
		return nil, fmt.Errorf("configure update source: %w", err)
	}

	return &Manager{
		logger:         logger,
		source:         source,
		checker:        updates.NewChecker(updates.CheckerOptions{}),
		currentVersion: opts.CurrentVersion,
		interval:       interval,
		timeout:        timeout,
		handler:        opts.OnUpdateAvailable,
	}, nil
}

// Start begins periodic checks: an immediate first check, then one every
// interval. It is idempotent while running. The watcher is single-use, so a
// fresh generation is built every time.
func (m *Manager) Start() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()

		return
	}
	w := watch.New(m.checker, watch.Options{Interval: m.interval, Timeout: m.timeout})
	if err := w.Add(Repository, m.target()); err != nil {
		// Unreachable with constant inputs; refuse to run a broken generation.
		m.logger.Error("register update target", "error", err)
		m.mu.Unlock()

		return
	}
	events, unsubscribe := w.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.watcher, m.cancel, m.done, m.running = w, cancel, done, true
	m.mu.Unlock()

	// The startup line is the only launch-time evidence that checks are on;
	// a completed check with nothing newer found logs at debug only.
	m.logger.Info("periodic update checks started", "interval", m.interval)
	go m.runWatcher(ctx, w, events, unsubscribe, done)
}

// runWatcher owns one watcher generation: it drains the (unbuffered) event
// channel until the watcher stops, then releases the subscription and
// signals teardown.
func (m *Manager) runWatcher(
	ctx context.Context,
	w *watch.Watcher,
	events <-chan watch.Event,
	unsubscribe func(),
	done chan struct{},
) {
	defer close(done)
	defer unsubscribe()
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = w.Run(ctx)
	}()
	for ev := range events {
		m.handleEvent(ev)
	}
	<-runDone
}

// Stop cancels periodic checks and waits for full teardown. It is idempotent
// and safe to call on a never-started Manager.
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()

		return
	}
	cancel, done := m.cancel, m.done
	m.watcher, m.cancel, m.done, m.running = nil, nil, nil, false
	m.mu.Unlock()

	cancel()
	<-done
}

// Running reports whether periodic checks are active.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.running
}

// CheckNow performs a single check independent of Start. When it finds an
// update, the version is marked notified so the automatic path does not
// prompt for it again.
func (m *Manager) CheckNow(ctx context.Context) (updates.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	result, err := m.checker.Check(ctx, m.target())
	if err != nil {
		return updates.Result{}, fmt.Errorf("check for updates: %w", err)
	}
	if result.Status == updates.StatusUpdateAvailable && result.Latest != nil {
		m.claimVersion(result.Latest.Version)
	}

	return result, nil
}

// target builds the check target: stable releases only, most recent first.
func (m *Manager) target() updates.Target {
	return updates.Target{
		CurrentVersion: m.currentVersion,
		Source:         m.source,
		Fetch:          updates.FetchOptions{Limit: releaseLimit},
	}
}

// handleEvent processes one watcher event: failures log at warn, every
// completed check logs its outcome at debug, and an available update is
// surfaced at most once per version.
func (m *Manager) handleEvent(ev watch.Event) {
	if ev.State.LastError != nil {
		m.logger.Warn("automatic update check failed", "error", ev.State.LastError)

		return
	}
	result := ev.State.Result
	if result == nil {
		m.logger.Debug("automatic update check completed", "status", updates.StatusUnknown.String())

		return
	}
	m.logger.Debug("automatic update check completed", "status", result.Status.String())
	if result.Status != updates.StatusUpdateAvailable || result.Latest == nil {
		return
	}
	if !m.claimVersion(result.Latest.Version) {
		return
	}
	m.logger.Info("update available",
		"version", result.Latest.Version,
		"current", result.CurrentVersion,
		"url", result.Latest.URL)
	if m.handler != nil {
		m.handler(*result)
	}
}

// claimVersion records version as notified and reports whether this call is
// the first for it.
func (m *Manager) claimVersion(version string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.notifiedVersion == version {
		return false
	}
	m.notifiedVersion = version

	return true
}
