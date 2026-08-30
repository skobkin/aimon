package codex

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// --- launcher discovery -------------------------------------------------

func TestDiscoverExplicitPaths(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "no-such-codex")
	dir := t.TempDir()

	cases := []struct {
		name  string
		exe   string
		want  DiscoveryState
		found string
	}{
		{"existing executable", exe, DiscoveryFound, exe},
		{"missing path", missing, DiscoveryExplicitMissing, ""},
		{"directory", dir, DiscoveryNotExecutable, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Discover(config.CodexSettings{Executable: tc.exe})
			if got.State != tc.want {
				t.Fatalf("state = %q, want %q", got.State, tc.want)
			}
			if got.Path != tc.found {
				t.Fatalf("path = %q, want %q", got.Path, tc.found)
			}
			if got.OK() != (tc.want == DiscoveryFound) {
				t.Fatalf("OK() = %v", got.OK())
			}
		})
	}
}

func TestDiscoverBareNameUsesEffectivePATH(t *testing.T) {
	bin := t.TempDir()
	launcher := filepath.Join(bin, "codex-fake")
	writeExecutable(t, launcher, "#!/bin/sh\nexit 0\n")

	// The override PATH is the only place the name can come from, so a hit
	// proves the effective environment was searched.
	got := Discover(config.CodexSettings{
		Executable: "codex-fake",
		Env:        []config.EnvOverride{{Name: "PATH", Value: bin}},
	})
	if !got.OK() || got.Path != launcher {
		t.Fatalf("discovery = %+v, want %q", got, launcher)
	}

	empty := t.TempDir()
	missing := Discover(config.CodexSettings{
		Executable: "codex-fake",
		Env:        []config.EnvOverride{{Name: "PATH", Value: empty}},
	})
	if missing.State != DiscoveryNotFoundInPath {
		t.Fatalf("state = %q, want %q", missing.State, DiscoveryNotFoundInPath)
	}

	// A PATH override wins over the inherited one, whatever it holds.
	other := t.TempDir()
	writeExecutable(t, filepath.Join(other, "codex-fake"), "#!/bin/sh\nexit 0\n")
	two := Discover(config.CodexSettings{
		Executable: "codex-fake",
		Env:        []config.EnvOverride{{Name: "PATH", Value: strings.Join([]string{bin, other}, string(filepath.ListSeparator))}},
	})
	if two.Path != launcher {
		t.Fatalf("path = %q, want the first override dir %q", two.Path, launcher)
	}
}

// A relative PATH entry (".", "./", "bin") must not produce a bare or
// relative name for the exec layer: Join's cleaning turns "./codex-fake"
// into "codex-fake", which exec would look up again against the inherited
// PATH instead of the effective one. The resolved path is absolute.
func TestDiscoverRelativePATHEntryResolvesAbsolutely(t *testing.T) {
	dir := t.TempDir()
	launcher := filepath.Join(dir, "codex-fake")
	writeExecutable(t, launcher, "#!/bin/sh\nexit 0\n")
	t.Chdir(dir)

	got := Discover(config.CodexSettings{
		Executable: "codex-fake",
		Env:        []config.EnvOverride{{Name: "PATH", Value: "."}},
	})
	if !got.OK() || got.Path != launcher {
		t.Fatalf("discovery = %+v, want absolute %q", got, launcher)
	}
}

// The full launch path for a relative PATH entry: the refresh must exec the
// launcher discovery found (by absolute path) and never re-look the bare
// name up against the inherited PATH, which does not contain it here.
func TestRefreshResolvesBareNameThroughEffectivePATHOnly(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	launcher := filepath.Join(dir, "codex-fake")
	if err := os.Link(exe, launcher); err != nil {
		t.Skipf("hardlink of the test binary: %v", err)
	}
	t.Chdir(dir)

	settings := config.CodexSettings{
		Executable: "codex-fake",
		Env: []config.EnvOverride{
			{Name: fakeChildEnv, Value: "ok"},
			{Name: fakeDirEnv, Value: t.TempDir()},
			{Name: "PATH", Value: "."},
		},
	}
	adapter := New(settings, quietLoggerForTest(t), "v")
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
}

