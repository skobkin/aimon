package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
)

// rpcError is a JSON-RPC error object of a response.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// rpcMessage is one decoded stdout line. A message with a Method is a
// server-initiated notification or request and never answers our calls.
type rpcMessage struct {
	// RawID keeps the id's JSON spelling: JSON-RPC allows numbers and
	// strings, and decoding into *int64 would make a string reply look
	// unrelated and turn every call into a timeout.
	RawID   json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
	Err     error           `json:"-"`
	Emitted bool            `json:"-"`
}

// terminal wraps a transport-level failure (EOF, read error, malformed
// line) into the stream. After a terminal message the reader goroutine has
// returned; the messages channel itself is never closed, so a call can
// never spin on a closed channel.
func terminal(err error) rpcMessage {
	return rpcMessage{Err: err, Emitted: true}
}

// malformedJSONError marks protocol output that decoded as a line but is
// not a usable JSON-RPC message or payload, so the classifier can report an
// invalid response rather than a broken connection.
type malformedJSONError struct{ err error }

func (e malformedJSONError) Error() string { return e.err.Error() }

// Unwrap preserves the cause for the log while keeping the marker findable
// with errors.As.
func (e malformedJSONError) Unwrap() error { return e.err }

func malformedJSON(err error) error { return malformedJSONError{err: err} }

// writeError marks a failure to send on the child's stdin: the stream is
// broken (the process usually died), not the response malformed.
type writeError struct{ err error }

func (e writeError) Error() string { return e.err.Error() }

func (e writeError) Unwrap() error { return e.err }

// rpcConn is one spawned app-server process plus its JSON-RPC stream. It is
// single-use: after shutdown the connection is dead (the adapter spawns a
// fresh one when needed).
//
// Concurrency: call and notify must not be called concurrently with each
// other (the adapter serializes refreshes); shutdown may be called at any
// time, including concurrently with a call racing its context.
type rpcConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	msgs chan rpcMessage
	// done is closed by shutdown; the reader uses it to stop waiting to
	// deliver a message, and call uses it to stop waiting for a reply that
	// can no longer arrive.
	done chan struct{}

	// waitErr receives the single cmd.Wait result. The wait goroutine
	// started at spawn is the process's only reaper: calling Wait twice
	// would fail the second time and leak the child as a zombie exactly on
	// the force-kill path.
	waitErr chan error
	// waitOnce guards the shutdown bookkeeping, killOnce the signal.
	waitOnce sync.Once
	killOnce sync.Once

	// writeMu serializes stdin writes against shutdown's stream teardown.
	writeMu sync.Mutex
	nextID  int64

	stderrMu   sync.Mutex
	stderrTail []byte

	log *slog.Logger
}

// lineLimit bounds one JSON-RPC message. A real rate-limit reply is a few
// kilobytes; a blown cap means the stream is producing something we did not
// ask for and must not buffer without end.
const lineLimit = 1 << 20

// shutdownGrace is how long a graceful shutdown waits for the app-server to
// exit on stdin EOF before the process group is killed. It is a variable so
// tests can shrink it.
var shutdownGrace = 2 * time.Second

// stderrTailLimit is how much of the child's stderr is retained for
// debug-length reporting. The pipe is always drained to EOF — a stalled
// reader would fill the pipe and deadlock the child — but the content is
// never surfaced: at most its size is logged.
const stderrTailLimit = 4096

// spawn resolves and starts one app-server process with the configured
// launcher and environment. A failed start owns no process and skips every
// teardown step.
func spawn(settings config.CodexSettings, log *slog.Logger) (*rpcConn, error) {
	d := Discover(settings)
	if !d.OK() {
		return nil, &spawnError{state: d.State}
	}

	// Launching a user-configured launcher is the entire point of this
	// source: d.Path is our own resolution of the settings value, and the
	// argv is fixed. No shell is involved.
	cmd := exec.Command(d.Path, spawnArgv()...) //nolint:gosec // the configured launcher path is the integration boundary
	cmd.Env = effectiveEnv(settings)
	cmd.Dir = "" // inherit the application's working directory

	attr := &syscall.SysProcAttr{}
	setProcessGroup(attr)
	cmd.SysProcAttr = attr

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, &spawnError{err: fmt.Errorf("stdin pipe: %w", err)}
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closePipe(stdinR, stdinW)

		return nil, &spawnError{err: fmt.Errorf("stdout pipe: %w", err)}
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closePipe(stdinR, stdinW)
		closePipe(stdoutR, stdoutW)

		return nil, &spawnError{err: fmt.Errorf("stderr pipe: %w", err)}
	}

	// The *os.File wiring is deliberate: exec.Cmd's StdoutPipe/StdinPipe
	// documentation forbids calling Wait before reads finish, and Wait
	// closing the pipe under the reader is exactly what a force kill needs
	// to avoid. With raw pipes the parent fully controls the lifecycle:
	// Wait only waits for the process, and the reader ends when the write
	// end closes or when teardown closes our read end (a launcher
	// grandchild may hold the write end open long after the leader died).
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	c := &rpcConn{
		cmd:     cmd,
		stdin:   stdinW,
		stdout:  stdoutR,
		stderr:  stderrR,
		msgs:    make(chan rpcMessage, 8),
		done:    make(chan struct{}),
		waitErr: make(chan error, 1),
		nextID:  1,
		log:     log,
	}

	if err := cmd.Start(); err != nil {
		closePipe(stdinR, stdinW)
		closePipe(stdoutR, stdoutW)
		closePipe(stderrR, stderrW)

		return nil, &spawnError{err: err}
	}
	// The child owns its ends now; the parent's copies only keep the pipes
	// artificially open and would mask EOF.
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()

	// The process's single reaper.
	go func() { c.waitErr <- cmd.Wait() }()
	go c.readLoop()
	go c.drainStderr()

	return c, nil
}

