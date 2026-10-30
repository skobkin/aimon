package app

import (
	"errors"
	"os"
	"path/filepath"
)

// The single-instance guard: one exclusive flock per user. A second launch
// is a user mistake (double-click, restart loop) that would otherwise end
// as two tray icons fighting over one preferences file, so the loser exits
// quietly before any GUI initialization. The lock is advisory and dies
// with the process — a crashed instance leaves a stale lock file, which is
// harmless.

// ErrAlreadyRunning reports that another instance holds the lock. The
// launcher maps it to a quiet, zero-status exit.
var ErrAlreadyRunning = errors.New("another instance is already running")

// singleInstanceLock is this process's open lock file, when it owns one.
var singleInstanceLock *os.File

// singleInstanceLockPath is the lock file location under the user config
// directory (<UserConfigDir>/aimon/aimon.lock). An unresolvable config dir
// degrades to running unguarded: a missing lock is a cosmetic regression,
// not a data hazard.
func singleInstanceLockPath() string {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return ""
	}

	return filepath.Join(configHome, "aimon", "aimon.lock")
}

// AcquireSingleInstanceLock takes the app's exclusive lock and returns
// nil, or an error wrapping ErrAlreadyRunning when another instance holds
// it. It must run before any Fyne/GUI initialization. The kernel releases
// the flock when the process exits; ReleaseSingleInstanceLock exists for
// the in-process tests.
func AcquireSingleInstanceLock() error {
	f, err := lockSingleInstance(singleInstanceLockPath())
	if err != nil {
		return err
	}
	singleInstanceLock = f

	return nil
}

// ReleaseSingleInstanceLock drops the lock. Normal shutdown releases it by
// exiting; this exists so tests can verify re-acquisition.
func ReleaseSingleInstanceLock() error {
	if singleInstanceLock == nil {
		return nil
	}
	err := singleInstanceLock.Close()
	singleInstanceLock = nil

	return err
}