// --- snapshot normalization ---------------------------------------------
func TestBuildSnapshotNormalization(t *testing.T) {
	now := time.Unix(1756500000, 0)

	t.Run("by-id map preferred over top level", func(t *testing.T) {
		result := &rateLimitsReadResult{
			RateLimits: &rateLimitSnapshot{Primary: fakeWindow(10.5, 300, true)},
			RateLimitsByID: map[string]*rateLimitSnapshot{
				"codex": {Primary: fakeWindow(42.5, 300, true), Secondary: fakeWindow(7.25, 60, false), PlanType: strPtr(" plus ")},
			},
			ResetCredits: &resetCreditsSummary{AvailableCount: int64Ptr(2)},
		}
		snap, err := buildSnapshot(result, now)
		if err != nil {
			t.Fatalf("buildSnapshot: %v", err)
		}
		if len(snap.Windows) != 2 {
			t.Fatalf("windows = %d, want 2", len(snap.Windows))
		}
		first := snap.Windows[0]
		if first.ID != windowIDPrimary || first.Name != "5-hour" {
			t.Fatalf("first window = %+v", first)
		}
		if first.UsedFraction != 0.425 || first.RemainingFraction != 0.575 {
			t.Fatalf("fractions = %v/%v, want 0.425/0.575", first.UsedFraction, first.RemainingFraction)
		}
		if first.ResetAt == nil {
			t.Fatal("primary reset time lost")
		}
		second := snap.Windows[1]
		if second.ID != windowIDSecondary || second.Name != "1-hour" || second.UsedFraction != 0.0725 {
			t.Fatalf("second window = %+v", second)
		}
		if second.ResetAt != nil {
			t.Fatal("absent reset time must stay absent")
		}
		if snap.PlanType != "plus" {
			t.Fatalf("plan type = %q, want trimmed \"plus\"", snap.PlanType)
		}
		if snap.ResetCreditsAvailable == nil || *snap.ResetCreditsAvailable != 2 {
			t.Fatalf("reset credits = %v, want 2", snap.ResetCreditsAvailable)
		}
		if snap.CollectedAt != now {
			t.Fatalf("collected at = %v, want %v", snap.CollectedAt, now)
		}
	})

	t.Run("top level fallback and absent metadata", func(t *testing.T) {
		result := &rateLimitsReadResult{
			RateLimits: &rateLimitSnapshot{Primary: fakeWindow(30, 0, false)},
		}
		snap, err := buildSnapshot(result, now)
		if err != nil {
			t.Fatalf("buildSnapshot: %v", err)
		}
		if len(snap.Windows) != 1 {
			t.Fatalf("windows = %d, want 1", len(snap.Windows))
		}
		if snap.Windows[0].Name != "Primary" {
			t.Fatalf("name = %q, want positional fallback", snap.Windows[0].Name)
		}
		if snap.PlanType != "" || snap.ResetCreditsAvailable != nil {
			t.Fatalf("absent metadata became non-empty: %+v", snap)
		}
	})

	t.Run("secondary falls back to its own name", func(t *testing.T) {
		result := &rateLimitsReadResult{
			RateLimits: &rateLimitSnapshot{Secondary: fakeWindow(55, 10080, true)},
		}
		snap, err := buildSnapshot(result, now)
		if err != nil {
			t.Fatalf("buildSnapshot: %v", err)
		}
		if len(snap.Windows) != 1 || snap.Windows[0].Name != "Weekly" {
			t.Fatalf("windows = %+v, want one Weekly window", snap.Windows)
		}
	})

	t.Run("boundary drift clamped, garbage windows skipped", func(t *testing.T) {
		result := &rateLimitsReadResult{
			RateLimits: &rateLimitSnapshot{
				Primary:   fakeWindow(150, 300, true), // materially out of range
				Secondary: fakeWindow(-0.4, 60, false),
			},
		}
		snap, err := buildSnapshot(result, now)
		if err != nil {
			t.Fatalf("buildSnapshot: %v", err)
		}
		if len(snap.Windows) != 1 || snap.Windows[0].UsedFraction != 0 {
			t.Fatalf("windows = %+v, want one clamped zero window", snap.Windows)
		}

		over := fakeWindow(100.4, 60, false)
		w, err := buildWindow(over, windowIDSecondary, "Secondary")
		if err != nil || w.UsedFraction != 1 {
			t.Fatalf("over-window = %+v err %v, want used 1", w, err)
		}
	})

	t.Run("no usable window is an invalid response", func(t *testing.T) {
		cases := map[string]*rateLimitsReadResult{
			"empty":          {},
			"only nil parts": {RateLimits: &rateLimitSnapshot{}},
			"no percent":     {RateLimits: &rateLimitSnapshot{Primary: fakeWindow(0, 300, false)}},
		}
		cases["no percent"].RateLimits.Primary.UsedPercent = nil
		for name, result := range cases {
			if _, err := buildSnapshot(result, now); !errors.Is(err, errInvalidResponse) {
				t.Fatalf("%s: err = %v, want errInvalidResponse", name, err)
			}
		}
	})

	t.Run("generic duration labels", func(t *testing.T) {
		cases := map[int64]string{
			120:  "2h",
			2880: "2d",
			45:   "45m",
		}
		for mins, want := range cases {
			w, err := buildWindow(fakeWindow(50, mins, false), windowIDPrimary, "Primary")
			if err != nil {
				t.Fatalf("%d: %v", mins, err)
			}
			if w.Name != want {
				t.Fatalf("%d minutes named %q, want %q", mins, w.Name, want)
			}
		}
	})
}

