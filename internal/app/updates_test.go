package app

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	updates "github.com/skobkin/go4updates"

	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
	"git.skobk.in/skobkin/aimonitor/internal/config"
)

// memPreferences is a minimal map-backed fake of the preference surface the
// config repository depends on; it never touches a real preference store.
type memPreferences struct {
	data map[string]any
}

func (p *memPreferences) Bool(key string) bool {
	v, _ := p.data[key].(bool)

	return v
}

func (p *memPreferences) String(key string) string {
	v, _ := p.data[key].(string)

	return v
}

func (p *memPreferences) SetBool(key string, value bool) {
	p.data[key] = value
}

func (p *memPreferences) SetString(key string, value string) {
	p.data[key] = value
}

func (p *memPreferences) RemoveValue(key string) {
	delete(p.data, key)
}

// stubUpdateChecker records lifecycle calls and answers CheckNow from
// scripted values.
type stubUpdateChecker struct {
	mu       sync.Mutex
	starts   int
	stops    int
	running  bool
	result   updates.Result
	checkErr error
}

func (s *stubUpdateChecker) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.running
}

func (s *stubUpdateChecker) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	s.running = true
}

func (s *stubUpdateChecker) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stops++
	s.running = false
}

func (s *stubUpdateChecker) CheckNow(context.Context) (updates.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.result, s.checkErr
}

// setVersion temporarily replaces buildinfo.Version (release builds stamp it
// at link time; tests stamp it here).
func setVersion(t *testing.T, version string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = version
	t.Cleanup(func() { buildinfo.Version = old })
}

func newUpdatesTestApp(t *testing.T) (*App, *stubUpdateChecker) {
	t.Helper()
	a := &App{
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo: config.NewRepository(&memPreferences{data: map[string]any{}}),
	}
	stub := &stubUpdateChecker{}
	a.updateChecker = stub

	return a, stub
}

func TestUpdateChecksEnabled(t *testing.T) {
	cases := []struct {
		name      string
		automatic bool
		version   string
		want      bool
	}{
		{"dev build never checks", true, buildinfo.DevVersion, false},
		{"dev build even when disabled", false, buildinfo.DevVersion, false},
		{"release build follows enabled preference", true, "0.6.2", true},
		{"release build follows disabled preference", false, "0.6.2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateChecksEnabled(tc.automatic, tc.version); got != tc.want {
				t.Errorf("updateChecksEnabled(%v, %q) = %v, want %v", tc.automatic, tc.version, got, tc.want)
			}
		})
	}
}

// Toggling the preference persists it and starts/stops periodic checks only
// on an actual change.
func TestSetCheckUpdatesAutomaticallyStartsAndStops(t *testing.T) {
	setVersion(t, "0.6.2")
	a, stub := newUpdatesTestApp(t)

	a.SetCheckUpdatesAutomatically(true)
	if stub.starts != 1 || !stub.Running() {
		t.Errorf("enabling: starts = %d, running = %v, want 1 and true", stub.starts, stub.Running())
	}
	if !a.repo.CheckUpdatesAutomatically() {
		t.Error("enabling must persist the preference")
	}

	a.SetCheckUpdatesAutomatically(true)
	if stub.starts != 1 {
		t.Errorf("re-enabling a running checker restarted it: %d starts", stub.starts)
	}

	a.SetCheckUpdatesAutomatically(false)
	if stub.stops != 1 || stub.Running() {
		t.Errorf("disabling: stops = %d, running = %v, want 1 and false", stub.stops, stub.Running())
	}
	if a.repo.CheckUpdatesAutomatically() {
		t.Error("disabling must persist the preference")
	}

	a.SetCheckUpdatesAutomatically(false)
	if stub.stops != 1 {
		t.Errorf("re-disabling a stopped checker repeated the stop: %d stops", stub.stops)
	}
}

// A development build persists the preference but never starts periodic
// checks.
func TestSetCheckUpdatesAutomaticallySkipsStartOnDevBuild(t *testing.T) {
	setVersion(t, buildinfo.DevVersion)
	a, stub := newUpdatesTestApp(t)

	a.SetCheckUpdatesAutomatically(true)
	if !a.repo.CheckUpdatesAutomatically() {
		t.Error("the preference must persist on dev builds")
	}
	if stub.starts != 0 {
		t.Errorf("dev build started checks %d times, want 0", stub.starts)
	}
}

func TestStartUpdateChecksSkippedOnDevBuildOrDisabledPreference(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		automatic bool
	}{
		{"dev build", buildinfo.DevVersion, true},
		{"disabled preference", "0.6.2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setVersion(t, tc.version)
			a, stub := newUpdatesTestApp(t)
			a.repo.SetCheckUpdatesAutomatically(tc.automatic)

			a.startUpdateChecks()
			if stub.starts != 0 {
				t.Errorf("started checks %d times, want 0", stub.starts)
			}
		})
	}
}

// An enabled release build starts checks, and the skip case is explained in
// the log — at the default level that line is the only evidence that update
// checking exists.
func TestStartUpdateChecksStartsAndLogs(t *testing.T) {
	setVersion(t, "0.6.2")
	a, stub := newUpdatesTestApp(t)
	a.repo.SetCheckUpdatesAutomatically(true)

	var buf bytes.Buffer
	a.log = slog.New(slog.NewTextHandler(&buf, nil))

	a.startUpdateChecks()
	if stub.starts != 1 {
		t.Errorf("starts = %d, want 1", stub.starts)
	}
	if strings.Contains(buf.String(), "automatic update checks disabled") {
		t.Errorf("the enabled path must not log the skip line: %q", buf.String())
	}
}

// Every entry point must tolerate a nil checker (construction failed): the
// app runs without update checking, nothing panics.
func TestUpdateEntryPointsTolerateNilChecker(t *testing.T) {
	setVersion(t, "0.6.2")
	a := &App{
		log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo: config.NewRepository(&memPreferences{data: map[string]any{}}),
	}

	a.startUpdateChecks()
	a.SetCheckUpdatesAutomatically(true)
	if !a.repo.CheckUpdatesAutomatically() {
		t.Error("the preference must persist even without a checker")
	}
}
