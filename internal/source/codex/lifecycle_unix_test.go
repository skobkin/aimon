//go:build unix

package codex

import (
	"context"
	"errors"
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
	// child crash reports a classified error and leaves nothing behind. A
	// child that dies during the handshake is indistinguishable from a CLI
	// without app-server support, so it carries the setup guidance.
	h := newFakeHarness(t, "eof")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	_, err := adapter.Refresh(context.Background())
	if err == nil {
		t.Fatal("refresh succeeded on a child that died instantly")
	}
	wantRefreshError(t, err, source.CatConfig, "does not support")
	assertReaped(t, h)
}
