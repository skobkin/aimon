//go:build unix

package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// The reaping assertions use signal 0 on the child's recorded PID: a reaped
// process answers ESRCH, so "Refresh returned" plus ESRCH proves AI Monitor
// never leaves an app-server behind.

func assertReaped(t *testing.T, h *fakeHarness) {
	t.Helper()
	pid := h.pid(t)
	if !waitUntil(func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	}) {
		t.Fatalf("child process %d is still alive", pid)
	}
}

func TestOnDemandReapsProcessAfterRefresh(t *testing.T) {
	h := newFakeHarness(t, "ok")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	assertReaped(t, h)
}

func TestCancelDuringReadKillsProcess(t *testing.T) {
	h := newFakeHarness(t, "silent")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	adapter.readTimeout = 30 * time.Second // the cancellation, not the stage timeout, must end this
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	if _, err := adapter.Refresh(ctx); err == nil {
		t.Fatal("refresh succeeded despite cancellation")
	}
	// A cancelled context skips the graceful window and goes straight to
	// the process-group kill.
	assertReaped(t, h)
}

func TestPersistentChildSurvivesCancelledRefresh(t *testing.T) {
	// The manager cancels a loop's in-flight request on every
	// reconfiguration, a pure interval change included. The owned
	// app-server must survive that: a cancellation comes from the caller,
	// not from the stream (DECISION_LOG 2 — a persistent app-server
	// survives a schedule change).
	h := newFakeHarness(t, "late")
	h.settings.Mode = config.CodexModePersistent
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	if _, err := adapter.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	pid := h.pid(t)
	if syscall.Kill(pid, 0) != nil {
		t.Fatalf("persistent child %d did not survive the cancelled refresh", pid)
	}

	// The next refresh serves its read on the same process.
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh after cancellation: %v", err)
	}
	h.awaitLines(t, "read after cancellation", 2)
	if got := h.countLines("spawn"); got != 1 {
		t.Fatalf("spawn count = %d, want 1 (the child must be reused)", got)
	}
	adapter.Close()
}

func TestPersistentCloseReapsAndStaysUsable(t *testing.T) {
	h := newFakeHarness(t, "ok")
	h.settings.Mode = config.CodexModePersistent
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	adapter.Close()
	adapter.Close() // idempotent
	assertReaped(t, h)

	// Close is disposal, not destruction: the next refresh starts fresh.
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh after close: %v", err)
	}
}

func TestHangOnEOFKilledAfterGrace(t *testing.T) {
	h := newFakeHarness(t, "hang-on-eof")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	restore := shutdownGrace
	shutdownGrace = 100 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = restore })

	start := time.Now()
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("shutdown took %v; the grace kill did not fire", elapsed)
	}
	assertReaped(t, h)
}

func TestManagerDisableReapsPersistentProcess(t *testing.T) {
	h := newFakeHarness(t, "ok")
	h.settings.Mode = config.CodexModePersistent
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	const inst = domain.InstanceID("codex-1")
	m := source.NewManager(quietLoggerForTest(t))
	defer m.Stop()
	m.Apply(map[domain.InstanceID]source.Registration{
		inst: {Kind: domain.KindCodex, Source: adapter, Enabled: true, Interval: time.Minute},
	})
	h.awaitLines(t, "loop refresh", 1)

	// Disable keeps the instance registered but drops its loop; the manager
	// must dispose of the process before Apply returns.
	m.Apply(map[domain.InstanceID]source.Registration{
		inst: {Kind: domain.KindCodex, Source: adapter, Enabled: false, Interval: time.Minute},
	})
	assertReaped(t, h)

	// Re-enabling starts a fresh process through the same adapter.
	m.Apply(map[domain.InstanceID]source.Registration{
		inst: {Kind: domain.KindCodex, Source: adapter, Enabled: true, Interval: time.Minute},
	})
	h.awaitLines(t, "re-enabled refresh", 2)
}

// A launcher wrapper that exits right after spawning the real server must
// not shield the grandchild from teardown: the leader's wait result is
// already ready when stdin is closed, so the group kill that normally rides
// the forced path has to happen on the graceful path too.
func TestWrapperExitLeavesNoGrandchild(t *testing.T) {
	h := newFakeHarness(t, "orphan-wrapper")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// The refresh was served by the grandchild, so both the wrapper and the
	// grandchild recorded a spawn. Every one of them must be gone.
	var pids []int
	for _, line := range h.logLines() {
		if !strings.HasPrefix(line, "spawn ") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimPrefix(line, "spawn "))
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	if len(pids) < 2 {
		t.Fatalf("spawn log records %v, want the wrapper and its grandchild", pids)
	}
	for _, pid := range pids {
		if !waitUntil(func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }) {
			t.Fatalf("process %d outlived the shutdown", pid)
		}
	}
}