// --- error classification ------------------------------------------------

func TestClassifySpawnError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		category source.Category
		detail   string
	}{
		{
			name:     "not found in path",
			err:      &spawnError{state: DiscoveryNotFoundInPath},
			category: source.CatConfig,
			detail:   "Codex CLI not found in PATH. Install Codex CLI or choose its executable/wrapper.",
		},
		{
			name:     "explicit missing",
			err:      &spawnError{state: DiscoveryExplicitMissing},
			category: source.CatConfig,
			detail:   "Codex executable not found or not executable.",
		},
		{
			name:     "not executable",
			err:      &spawnError{state: DiscoveryNotExecutable},
			category: source.CatConfig,
			detail:   "Codex executable not found or not executable.",
		},
		{
			name:     "start failure",
			err:      &spawnError{err: errors.New("fork/exec failed")},
			category: source.CatConfig,
			detail:   "Codex CLI could not be started",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			classified := classifySpawnError(tc.err)
			se := classifyOf(t, classified)
			if se.Category != tc.category {
				t.Fatalf("category = %q, want %q", se.Category, tc.category)
			}
			if se.Detail != tc.detail {
				t.Fatalf("detail = %q, want %q", se.Detail, tc.detail)
			}
			// Setup messages are complete on their own: they never leak the
			// underlying OS error text into the UI.
			if tc.err != nil && errors.Is(tc.err, os.ErrNotExist) {
				t.Fatal("unexpected sentinel")
			}
		})
	}
}

