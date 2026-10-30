//go:build unix

package app

import (
	"errors"
	"path/filepath"
	"testing"
)

// flock exclusivity: the first taker wins, a second concurrent taker gets
// ErrAlreadyRunning, and after the first holder releases, the lock is
// takeable again. This exercises the in-process shape of what the launcher
// does across processes.
func TestSingleInstanceLockContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aimon", "aimon.lock")

	first, err := lockSingleInstance(path)
	if err != nil || first == nil {
		t.Fatalf("first acquire: file=%v err=%v", first, err)
	}

	second, err := lockSingleInstance(path)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire err = %v, want ErrAlreadyRunning", err)
	}
	if second != nil {
		_ = second.Close()
		t.Error("a contended acquire must not hand out a file")
	}

	if err := first.Close(); err != nil {
		t.Fatalf("release: %v", err)
	}

	third, err := lockSingleInstance(path)
	if err != nil || third == nil {
		t.Fatalf("re-acquire after release: file=%v err=%v", third, err)
	}
	if err := third.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// An empty path degrades to unguarded: acquired, nothing held.
func TestSingleInstanceLockEmptyPath(t *testing.T) {
	f, err := lockSingleInstance("")
	if err != nil {
		t.Errorf("empty path err = %v, want nil", err)
	}
	if f != nil {
		_ = f.Close()
		t.Error("empty path must not hand out a file")
	}
}