func TestReapedChildIsNotAZombie(t *testing.T) {
	// The explicit variant of the reaping rule: after the graceful path the
	// wait result must already be collected, so a refresh that ends in a
	// child crash reports a classified error and leaves nothing behind. The
	// crash carries the status the child exited with, not a verdict about
	// the CLI's feature set.
	h := newFakeHarness(t, "eof")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	_, err := adapter.Refresh(context.Background())
	if err == nil {
		t.Fatal("refresh succeeded on a child that died instantly")
	}
	wantRefreshError(t, err, source.CatConfig, "exited during startup")
	assertReaped(t, h)
}

// The reported real-world shape: a launcher wrapper runs its own proxy tool
// by bare name (`exec graftcp codex`), the tool is on the shell's PATH but
// not on the desktop session's, and the shell exits 127 before any
// app-server exists. That is an environment failure, and neither Refresh nor
// Test may report it as a CLI without app-server support.
func TestWrapperWithMissingToolReportsStartupExit(t *testing.T) {
	dir := t.TempDir()
	// `does-not-exist` is deliberately absent from this PATH too, which is
	// what a GUI session's PATH looks like next to a terminal's.
	empty := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "cdx"), "#!/bin/sh\nexec does-not-exist codex-fake\n")

	settings := config.CodexSettings{
		Executable: filepath.Join(dir, "cdx"),
		Mode:       config.CodexModeOnDemand,
		Env: []config.EnvOverride{
			{Name: fakeChildEnv, Value: "ok"},
			{Name: fakeDirEnv, Value: t.TempDir()},
			{Name: "PATH", Value: empty},
		},
	}
	adapter := New(settings, quietLoggerForTest(t), "v")
	_, err := adapter.Refresh(context.Background())
	if err == nil {
		t.Fatal("refresh succeeded on a wrapper whose tool is missing")
	}
	if !IsExitedDuringStartup(err) {
		t.Fatalf("err = %v, want the exited-during-startup marker", err)
	}
	if isIncompatibleCLI(err) {
		t.Fatal("a missing wrapper tool must not be reported as an incompatible CLI")
	}
	wantRefreshError(t, err, source.CatConfig, "exit status 127")

	out, err := adapter.Test(context.Background())
	if err == nil {
		t.Fatal("test succeeded on a wrapper whose tool is missing")
	}
	if out.CLIIncompatible || out.ChatGPTOK {
		t.Fatalf("outcome = %+v, want no incompatible-CLI verdict", out)
	}
	if out.CLIState != DiscoveryFound {
		t.Fatalf("cli state = %q, want %q (the launcher itself resolved)", out.CLIState, DiscoveryFound)
	}
	if !IsExitedDuringStartup(out.Err) || !strings.Contains(out.Err.Error(), "127") {
		t.Fatalf("outcome err = %v, want the exited-during-startup status", out.Err)
	}
}

// The end-to-end form of the same setup: a bare launcher name resolved from
// ~/.local/bin because the desktop PATH lacks it, a wrapper that execs a
// proxy tool and the real CLI by bare name, and both of them living next to
// the wrapper. The child's PATH therefore has to start with the directory
// the launcher was found in.
func TestWrapperResolvesSiblingToolsFromLauncherDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A GUI session's PATH: system directories, no per-user bin directory.
	t.Setenv("PATH", strings.Join([]string{"/usr/local/bin", "/usr/bin"}, string(filepath.ListSeparator)))

	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", bin, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// The "real CLI" is this test binary reached through a hard link: it
	// serves a complete app-server conversation under the harness script.
	cli := filepath.Join(bin, "codex-fake")
	if err := os.Link(exe, cli); err != nil {
		t.Skipf("hardlink of the test binary: %v", err)
	}
	// The proxy front-end passes its arguments through; what matters is that
	// the wrapper reaches it by bare name.
	writeExecutable(t, filepath.Join(bin, "graftcp-fake"), "#!/bin/sh\nexec \"$@\"\n")
	writeExecutable(t, filepath.Join(bin, "cdx"), "#!/bin/sh\nexec graftcp-fake codex-fake\n")

	settings := config.CodexSettings{
		Executable: "cdx",
		Mode:       config.CodexModeOnDemand,
		Env: []config.EnvOverride{
			{Name: fakeChildEnv, Value: "ok"},
			{Name: fakeDirEnv, Value: t.TempDir()},
		},
	}
	h := &fakeHarness{dir: settings.Env[1].Value, settings: settings}
	adapter := New(settings, quietLoggerForTest(t), "v")
	if _, err := adapter.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// The refresh was served by the real CLI three lookups deep: wrapper →
	// proxy → CLI, every one of them a bare name in the child's PATH.
	h.awaitLines(t, "read through the wrapper chain", 1)
}
