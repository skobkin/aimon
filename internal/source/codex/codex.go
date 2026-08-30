package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// Protocol timing (docs/S6_CODEX.md §Errors and timeouts). The outer
// RequestTimeout must cover initialize + read + a reaping grace; the stage
// timeouts bound each step inside it, and a caller-provided context bounds
// everything.
const (
	requestTimeout    = 30 * time.Second
	initializeTimeout = 10 * time.Second
	readTimeout       = 15 * time.Second
)

// The only account method S6 is allowed to call, plus the protocol
// handshake. Everything else on the account surface — reset-credit
// consumption, nudge emails, login/logout, usage reads, account/read — is
// deliberately unreachable from this package (docs/S6_AGENT_RULES.md §10).
const (
	methodInitialize  = "initialize"
	methodInitialized = "initialized"
	methodRateLimits  = "account/rateLimits/read"
	rpcMethodNotFound = -32601
)

// Adapter polls the ChatGPT Codex subscription through a Codex CLI
// app-server process. It is a pointer type usable as a source.Source and,
// in persistent mode, a source.Closer: Close disposes the owned process and
// the next Refresh lazily starts a fresh one, which is what the manager's
// disable-then-re-enable flow relies on.
type Adapter struct {
	settings config.CodexSettings
	log      *slog.Logger
	// appVersion is reported as the JSON-RPC client version during
	// initialize.
	appVersion string
	// now is the observation clock (test seam).
	now func() time.Time

	// Stage timeouts are fields so tests can shrink them; New sets the
	// protocol defaults.
	initTimeout time.Duration
	readTimeout time.Duration

	// mu guards the persistent-mode connection. On-demand refreshes keep
	// everything local; the manager never overlaps two refreshes of one
	// adapter, but Close may race an in-flight refresh from Apply.
	mu   sync.Mutex
	conn *rpcConn

	// lastPlan/lastWindows are the presentation summary of the latest
	// successful probe run; only Test's pipeline writes and reads them.
	lastPlan    string
	lastWindows int
}

// New builds a Codex adapter for one configured instance.
func New(settings config.CodexSettings, log *slog.Logger, appVersion string) *Adapter {
	return &Adapter{
		settings:    settings,
		log:         log,
		appVersion:  appVersion,
		now:         time.Now,
		initTimeout: initializeTimeout,
		readTimeout: readTimeout,
	}
}

// Kind implements source.Source.
func (a *Adapter) Kind() domain.SourceKind { return domain.KindCodex }

// RequestTimeout implements source.Source. The remote convention is 10s,
// but one refresh here spans a process spawn, an initialize round trip, a
// live backend read, and the reap of the process — the stages alone budget
// 10s + 15s + 2s.
func (a *Adapter) RequestTimeout() time.Duration { return requestTimeout }

// Refresh implements source.Source. It performs exactly one rate-limits
// read after the required handshake, and never leaves a process behind in
// on-demand mode.
func (a *Adapter) Refresh(ctx context.Context) (domain.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		// A cancelled loop must not spawn a process at all.
		return nil, err
	}
	if a.settings.EffectiveCodexMode() == config.CodexModePersistent {
		return a.refreshPersistent(ctx)
	}

	return a.refreshOnDemand(ctx)
}

// Close implements source.Closer: the owned app-server, if any, is reaped
// before Close returns. Close is idempotent and safe on an adapter that
// never started one; the next Refresh starts a new process.
func (a *Adapter) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.teardownLocked(false)
}

func (a *Adapter) refreshOnDemand(ctx context.Context) (domain.Snapshot, error) {
	conn, err := a.start(ctx)
	if err != nil {
		return nil, err
	}
	snap, readErr := a.readRates(ctx, conn)
	// A cancelled context means waiting for a graceful EOF is pointless;
	// otherwise give the app-server its short clean-exit window first. The
	// reap always completes before Refresh returns.
	kill := ctx.Err() != nil
	if closeErr := conn.shutdown(kill); closeErr != nil {
		// A messy exit after a valid snapshot is a log detail, not a lost
		// snapshot (docs/S6_IMPLEMENTATION_SPEC.md §6).
		a.log.Debug("codex app-server exit", "error", closeErr)
	}
	if readErr != nil {
		return nil, readErr
	}

	return snap, nil
}