func TestClassifyStageError(t *testing.T) {
	t.Run("auth-shaped rpc error", func(t *testing.T) {
		se := classifyOf(t, classifyStageError("account/rateLimits/read", &rpcError{Code: -32000, Message: "unauthorized: log in again"}))
		if se.Category != source.CatAuth {
			t.Fatalf("category = %q, want auth", se.Category)
		}
	})
	t.Run("other rpc error is a backend rejection", func(t *testing.T) {
		se := classifyOf(t, classifyStageError("account/rateLimits/read", &rpcError{Code: -32000, Message: "backend exploded"}))
		if se.Category != source.CatBackend {
			t.Fatalf("category = %q, want backend", se.Category)
		}
		// The JSON-RPC surface has no HTTP status: the fixed message must
		// not claim one.
		if msg := se.UserMessage(); strings.Contains(msg, "HTTP") {
			t.Fatalf("user message = %q, want HTTP-free wording", msg)
		}
	})
	t.Run("method not found is a config problem and stays recognizable", func(t *testing.T) {
		orig := &rpcError{Code: rpcMethodNotFound, Message: "method not found"}
		classified := classifyStageError("initialize", orig)
		se := classifyOf(t, classified)
		if se.Category != source.CatConfig {
			t.Fatalf("category = %q, want config", se.Category)
		}
		if !isMethodNotFound(classified) {
			t.Fatal("classified error lost the json-rpc method-not-found marker")
		}
	})
	t.Run("deadlines, cancellation, malformed and broken streams", func(t *testing.T) {
		cases := []struct {
			name     string
			stage    string
			err      error
			category source.Category
		}{
			{"deadline", methodRateLimits, context.DeadlineExceeded, source.CatTimeout},
			{"cancelled", methodRateLimits, context.Canceled, source.CatConnection},
			{"malformed", methodInitialize, malformedJSON(errors.New("unexpected token")), source.CatInvalidResponse},
			{"write", methodRateLimits, writeError{err: errors.New("broken pipe")}, source.CatConnection},
			{"eof during read", methodRateLimits, errors.New("EOF"), source.CatConnection},
			// A stream that dies during the handshake never came up: the
			// incompatible-CLI setup message replaces the generic one.
			{"eof during initialize", methodInitialize, errors.New("EOF"), source.CatConfig},
			{"write during initialize", methodInitialize, writeError{err: errors.New("broken pipe")}, source.CatConfig},
		}
		for _, tc := range cases {
			se := classifyOf(t, classifyStageError(tc.stage, tc.err))
			if se.Category != tc.category {
				t.Fatalf("%s: category = %q, want %q", tc.name, se.Category, tc.category)
			}
		}
	})
}

// --- refresh flows against the fake child -------------------------------

// refreshOnce spawns the harness adapter and performs one refresh,
// returning the snapshot/error pair and the harness for assertions.
func refreshOnce(t *testing.T, script string, extra ...config.EnvOverride) (domain.Snapshot, *fakeHarness, error) {
	t.Helper()
	h := newFakeHarness(t, script, extra...)
	log, _ := quietLogger()
	adapter := New(h.settings, log, "test-version")
	snap, err := adapter.Refresh(context.Background())

	return snap, h, err
}

func classifyOf(t *testing.T, err error) *source.Error {
	t.Helper()
	var se *source.Error
	if !errors.As(err, &se) {
		t.Fatalf("error is not a *source.Error: %v", err)
	}

	return se
}

func wantRefreshError(t *testing.T, err error, want source.Category, detailPart string) {
	t.Helper()
	if err == nil {
		t.Fatal("refresh succeeded, want error")
	}
	se := classifyOf(t, err)
	if se.Category != want {
		t.Fatalf("category = %q, want %q (detail %q)", se.Category, want, se.Detail)
	}
	if detailPart != "" && !strings.Contains(se.Detail, detailPart) {
		t.Fatalf("detail = %q, want it to contain %q", se.Detail, detailPart)
	}
}

func TestAdapterKindAndTimeout(t *testing.T) {
	adapter := New(config.CodexSettings{}, quietLoggerForTest(t), "v")
	if adapter.Kind() != domain.KindCodex {
		t.Fatalf("kind = %q", adapter.Kind())
	}
	if adapter.RequestTimeout() != requestTimeout {
		t.Fatalf("request timeout = %v", adapter.RequestTimeout())
	}
}

