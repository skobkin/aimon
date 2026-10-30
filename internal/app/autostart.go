package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
)

// The start-on-login feature is a plain freedesktop autostart desktop
// entry written into the user's autostart directory. It stays user-asked:
// nothing is installed until the Settings toggle is switched on (the
// 2026-08-29 "nothing is auto-installed" Decision Log entry), and turning
// the toggle off removes the file again.

// autostartPath is the autostart desktop entry for configHome
// (<configHome>/autostart/aimon.desktop). os.UserConfigDir honors
// XDG_CONFIG_HOME, so the location follows the session's actual config
// root.
func autostartPath(configHome string) string {
	return filepath.Join(configHome, "autostart", "aimon.desktop")
}

// autostartPresent reports whether the entry file exists.
func autostartPresent(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

// autostartEntry renders the desktop entry. The Exec path is quoted
// because user paths may contain spaces.
func autostartEntry(execPath string) string {
	return "# Autostart entry written by " + buildinfo.AppName + " (Settings — General).\n" +
		"[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + buildinfo.AppName + "\n" +
		"Comment=System tray monitor for AI service quotas and local GPU telemetry\n" +
		fmt.Sprintf("Exec=%q\n", execPath) +
		"Icon=" + appID + "\n" +
		"Terminal=false\n"
}

// writeAutostartEntry writes the autostart desktop entry for execPath.
func writeAutostartEntry(path, execPath string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create autostart directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(autostartEntry(execPath)), 0o644); err != nil { //nolint:gosec // the entry carries no secrets; world-readable is the convention for desktop entries
		return fmt.Errorf("write autostart entry: %w", err)
	}

	return nil
}

// removeAutostartEntry deletes the autostart desktop entry; a missing file
// is already the goal state.
func removeAutostartEntry(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove autostart entry: %w", err)
	}

	return nil
}

// autostartSupported reports whether autostart is meaningful for this
// binary. Go's ephemeral binaries are not: a `go run` build lives under
// the GOCACHE go-build directory and a `go test` build under os.TempDir(),
// and an entry pointing at either would try to launch a file that vanishes
// the moment the invocation ends.
func autostartSupported(execPath string) bool {
	if execPath == "" {
		return false
	}
	if strings.Contains(execPath, "go-build") {
		return false
	}
	if strings.HasPrefix(execPath, os.TempDir()) {
		return false
	}

	return true
}

// autostartFile is the desktop-entry path the app owns
// (<UserConfigDir>/autostart/aimon.desktop); "" when the config dir
// cannot be resolved.
func autostartFile() string {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return ""
	}

	return autostartPath(configHome)
}

// AutostartState reports (present, supported): whether the autostart
// entry currently exists, and whether autostart is meaningful for this
// binary at all — the Settings control is disabled when it is not.
func (a *App) AutostartState() (present, supported bool) {
	execPath, err := os.Executable()
	if err != nil || !autostartSupported(execPath) {
		return false, false
	}
	path := autostartFile()
	if path == "" {
		return false, false
	}

	return autostartPresent(path), true
}

// SetAutostart writes or removes the autostart desktop entry for the
// running binary.
func (a *App) SetAutostart(on bool) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	path := autostartFile()
	if path == "" {
		return errors.New("cannot determine the user configuration directory")
	}
	if on {
		return writeAutostartEntry(path, execPath)
	}

	return removeAutostartEntry(path)
}
