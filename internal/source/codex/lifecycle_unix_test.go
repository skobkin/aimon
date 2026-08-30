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
	// child crash reports a connection error and leaves nothing behind.
	h := newFakeHarness(t, "eof")
	adapter := New(h.settings, quietLoggerForTest(t), "v")
	_, err := adapter.Refresh(context.Background())
	if err == nil {
		t.Fatal("refresh succeeded on a child that died instantly")
	}
	var se *source.Error
	if !errors.As(err, &se) || se.Category == source.CatConfig {
		t.Fatalf("unexpected classification: %v", err)
	}
	assertReaped(t, h)
}