func TestRefreshOnDemandHappyPath(t *testing.T) {
	for _, script := range []string{"ok", "notify", "stale-id", "string-id"} {
		t.Run(script, func(t *testing.T) {
			snap, h, err := refreshOnce(t, script)
			if err != nil {
				t.Fatalf("refresh: %v", err)
			}
			codexSnap, ok := snap.(domain.CodexSnapshot)
			if !ok {
				t.Fatalf("snapshot type = %T", snap)
			}
			if snap.SnapshotKind() != domain.KindCodex {
				t.Fatalf("snapshot kind = %q", snap.SnapshotKind())
			}
			if len(codexSnap.Windows) != 2 {
				t.Fatalf("windows = %d, want 2", len(codexSnap.Windows))
			}
			if codexSnap.PlanType != "plus" || codexSnap.ResetCreditsAvailable == nil || *codexSnap.ResetCreditsAvailable != 2 {
				t.Fatalf("snapshot metadata = %+v", codexSnap)
			}

			// The child saw exactly the protocol AI Monitor is allowed to
			// speak, in order, with the fixed argv and the env overrides.
			h.awaitLines(t, "read", 1)
			methods := prefixed(h.logLines(), "method ")
			want := []string{"method " + methodInitialize, "method " + methodInitialized, "method " + methodRateLimits}
			if len(methods) != len(want) {
				t.Fatalf("methods = %v, want exactly %v", methods, want)
			}
			for i, w := range want {
				if methods[i] != w {
					t.Fatalf("methods = %v, want %v", methods, want)
				}
			}
			args := prefixed(h.logLines(), "args ")
			if len(args) != 1 || args[0] != "args app-server --stdio" {
				t.Fatalf("child argv recorded as %v", args)
			}
			envLines := prefixed(h.logLines(), "env ")
			if len(envLines) != 1 || !strings.Contains(envLines[0], "override-marker") {
				t.Fatalf("env overrides did not reach the child: %v", envLines)
			}
			if h.countLines("spawn") != 1 {
				t.Fatalf("spawn count = %d, want 1", h.countLines("spawn"))
			}
		})
	}
}

func TestRefreshSingleWindow(t *testing.T) {
	snap, _, err := refreshOnce(t, "ok-single")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	windows := snap.(domain.CodexSnapshot).Windows
	if len(windows) != 1 || windows[0].UsedFraction != 0.64 {
		t.Fatalf("windows = %+v, want one 64%% window", windows)
	}
}

func TestRefreshSpawnFailuresAreConfigErrors(t *testing.T) {
	h := newFakeHarness(t, "ok")
	h.settings.Executable = filepath.Join(t.TempDir(), "no-such-codex")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	_, err := adapter.Refresh(context.Background())
	wantRefreshError(t, err, source.CatConfig, "not found or not executable")
	if h.countLines("spawn") != 0 {
		t.Fatal("a failed discovery must not spawn anything")
	}
}

func TestRefreshProtocolFailures(t *testing.T) {
	cases := []struct {
		script   string
		category source.Category
		detail   string
	}{
		{"init-error-32601", source.CatConfig, "does not support"},
		{"read-error-auth", source.CatAuth, "authentication failed"},
		{"read-error-provider", source.CatBackend, "rejected"},
		{"malformed", source.CatInvalidResponse, "malformed"},
		// A child that dies before answering the handshake is reported as
		// the incompatible-CLI setup problem, not as a generic connection
		// failure.
		{"eof", source.CatConfig, "does not support"},
	}
	for _, tc := range cases {
		t.Run(tc.script, func(t *testing.T) {
			_, _, err := refreshOnce(t, tc.script)
			wantRefreshError(t, err, tc.category, tc.detail)
		})
	}
}