func closePipe(r, w *os.File) {
	_ = r.Close()
	_ = w.Close()
}

// call sends one request and waits for the response carrying its id,
// skipping notifications and any other id. The context bounds the wait and
// may only shorten the caller's budget: a timed-out stream is left broken
// for the adapter to tear down — a late response must never be matched by a
// later call.
func (c *rpcConn) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID
	c.nextID++
	if err := c.writeRequest(method, id, params); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return errors.New("codex app-server stream closed during the call")
		case msg := <-c.msgs:
			if msg.Emitted {
				return msg.Err
			}
			if msg.Method != "" {
				continue // unsolicited notification or server request
			}
			if len(msg.RawID) == 0 {
				continue
			}
			got, err := decodeID(msg.RawID)
			if err != nil {
				return malformedJSON(fmt.Errorf("malformed response id: %w", err))
			}
			if got != id {
				continue // stale or duplicated reply to an earlier request
			}
			if msg.Error != nil {
				return msg.Error
			}
			if out != nil && len(msg.Result) > 0 {
				if err := json.Unmarshal(msg.Result, out); err != nil {
					return malformedJSON(fmt.Errorf("malformed response result: %w", err))
				}
			}

			return nil
		}
	}
}

// notify sends a notification (no id, no reply expected).
func (c *rpcConn) notify(method string, params any) error {
	return c.writeRequest(method, 0, params)
}

func (c *rpcConn) writeRequest(method string, id int64, params any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	select {
	case <-c.done:
		return errors.New("codex app-server stream is closed")
	default:
	}

	request := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      *int64 `json:"id,omitempty"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: params}
	if id != 0 {
		request.ID = &id
	}
	line, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return writeError{err: fmt.Errorf("write request: %w", err)}
	}

	return nil
}

// readLoop decodes stdout one line at a time until EOF or a malformed line,
// then emits the terminal condition and exits. Delivering a message never
// blocks indefinitely: teardown closes done, which releases a reader wedged
// on a full channel (a persistent child keeps emitting notifications
// between refreshes, when nobody is receiving).
func (c *rpcConn) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), lineLimit)
	for {
		if !scanner.Scan() {
			err := scanner.Err()
			if err == nil {
				err = io.EOF
			}
			c.emit(terminal(err))

			return
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.emit(terminal(malformedJSON(fmt.Errorf("malformed json-rpc line: %w", err))))

			return
		}
		c.emit(msg)
	}
}

func (c *rpcConn) emit(msg rpcMessage) {
	select {
	case c.msgs <- msg:
	case <-c.done:
	}
}

// drainStderr keeps the child's stderr pipe empty for the process's whole
// lifetime while retaining only its tail. The content is never logged and
// never reaches a user-facing error.
func (c *rpcConn) drainStderr() {
	buf := make([]byte, 4096)
	for {
		n, err := c.stderr.Read(buf)
		if n > 0 {
			c.stderrMu.Lock()
			c.stderrTail = append(c.stderrTail, buf[:n]...)
			if len(c.stderrTail) > stderrTailLimit {
				c.stderrTail = c.stderrTail[len(c.stderrTail)-stderrTailLimit:]
			}
			c.stderrMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// stderrSize reports how much stderr the child produced, for debug logging
// only.
func (c *rpcConn) stderrSize() int {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()

	return len(c.stderrTail)
}

// shutdown closes the stream and reaps the process, always within its own
// bounded time: graceful first (close stdin, let a healthy app-server exit),
// then a forced kill of the process group, then the reap. kill skips the
// graceful phase — used when cancellation or a broken stream means waiting
// for EOF would be pointless. The returned error is the wait result for
// diagnostics; a kill failure never replaces the caller's real error.
func (c *rpcConn) shutdown(kill bool) error {
	var first error
	c.waitOnce.Do(func() {
		if kill {
			c.kill()
		} else {
			_ = c.stdin.Close() // EOF on the child's stdin: the clean exit signal
		}
		select {
		case first = <-c.waitErr:
		case <-time.After(shutdownGrace):
			c.kill()
			first = <-c.waitErr
		}
	})
	// cmd.Wait may have returned while launcher-spawned grandchildren still
	// hold the pipe write ends; closing our read ends ends the reader and
	// the stderr drain in that case too.
	_ = c.stdout.Close()
	_ = c.stderr.Close()
	_ = c.stdin.Close()
	close(c.done)

	if c.stderrSize() > 0 {
		c.log.Debug("codex app-server produced stderr output", "bytes", c.stderrSize())
	}

	return first
}

func (c *rpcConn) kill() {
	c.killOnce.Do(func() {
		if c.cmd.Process == nil {
			return
		}
		// A failure here (process already gone) is not interesting: the
		// reap below is what matters.
		_ = killProcess(c.cmd.Process)
	})
}

// decodeID accepts the two JSON-RPC id spellings: a number and a string of
// digits. Anything else is a protocol violation rather than an unrelated
// reply, so the caller reports it instead of skipping it.
func decodeID(raw json.RawMessage) (int64, error) {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseInt(s, 10, 64)
	}

	return 0, fmt.Errorf("unsupported id %s", string(raw))
}