func (a *Adapter) refreshPersistent(ctx context.Context) (domain.Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.conn == nil {
		conn, err := a.start(ctx)
		if err != nil {
			return nil, err
		}
		a.conn = conn
	}
	snap, err := a.readRates(ctx, a.conn)
	if err != nil {
		// A cancelled caller's context says nothing about stream health: the
		// manager detaches the loop on any reconfiguration (an interval-only
		// change among them), and the owned app-server must survive that —
		// only a schedule changed. A late reply to the abandoned request is
		// skipped by id correlation on the next call. A timeout, by
		// contrast, leaves the request's outcome unknown, and a transport or
		// protocol failure means the stream itself is gone: both tear the
		// connection down and the next refresh starts clean
		// (docs/S6_CODEX.md §Persistent lifecycle).
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		a.teardownLocked(true)

		return nil, err
	}

	return snap, nil
}

// teardownLocked disposes the persistent connection if one is held.
func (a *Adapter) teardownLocked(kill bool) {
	if a.conn == nil {
		return
	}
	conn := a.conn
	a.conn = nil
	if err := conn.shutdown(kill); err != nil {
		a.log.Debug("codex app-server teardown", "error", err)
	}
}

// start spawns and initializes one app-server process.
func (a *Adapter) start(ctx context.Context) (*rpcConn, error) {
	conn, err := spawn(a.settings, a.log)
	if err != nil {
		return nil, classifySpawnError(err)
	}
	if err := a.handshake(ctx, conn); err != nil {
		// The reap doubles as the observation: a child that already ended on
		// its own reports its own exit status, while one AI Monitor had to
		// kill reports a signal and therefore no status at all. Classifying
		// from the stream death alone would guess, and the guess used to
		// blame a missing graftcp on the CLI's feature set.
		waitErr := conn.shutdown(true)
		if waitErr != nil {
			// An *exec.ExitError's text is "exit status N"/"signal: killed";
			// the child's stderr never joins it (rpc.go drains it separately).
			a.log.Debug("codex app-server startup wait", "error", waitErr)
		}

		return nil, classifyStageError(methodInitialize, err, exitStatusOf(waitErr))
	}

	return conn, nil
}

// handshake performs the initialize/initialized exchange. Its error is the
// raw protocol failure, deliberately unclassified: the caller reaps the
// process first, and the classification is only as honest as the exit
// observation that reap produces (see classifyStageError).
func (a *Adapter) handshake(ctx context.Context, conn *rpcConn) error {
	initCtx, cancel := context.WithTimeout(ctx, a.initTimeout)
	defer cancel()
	if err := conn.call(initCtx, methodInitialize, newInitializeParams(a.appVersion), new(initializeResult)); err != nil {
		return err
	}

	return conn.notify(methodInitialized, nil)
}

// readRates performs the single permitted account read and normalizes it.
func (a *Adapter) readRates(ctx context.Context, conn *rpcConn) (domain.Snapshot, error) {
	readCtx, cancel := context.WithTimeout(ctx, a.readTimeout)
	defer cancel()

	var result rateLimitsReadResult
	if err := conn.call(readCtx, methodRateLimits, nil, &result); err != nil {
		// No exit observation here on purpose: only a startup death says
		// something about the executable, a read-stage death stays a
		// connection failure whatever the process went on to do.
		return nil, classifyStageError(methodRateLimits, err, exitStatus{})
	}
	snap, err := buildSnapshot(&result, a.now())
	if err != nil {
		return nil, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "rate-limit response carried no usable window",
		}
	}

	return snap, nil
}

// initializeResult intentionally models nothing: the reply's capabilities
// are not needed and must not be depended on.
type initializeResult struct{}

