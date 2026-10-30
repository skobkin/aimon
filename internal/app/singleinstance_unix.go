//go:build unix

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockSingleInstance opens path and takes an exclusive non-blocking flock
// on it. Contention returns an error wrapping ErrAlreadyRunning; an empty
// path or an unusable lock file degrades to (nil, nil) — running unguarded
// beats failing to start over a lock file. A nil file with nil error means
// "acquired, nothing to hold".
func lockSingleInstance(path string) (*os.File, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil //nolint:nilerr // deliberate degrade-to-unguarded: startup must not fail over a lock file
	}
	// #nosec G304 -- the path is the app's own lock file under the user config dir.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil //nolint:nilerr // deliberate degrade-to-unguarded: startup must not fail over a lock file
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()

		return nil, fmt.Errorf("%w (%s)", ErrAlreadyRunning, path)
	}

	return f, nil
}
