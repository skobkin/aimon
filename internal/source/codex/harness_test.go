package codex

// The fake-child harness re-executes this test binary as the configured
// Codex launcher. TestMain dispatches on CODEX_FAKE_CHILD before any test
// runs, so the child never reaches the test suite, and every artifact the
// child produces (spawn log, PID file) lives in the directory named by
// CODEX_FAKE_DIR. Both variables travel exclusively through the adapter's
// own env-override machinery — nothing here calls os.Setenv — which is what
// makes every lifecycle test also exercise effectiveEnv.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
)

const (
	fakeChildEnv = "CODEX_FAKE_CHILD"
	fakeDirEnv   = "CODEX_FAKE_DIR"

	logName = "fake.log"
	pidName = "fake.pid"

	// harnessWait bounds every polling wait in the harness: long enough
	// for a spawned fake child to answer a refresh many times over.
	harnessWait = 5 * time.Second

	// stderrMarker is what the "stderr-blast" script writes on stderr; the
	// privacy test asserts it never reaches AI Monitor's log or errors.
	stderrMarker = "STDERR-MARKER-child-noise"
)

func TestMain(m *testing.M) {
	if script := os.Getenv(fakeChildEnv); script != "" {
		runFakeChild(script)

		return
	}
	os.Exit(m.Run())
}

// runFakeChild is the child half of the harness: a scripted app-server
// stand-in. It records its argv, PID and every received method, answers per
// the script, and exits on stdin EOF like a real app-server does.
func runFakeChild(script string) {
	dir := os.Getenv(fakeDirEnv)
	appendLine(filepath.Join(dir, logName), "spawn "+strconv.Itoa(os.Getpid()))
	appendLine(filepath.Join(dir, logName), "args "+strings.Join(os.Args[1:], " "))
	// Proves the parent's env overrides reached the child verbatim.
	appendLine(filepath.Join(dir, logName), "env CODEX_EXTRA="+os.Getenv("CODEX_EXTRA"))
	_ = os.WriteFile(filepath.Join(dir, pidName), []byte(strconv.Itoa(os.Getpid())), 0o600) //nolint:gosec // dir comes from our own harness env, pidName is a fixed name

	if script == "stderr-blast" {
		// Noise on stderr that AI Monitor must drain without surfacing.
		fmt.Fprintln(os.Stderr, stderrMarker)
	}
	switch script {
	case "eof":
		// Dies before speaking to anyone: the caller must see a dead
		// stream, not a healthy-looking server.
		return
	case "die":
		os.Exit(3)
	}

	respond := func(res fakeResponse) {
		line, err := json.Marshal(res)
		if err != nil {
			return
		}
		fmt.Fprintln(os.Stdout, string(line))
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), lineLimit)
	readsServed := 0
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		appendLine(filepath.Join(dir, logName), "method "+req.Method)

		switch script {
		case "malformed":
			fmt.Fprintln(os.Stdout, "this is not json at all")
		case "init-error-32601":
			if req.Method == methodInitialize {
				respond(fakeResponse{ID: req.ID, Error: &rpcError{Code: rpcMethodNotFound, Message: "method not found"}})

				return
			}
		}

		switch req.Method {
		case methodInitialize:
			if script == "notify" {
				// Unsolicited notification ahead of the reply must be
				// skipped, not mistaken for it.
				fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","method":"session/clear","params":{}}`)
			}
			if script == "stale-id" {
				// A reply to some other request arrives first.
				respond(fakeResponse{ID: json.RawMessage("999"), Result: json.RawMessage("{}")})
			}
			id := req.ID
			if script == "string-id" {
				// The string id spelling is legal JSON-RPC.
				id = json.RawMessage(`"1"`)
			}
			respond(fakeResponse{ID: id, Result: json.RawMessage("{}")})
		case methodRateLimits:
			switch script {
			case "read-error-auth":
				respond(fakeResponse{ID: req.ID, Error: &rpcError{Code: -32000, Message: "unauthorized: session expired, please log in"}})

				return
			case "read-error-pii":
				// A backend message shaped like real account output: the
				// privacy test asserts none of it is ever rendered.
				respond(fakeResponse{ID: req.ID, Error: &rpcError{
					Code:    -32000,
					Message: "unauthorized for account acct-77431 (person@example.com), org /orgs/private/path",
				}})

				return
			case "read-error-provider":
				respond(fakeResponse{ID: req.ID, Error: &rpcError{Code: -32000, Message: "backend upstream unavailable"}})

				return
			case "silent":
				// No reply, ever; the caller should hit its own timeout.
			default:
				respond(fakeResponse{ID: req.ID, Result: json.RawMessage(fakeResultJSON(script, readsServed))})
				readsServed++
				if script == "ok-then-die" {
					// Served its one read; exits while the parent still
					// believes the connection is healthy.
					return
				}
			}
		}
	}

	if script == "hang-on-eof" {
		// Ignores the clean-exit signal: only a forced kill can reap it.
		time.Sleep(60 * time.Second)
	}
}

