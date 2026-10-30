package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
)

// The autostart entry lives where the session looks for it, carries the
// quoted executable so spaces survive, and is fully removable by the
// Settings toggle.
func TestAutostartPathAndEntry(t *testing.T) {
	configHome := "/home/tester/.config"
	if got, want := autostartPath(configHome), filepath.Join(configHome, "autostart", "aimon.desktop"); got != want {
		t.Fatalf("autostartPath = %q, want %q", got, want)
	}

	entry := autostartEntry("/opt/apps/my monitor/aimon")
	for _, want := range []string{
		"[Desktop Entry]",
		"Type=Application",
		"Name=" + buildinfo.AppName,
		`Exec="/opt/apps/my monitor/aimon"`,
		"Icon=" + appID,
		"Terminal=false",
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("entry lacks %q:\n%s", want, entry)
		}
	}
}

// write then remove round-trips through the filesystem; removing a second
// time is still success (the goal state already holds).
func TestWriteAndRemoveAutostartEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autostart", "aimon.desktop")

	if autostartPresent(path) {
		t.Fatal("nothing written yet, entry must be absent")
	}
	if err := writeAutostartEntry(path, "/usr/local/bin/aimon"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !autostartPresent(path) {
		t.Fatal("written entry must be present")
	}

	raw, err := os.ReadFile(path) // #nosec G304 -- test-created path
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), `Exec="/usr/local/bin/aimon"`) {
		t.Errorf("entry lost the executable: %s", raw)
	}

	if err := removeAutostartEntry(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if autostartPresent(path) {
		t.Error("removed entry must be absent")
	}
	if err := removeAutostartEntry(path); err != nil {
		t.Errorf("removing an absent entry must succeed, got %v", err)
	}
}

// Ephemeral Go binaries must never end up in an autostart entry: `go run`
// builds vanish from the GOCACHE go-build dir, `go test` builds live under
// os.TempDir() — pointing an entry at either launches nothing at next
// login.
func TestAutostartSupported(t *testing.T) {
	cases := []struct {
		exec string
		want bool
	}{
		{"/usr/local/bin/aimon", true},
		{"/home/tester/go/bin/aimon", true},
		{filepath.Join(os.TempDir(), "go-build123", "b001", "aimon.test"), false},
		{filepath.Join(os.TempDir(), "aimon"), false},
		{"", false},
	}
	for _, c := range cases {
		if got := autostartSupported(c.exec); got != c.want {
			t.Errorf("autostartSupported(%q) = %v, want %v", c.exec, got, c.want)
		}
	}
}