// initializeParams is the JSON-RPC initialize parameter block.
type initializeParams struct {
	ClientInfo clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

func newInitializeParams(version string) initializeParams {
	return initializeParams{ClientInfo: clientInfo{Name: "aimon", Title: "AI Monitor", Version: version}}
}

// TestOutcome is the staged result of a Settings connection test. Each
// stage is reported separately so a partial failure names the stage that
// broke (docs/S6_UI_UX.md).
type TestOutcome struct {
	// CLIState is the launcher discovery state (set when the probe failed
	// to start the CLI at all).
	CLIState DiscoveryState
	// CLIIncompatible marks the authored incompatible-CLI outcome: a CLI
	// that answered and rejected the app-server interface, or one whose
	// handshake died with no exit status to learn from.
	CLIIncompatible bool
	// ChatGPTOK marks a CLI that completed the handshake and answered the
	// rate-limits read successfully.
	ChatGPTOK bool
	// PlanType and Windows are reported when the read succeeded.
	PlanType string
	Windows  int
	// Err is the classified failure of the deepest stage that broke.
	Err error
}

// Test runs one throwaway on-demand probe with the draft configuration,
// regardless of the configured mode. It fully honors ctx — the caller owns
// the budget and these stage timeouts only shorten it — and never leaves a
// process behind. It never persists anything.
func (a *Adapter) Test(ctx context.Context) (TestOutcome, error) {
	out := TestOutcome{}
	if err := ctx.Err(); err != nil {
		out.Err = err

		return out, err
	}

	// A draft test is always a one-shot probe even when the saved mode is
	// persistent: a test must never leave a process behind from unsaved
	// settings (docs/S6_AGENT_RULES.md §8). A fresh adapter, not a struct
	// copy: Adapter carries a mutex and, in persistent mode, a live
	// connection the probe must not touch.
	probe := New(a.settings, a.log, a.appVersion)
	probe.settings.Mode = config.CodexModeOnDemand

	conn, err := spawn(probe.settings, probe.log)
	if err != nil {
		out.CLIState = spawnErrorState(err)
		out.Err = classifySpawnError(err)

		return out, out.Err
	}
	out.CLIState = DiscoveryFound
	readErr := probe.probe(ctx, conn)
	kill := ctx.Err() != nil
	closeErr := conn.shutdown(kill)
	if closeErr != nil {
		probe.log.Debug("codex app-server test exit", "error", closeErr)
	}
	if readErr != nil {
		// The probe's handshake failure arrives unclassified and is
		// classified only here, so the reap above — the same observation
		// start() classifies from — can say whether the CLI ever started.
		var init unclassifiedInitError
		if errors.As(readErr, &init) {
			readErr = classifyStageError(methodInitialize, init.err, exitStatusOf(closeErr))
		}
		if isIncompatibleCLI(readErr) {
			out.CLIIncompatible = true
		}
		out.Err = readErr

		return out, readErr
	}
	out.ChatGPTOK = true
	out.PlanType = probe.lastPlan
	out.Windows = probe.lastWindows

	return out, nil
}

// probe runs Test's one-shot pipeline (handshake + read) against an already
// spawned connection, stashing the presentation summary on the way. Only its
// handshake failure is wrapped — a read failure comes back already
// classified, and the read stage is exactly the one that never reinterprets
// an exit status.
func (a *Adapter) probe(ctx context.Context, conn *rpcConn) error {
	if err := a.handshake(ctx, conn); err != nil {
		return unclassifiedInitError{err: err}
	}
	snap, err := a.readRates(ctx, conn)
	if err != nil {
		return err
	}
	codexSnap, ok := snap.(domain.CodexSnapshot)
	if !ok {
		return errInvalidResponse
	}
	a.lastPlan = codexSnap.PlanType
	a.lastWindows = len(codexSnap.Windows)

	return nil
}

// unclassifiedInitError carries the initialize stage's raw failure to Test,
// which classifies it after the process has been reaped. start() classifies
// the same failure in place and never lets this type escape the package.
type unclassifiedInitError struct{ err error }

func (e unclassifiedInitError) Error() string { return e.err.Error() }

// Unwrap keeps the transport cause reachable for the classifier and for the
// errors.Is checks the transport failure itself supports.
func (e unclassifiedInitError) Unwrap() error { return e.err }

func spawnErrorState(err error) DiscoveryState {
	var se *spawnError
	if errors.As(err, &se) {
		return se.state
	}

	return DiscoveryNotExecutable
}

// isMethodNotFound reports whether err (possibly a classified wrapper) is a
// JSON-RPC method-not-found rejection: the marker of a CLI too old for the
// app-server rate-limit interface.
func isMethodNotFound(err error) bool {
	var rpcErr *rpcError

	return errors.As(err, &rpcErr) && rpcErr.Code == rpcMethodNotFound
}

// incompatibleCLIMessage is the authored setup guidance for a CLI that
// cannot serve the app-server interface: it said so itself, with a
// method-not-found rejection (docs/S6_CODEX.md §Errors and timeouts).
const incompatibleCLIMessage = "This Codex CLI does not support the app-server rate-limit interface. Update Codex CLI or choose a compatible executable."

// errIncompatibleCLI marks the incompatible-CLI condition on the wrapped
// chain so callers can recognize that shape.
var errIncompatibleCLI = errors.New("codex app-server interface unavailable")

// errExitedDuringStartup marks the observed-exit condition on the wrapped
// chain: the CLI process ended on its own before the handshake completed,
// which is not evidence about its feature set.
var errExitedDuringStartup = errors.New("codex CLI exited during startup")

// incompatibleCLIError wraps cause into the classified setup error.
func incompatibleCLIError(cause error) *source.Error {
	return &source.Error{
		Category: source.CatConfig,
		Detail:   incompatibleCLIMessage,
		Err:      fmt.Errorf("%w: %w", errIncompatibleCLI, cause),
	}
}

// exitedStartupFormat is the honest startup guidance for a CLI process that
// was observed to end before the handshake completed. Both causes are named
// because a non-zero exit is exactly what a wrapper whose own tools are
// missing produces (`exec graftcp …` under a PATH without graftcp exits
// 127) and also what an executable without app-server support produces.
const exitedStartupFormat = "The Codex CLI exited during startup (exit status %d). Its executable may lack app-server support, or a wrapper script failed to start its tools."

// exitedDuringStartupError builds that classified setup error around an exit
// status AI Monitor observed rather than guessed.
func exitedDuringStartupError(status int) *source.Error {
	return &source.Error{
		Category: source.CatConfig,
		Detail:   fmt.Sprintf(exitedStartupFormat, status),
		Err:      fmt.Errorf("%w (exit status %d)", errExitedDuringStartup, status),
	}
}

// IsExitedDuringStartup reports whether err is the classified failure of a
// CLI process that ended on its own before the handshake completed. The
// settings test report uses it to name the launcher stage: this CLI never
// answered anything, so "incompatible" and "OK" would both be wrong.
func IsExitedDuringStartup(err error) bool {
	return errors.Is(err, errExitedDuringStartup)
}

// isIncompatibleCLI reports whether err is the incompatible-CLI condition:
// an explicit method-not-found rejection, or a handshake whose stream died
// while the process was still running and whose exit status is therefore
// unknown.
func isIncompatibleCLI(err error) bool {
	return errors.Is(err, errIncompatibleCLI) || isMethodNotFound(err)
}

// exitStatus is what the reap of the CLI process said about how it ended.
type exitStatus struct {
	code  int
	known bool
}

// exitStatusOf reads a reap result. Wait reports a clean exit as a nil
// error and a non-zero exit as an *exec.ExitError that exited; a signal —
// our own teardown kill, not the CLI's doing — must read as unknown, so a
// timeout can never be misreported as a CLI that quit by itself.
func exitStatusOf(waitErr error) exitStatus {
	if waitErr == nil {
		return exitStatus{code: 0, known: true}
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) && exitErr.Exited() {
		return exitStatus{code: exitErr.ExitCode(), known: true}
	}

	return exitStatus{}
}

// isTransportFailure reports whether err is a stream-level death (EOF,
// read or write failure) rather than a classified protocol outcome or a
// caller-context cancellation/timeout.
func isTransportFailure(err error) bool {
	var rpcErr *rpcError
	var malformed malformedJSONError

	return !errors.As(err, &rpcErr) && !errors.As(err, &malformed) &&
		!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled)
}

