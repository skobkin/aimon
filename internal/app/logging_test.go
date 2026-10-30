package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2/test"
)

// rotateLogFile keeps a still-small log alone, rotates an oversized one to
// aimon.log.old (replacing a previous generation), and treats a missing
// log as nothing to do.
func TestRotateLogFileKeepsOneGeneration(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "aimon.log")

	if err := rotateLogFile(logPath); err != nil {
		t.Fatalf("rotate of a missing log: %v", err)
	}

	small := []byte("hello\n")
	if err := os.WriteFile(logPath, small, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rotateLogFile(logPath); err != nil {
		t.Fatalf("rotate of a small log: %v", err)
	}
	if got, err := os.ReadFile(logPath); err != nil || !bytes.Equal(got, small) { // #nosec G304 -- t.TempDir path
		t.Errorf("small log was touched: %q, %v", got, err)
	}
	if _, err := os.Stat(logPath + ".old"); !os.IsNotExist(err) {
		t.Errorf("a small log must not produce .old: %v", err)
	}

	big := bytes.Repeat([]byte("x"), logRotateBytes+1)
	if err := os.WriteFile(logPath, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rotateLogFile(logPath); err != nil {
		t.Fatalf("rotate of an oversized log: %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("the oversized log must have moved away: %v", err)
	}
	got, err := os.ReadFile(logPath + ".old") // #nosec G304 -- t.TempDir path // #nosec G304 -- t.TempDir path
	if err != nil || !bytes.Equal(got, big) {
		t.Errorf(".old content = %d bytes, %v; want the full %d", len(got), err, len(big))
	}

	// A fresh small current must not replace the kept generation.
	if err := os.WriteFile(logPath, small, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rotateLogFile(logPath); err != nil {
		t.Fatalf("second rotate: %v", err)
	}
	old, _ := os.ReadFile(logPath + ".old") // #nosec G304 -- t.TempDir path
	if !bytes.Equal(old, big) {
		t.Error(".old was replaced by a rotation that had nothing to rotate")
	}

	// But a second oversized log replaces it, one generation deep.
	if err := os.WriteFile(logPath, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rotateLogFile(logPath); err != nil {
		t.Fatalf("third rotate: %v", err)
	}
	old, _ = os.ReadFile(logPath + ".old") // #nosec G304 -- t.TempDir path
	if !bytes.Equal(old, big) {
		t.Error(".old must hold the newest rotated log")
	}
}

// The file logger appends across restarts and rotates a runaway log at
// open time, so the current file always starts fresh enough to read.
func TestFileLogWriterAppendsAndRotates(t *testing.T) {
	root := t.TempDir()
	wantPath := filepath.Join(root, "logs", "aimon.log")

	w, f, path := fileLogWriter(root)
	if path != wantPath {
		t.Fatalf("log path = %q, want %q", path, wantPath)
	}
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// A second start appends to the same file.
	w2, f2, _ := fileLogWriter(root)
	if _, err := w2.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	_ = f2.Close()
	got, err := os.ReadFile(wantPath) // #nosec G304 -- t.TempDir path
	if err != nil || string(got) != "first\nsecond\n" {
		t.Errorf("log after restart = %q, %v; want append", got, err)
	}

	// An oversized log rotates to .old at the next open.
	if err := os.WriteFile(wantPath, bytes.Repeat([]byte("x"), logRotateBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	w3, f3, _ := fileLogWriter(root)
	if _, err := w3.Write([]byte("fresh\n")); err != nil {
		t.Fatal(err)
	}
	_ = f3.Close()
	old, err := os.ReadFile(wantPath + ".old") // #nosec G304 -- t.TempDir path
	if err != nil || len(old) != logRotateBytes+1 {
		t.Errorf(".old = %d bytes, %v; want the rotated %d", len(old), err, logRotateBytes+1)
	}
	fresh, _ := os.ReadFile(wantPath) // #nosec G304 -- t.TempDir path
	if string(fresh) != "fresh\n" {
		t.Errorf("current log after rotation = %q, want a fresh file", got)
	}
}

// A failure anywhere in the file-logging path degrades to stderr-only,
// reporting an empty path — startup never blocks on logging.
func TestFileLogWriterFallsBackToStderr(t *testing.T) {
	root := t.TempDir()
	// A regular file where the log directory must be created: every mkdir
	// below it fails with ENOTDIR.
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	w, f, path := fileLogWriter(filepath.Join(blocker, "inside"))
	if f != nil || path != "" {
		t.Errorf("fallback = file %v, path %q; want nil and empty", f, path)
	}
	if w != os.Stderr {
		t.Error("fallback must keep stderr as the writer")
	}
}

// The storage root is read from the Fyne app's own storage URI, so both
// the log directory and the About-tab links follow the runtime identity.
func TestStorageRootPathComesFromAppStorage(t *testing.T) {
	root := storageRootPath(test.NewTempApp(t))
	if root == "" {
		t.Fatal("storage root path is empty")
	}
	if !filepath.IsAbs(root) {
		t.Errorf("storage root = %q, want an absolute path", root)
	}
}
