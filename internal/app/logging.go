package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// logRotateBytes bounds aimon.log before it rotates to aimon.log.old —
// one kept generation, so a runaway log cannot grow unbounded while the
// previous log stays readable.
const logRotateBytes = 1 << 20 // 1 MiB

// fileLogWriter opens the rotating application log under the app storage
// root (<root>/logs/aimon.log) and returns a writer mirroring every record
// to stderr (journal/terminal capture keeps working), the opened file for
// closing after the event loop returns, and the file's path for the About
// tab. An oversized previous log rotates to aimon.log.old first; a failed
// rotation keeps appending to the oversized file rather than losing
// logging. Any other failure degrades to stderr-only (nil file, empty
// path) with a note on stderr: logging must never block startup.
func fileLogWriter(storageRoot string) (io.Writer, *os.File, string) {
	logPath := filepath.Join(storageRoot, "logs", "aimon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		fallbackToStderr(err)

		return os.Stderr, nil, ""
	}
	if err := rotateLogFile(logPath); err != nil {
		fmt.Fprintf(os.Stderr, "aimon: rotating the log failed, appending to the existing file: %v\n", err)
	}
	// #nosec G304 -- logPath is derived from the app's own storage root,
	// not from user input.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fallbackToStderr(err)

		return os.Stderr, nil, ""
	}

	return io.MultiWriter(os.Stderr, f), f, logPath
}

// fallbackToStderr notes on stderr why file logging is unavailable — the
// one message that must go to stderr directly, since no logger exists yet.
func fallbackToStderr(err error) {
	fmt.Fprintf(os.Stderr, "aimon: file logging unavailable, using stderr only: %v\n", err)
}

// rotateLogFile renames an oversized log to aimon.log.old, removing the
// previous generation first. A missing or still-small log is left alone.
func rotateLogFile(logPath string) error {
	info, err := os.Stat(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return err
	}
	if info.Size() <= logRotateBytes {
		return nil
	}
	old := logPath + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return os.Rename(logPath, old)
}