// classifySpawnError maps launcher resolution/start failures onto the
// authored setup messages. The details are user-facing on purpose: they
// must tell the user what to fix, and they never contain environment
// values, stderr, or account data.
func classifySpawnError(err error) error {
	var se *spawnError
	if !errors.As(err, &se) {
		return &source.Error{Category: source.CatConnection, Detail: "failed to start Codex CLI", Err: err}
	}
	switch se.state {
	case DiscoveryNotFoundInPath:
		return &source.Error{
			Category: source.CatConfig,
			Detail:   "Codex CLI not found in PATH or the user bin directories (~/.local/bin, ~/bin). Install Codex CLI or choose its executable/wrapper.",
		}
	case DiscoveryExplicitMissing, DiscoveryNotExecutable:
		return &source.Error{
			Category: source.CatConfig,
			Detail:   "Codex executable not found or not executable.",
		}
	default:
		return &source.Error{Category: source.CatConfig, Detail: "Codex CLI could not be started"}
	}
}

// classifyStageError maps handshake/read failures onto the source error
// taxonomy, always wrapping the original error so callers can still
// recognize a method-not-found rejection. Timeouts stay timeouts, transport
// death is a connection failure, protocol garbage is an invalid response, a
// JSON-RPC error from the backend is auth/provider-shaped, and an
// unsupported method is the authored incompatible-CLI setup message.
//
// A transport-shaped failure during the initialize stage is a CLI that
// never came up, and what it means depends on what the process did:
// a status it exited with itself is reported as that status with both of
// its plausible causes — an executable without app-server support, or a
// wrapper script whose own tools did not start — while a stream that died
// under a still-running process (no status known) keeps the authored
// incompatible-CLI message, because "Codex CLI: OK" followed by a
// connection error would send the user in the wrong direction
// (docs/S6_CODEX.md §Errors and timeouts, DECISION_LOG 2026-08-30).
func classifyStageError(stage string, err error, exit exitStatus) error {
	if stage == methodInitialize && isTransportFailure(err) {
		if exit.known {
			return exitedDuringStartupError(exit.code)
		}

		return incompatibleCLIError(err)
	}
	var rpcErr *rpcError
	var malformed malformedJSONError
	var write writeError
	switch {
	case errors.As(err, &rpcErr):
		if rpcErr.Code == rpcMethodNotFound {
			return &source.Error{
				Category: source.CatConfig,
				Detail:   incompatibleCLIMessage,
				Err:      err,
			}
		}
		if looksLikeAuth(rpcErr.Message) {
			return &source.Error{Category: source.CatAuth, Detail: "ChatGPT authentication failed during " + stage, Err: err}
		}

		return &source.Error{
			// CatBackend, not CatProvider: the fixed provider message names
			// an HTTP 5xx that does not exist on a JSON-RPC surface. The
			// detail stays code-only (see rpcError.Error).
			Category: source.CatBackend,
			Detail:   fmt.Sprintf("%s rejected (json-rpc error %d)", stage, rpcErr.Code),
			Err:      err,
		}
	case errors.Is(err, context.DeadlineExceeded):
		return &source.Error{Category: source.CatTimeout, Detail: stage + " timed out", Err: err}
	case errors.Is(err, context.Canceled):
		return &source.Error{Category: source.CatConnection, Detail: stage + " cancelled", Err: err}
	case errors.As(err, &malformed):
		return &source.Error{Category: source.CatInvalidResponse, Detail: stage + " returned a malformed response", Err: err}
	case errors.As(err, &write):
		return &source.Error{Category: source.CatConnection, Detail: "the codex app-server stream is broken", Err: err}
	default:
		return &source.Error{Category: source.CatConnection, Detail: "codex app-server stopped responding during " + stage, Err: err}
	}
}

// looksLikeAuth reports whether a backend error message indicates an
// authentication problem. It is deliberately narrow: the classified message
// ("Authentication failed") is only claimed when the provider said
// something auth-shaped; anything else stays a provider error.
func looksLikeAuth(message string) bool {
	msg := strings.ToLower(message)
	for _, marker := range []string{"unauthorized", "unauthorised", "not authenticated", "authentication", "login required", "401", "403"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}

	return false
}