func TestRefreshReadTimeout(t *testing.T) {
	h := newFakeHarness(t, "silent")
	log, _ := quietLogger()
	adapter := New(h.settings, log, "v")
	adapter.readTimeout = 250 * time.Millisecond
	_, err := adapter.Refresh(context.Background())
	wantRefreshError(t, err, source.CatTimeout, "timed out")
}

func TestRefreshCancelledBeforeSpawn(t *testing.T) {
	h := newFakeHarness(t, "ok")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snap, err := adapter.Refresh(ctx)
	if snap != nil {
		t.Fatal("cancelled refresh returned a snapshot")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if h.countLines("spawn") != 0 {
		t.Fatal("a cancelled refresh must not spawn a process")
	}
}

// --- persistent mode ------------------------------------------------------

func TestPersistentModeReusesConnection(t *testing.T) {
	h := newFakeHarness(t, "ok")
	h.settings.Mode = config.CodexModePersistent
	log, _ := quietLogger()
	adapter := New(h.settings, log, "v")
	for i := 0; i < 2; i++ {
		if _, err := adapter.Refresh(context.Background()); err != nil {
			t.Fatalf("refresh %d: %v", i+1, err)
		}
	}
	h.awaitLines(t, "second read", 2)
	if got := h.countLines("spawn"); got != 1 {
		t.Fatalf("spawn count = %d, want exactly 1", got)
	}
}

func TestPersistentModeRestartsAfterBrokenStream(t *testing.T) {
	h := newFakeHarness(t, "ok-then-die")
	h.settings.Mode = config.CodexModePersistent
	log, _ := quietLogger()
	adapter := New(h.settings, log, "v")
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if _, err := adapter.Refresh(context.Background()); err == nil {
		t.Fatal("second refresh succeeded on a dead stream")
	}
	// The failed refresh tore the broken connection down; the next one
	// starts clean instead of reusing a dead or id-corrupted stream.
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("third refresh after teardown: %v", err)
	}
	if !waitUntil(func() bool { return h.countLines("spawn") == 2 }) {
		t.Fatalf("spawn count = %d, want 2", h.countLines("spawn"))
	}
}

// --- Settings connection test --------------------------------------------

func TestAdapterTestOutcomeSuccess(t *testing.T) {
	h := newFakeHarness(t, "ok")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	adapter.settings.Mode = config.CodexModePersistent // a test probes on-demand anyway
	out, err := adapter.Test(context.Background())
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !out.ChatGPTOK || out.CLIIncompatible || out.CLIState != DiscoveryFound {
		t.Fatalf("outcome = %+v", out)
	}
	if out.PlanType != "plus" || out.Windows != 2 {
		t.Fatalf("outcome summary = %+v", out)
	}
	// A test never persists a process, regardless of the saved mode.
	if got := h.countLines("spawn"); got != 1 {
		t.Fatalf("spawn count = %d, want 1", got)
	}
}

func TestAdapterTestOutcomeMissingCLI(t *testing.T) {
	h := newFakeHarness(t, "ok")
	h.settings.Executable = filepath.Join(t.TempDir(), "no-such-codex")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	out, err := adapter.Test(context.Background())
	if err == nil {
		t.Fatal("test succeeded without a CLI")
	}
	if out.ChatGPTOK || out.CLIState != DiscoveryExplicitMissing {
		t.Fatalf("outcome = %+v", out)
	}
	se := classifyOf(t, err)
	if se.Category != source.CatConfig || se.Detail != "Codex executable not found or not executable." {
		t.Fatalf("classified = %+v", se)
	}
}

func TestAdapterTestOutcomeIncompatibleCLI(t *testing.T) {
	h := newFakeHarness(t, "init-error-32601")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	out, err := adapter.Test(context.Background())
	if err == nil {
		t.Fatal("test succeeded on an incompatible CLI")
	}
	if !out.CLIIncompatible || out.ChatGPTOK {
		t.Fatalf("outcome = %+v", out)
	}
	wantRefreshError(t, err, source.CatConfig, "does not support")
}