// fakeResponse mirrors one JSON-RPC reply with a flexible id spelling.
type fakeResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// fakeResultJSON builds the rate-limits payload for a script. Scripts that
// never reach the read fall back to the standard two-window body. A second
// read (ok-then-die already exited) is not special-cased: the child is gone.
func fakeResultJSON(script string, readsServed int) string {
	body := fakeRateLimits()
	// The by-ID map carries different numbers than the top-level snapshot so
	// the normalization tests can see which one won.
	body.RateLimits = &rateLimitSnapshot{Primary: fakeWindow(10.5, 300, true), Secondary: fakeWindow(2.5, 60, false)}

	switch script {
	case "ok-single":
		body.RateLimitsByID = map[string]*rateLimitSnapshot{
			"codex": {Primary: fakeWindow(64, 300, true)},
		}
	case "ok-then-die":
		if readsServed == 0 {
			body.RateLimitsByID = map[string]*rateLimitSnapshot{
				"codex": {Primary: fakeWindow(64, 300, true), Secondary: fakeWindow(12, 60, false), PlanType: strPtr("plus")},
			}
		}
	default:
		body.RateLimitsByID = map[string]*rateLimitSnapshot{
			"codex": {Primary: fakeWindow(42.5, 300, true), Secondary: fakeWindow(7.25, 60, false), PlanType: strPtr("plus")},
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "{}"
	}

	return string(encoded)
}

func fakeRateLimits() rateLimitsReadResult {
	return rateLimitsReadResult{
		ResetCredits: &resetCreditsSummary{AvailableCount: int64Ptr(2)},
	}
}

func fakeWindow(pct float64, mins int64, withReset bool) *window {
	w := &window{UsedPercent: float64Ptr(pct), WindowDurationMins: int64Ptr(mins)}
	if withReset {
		w.ResetsAt = int64Ptr(time.Now().Add(2 * time.Hour).Unix())
	}

	return w
}

func strPtr(s string) *string       { return &s }
func float64Ptr(f float64) *float64 { return &f }
func int64Ptr(i int64) *int64       { return &i }

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // test log inside the harness temp dir
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// harness helpers used by the tests.

// fakeHarness pairs one scripted child with the settings that launch it.
// The launcher is this test binary, so a child run needs no external tool.
type fakeHarness struct {
	dir      string
	settings config.CodexSettings
}

// newFakeHarness builds settings whose launcher is this test binary running
// the named script. Every harness variable — script, artifact directory and
// the CODEX_EXTRA marker the child echoes — travels as an instance env
// override, so the lifecycle tests double as coverage of effectiveEnv.
func newFakeHarness(t *testing.T, script string, extra ...config.EnvOverride) *fakeHarness {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	env := []config.EnvOverride{
		{Name: fakeChildEnv, Value: script},
		{Name: fakeDirEnv, Value: t.TempDir()},
		{Name: "CODEX_EXTRA", Value: "override-marker"},
	}
	env = append(env, extra...)

	return &fakeHarness{
		dir:      env[1].Value,
		settings: config.CodexSettings{Executable: exe, Mode: config.CodexModeOnDemand, Env: env},
	}
}

// logLines returns the child's recorded lines (empty when it never ran).
func (h *fakeHarness) logLines() []string {
	data, err := os.ReadFile(filepath.Join(h.dir, logName))
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}

	return lines
}

// countLines reports how many recorded lines start with prefix.
func (h *fakeHarness) countLines(prefix string) int {
	n := 0
	for _, line := range h.logLines() {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}

	return n
}

// awaitLines polls until the child has recorded n rate-limits requests,
// which must happen without test-side sleeps for any refresh that came
// back.
func (h *fakeHarness) awaitLines(t *testing.T, what string, n int) {
	t.Helper()
	prefix := "method " + methodRateLimits
	if !waitUntil(func() bool { return h.countLines(prefix) >= n }) {
		t.Fatalf("timed out waiting for %s (%d lines with prefix %q, log: %v)", what, h.countLines(prefix), prefix, h.logLines())
	}
}

// pid returns the child's recorded PID, polling briefly for the file: the
// child writes it before its first read, so any completed refresh implies
// it exists.
func (h *fakeHarness) pid(t *testing.T) int {
	t.Helper()
	var pid int
	ok := waitUntil(func() bool {
		data, err := os.ReadFile(filepath.Join(h.dir, pidName))
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))

		return err == nil && pid > 0
	})
	if !ok {
		t.Fatal("child never recorded its PID")
	}

	return pid
}

// waitUntil polls cond for up to harnessWait, long enough for a spawned
// fake child to answer a refresh many times over.
func waitUntil(cond func() bool) bool {
	deadline := time.Now().Add(harnessWait)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}

	return cond()
}

// quietLogger returns a debug-level logger over an in-memory buffer, so
// tests can assert that nothing sensitive reached the log.
func quietLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}

	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// writeExecutable creates an executable script file for discovery tests.
func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil { //nolint:gosec // a launcher script must be executable
		t.Fatalf("write %s: %v", path, err)
	}
}