// An older CLI that resolves fine but exits before answering the handshake
// must produce the incompatible-CLI outcome — "Codex CLI: OK" followed by a
// connection error would hide the actual problem from the user.
func TestAdapterTestOutcomePreInitializeExit(t *testing.T) {
	h := newFakeHarness(t, "eof")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	out, err := adapter.Test(context.Background())
	if err == nil {
		t.Fatal("test succeeded on a child that died before the handshake")
	}
	if !out.CLIIncompatible || out.ChatGPTOK || out.CLIState != DiscoveryFound {
		t.Fatalf("outcome = %+v, want the incompatible-CLI outcome", out)
	}
	wantRefreshError(t, err, source.CatConfig, "does not support")
}

func TestAdapterTestHonoursCallerDeadline(t *testing.T) {
	h := newFakeHarness(t, "silent")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	// The caller owns the budget; the probe may only shorten it.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, err := adapter.Test(ctx)
	if err == nil {
		t.Fatalf("test returned success on a silent child: %+v", out)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("test ignored the caller's deadline (took %v)", elapsed)
	}
	if h.countLines("spawn") != 1 {
		t.Fatalf("spawn count = %d, want 1", h.countLines("spawn"))
	}
}

func TestAdapterTestCancelledUpfront(t *testing.T) {
	h := newFakeHarness(t, "ok")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Test(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if h.countLines("spawn") != 0 {
		t.Fatal("a cancelled test must not spawn a process")
	}
}

// --- privacy --------------------------------------------------------------

func TestEnvValuesAndStderrNeverLeak(t *testing.T) {
	const secret = "s3cr3t-OVERRIDE-42"

	t.Run("failing refresh", func(t *testing.T) {
		h := newFakeHarness(t, "eof", config.EnvOverride{Name: "CODEX_EXTRA_SECRET", Value: secret})
		log, buf := quietLogger()
		adapter := New(h.settings, log, "v")
		_, err := adapter.Refresh(context.Background())
		if err == nil {
			t.Fatal("refresh unexpectedly succeeded")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(buf.String(), secret) {
			t.Fatal("the env override value leaked into the error or the log")
		}
	})

	t.Run("child stderr is never surfaced", func(t *testing.T) {
		h := newFakeHarness(t, "stderr-blast")
		log, buf := quietLogger()
		adapter := New(h.settings, log, "v")
		if _, err := adapter.Refresh(context.Background()); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		if strings.Contains(buf.String(), stderrMarker) {
			t.Fatal("child stderr content leaked into the log")
		}
		if got := h.countLines("spawn"); got != 1 {
			t.Fatalf("spawn count = %d, want 1", got)
		}
	})

	// A JSON-RPC error reply may carry account data in its message. The
	// message drives classification only: the rendered error (and with it
	// the manager's log line and the UI) carries the code alone.
	t.Run("backend error message is never rendered", func(t *testing.T) {
		h := newFakeHarness(t, "read-error-pii")
		log, buf := quietLogger()
		adapter := New(h.settings, log, "v")
		_, err := adapter.Refresh(context.Background())
		wantRefreshError(t, err, source.CatAuth, "authentication failed")
		for _, pii := range []string{"acct-77431", "person@example.com", "/orgs/private/path"} {
			if strings.Contains(err.Error(), pii) || strings.Contains(buf.String(), pii) {
				t.Fatalf("backend message fragment %q leaked into the error or the log", pii)
			}
		}
		if !strings.Contains(err.Error(), "json-rpc error -32000") {
			t.Fatalf("error = %q, want the code-only rendering", err.Error())
		}
	})
}

func prefixed(lines []string, prefix string) []string {
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			out = append(out, line)
		}
	}

	return out
}

// quietLoggerForTest discards the buffer: most tests only need a working
// logger, not log assertions.
func quietLoggerForTest(t *testing.T) *slog.Logger {
	t.Helper()

	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
