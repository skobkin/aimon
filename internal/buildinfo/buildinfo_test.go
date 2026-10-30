package buildinfo

import "testing"

// VersionLine composes the --version / launch-log line: name and version
// always; commit and build date only when a release build stamped them.
func TestVersionLine(t *testing.T) {
	version, commit, date := Version, Commit, BuildDate
	t.Cleanup(func() { Version, Commit, BuildDate = version, commit, date })

	Version = "v1.2.3"
	Commit, BuildDate = "", ""
	if got, want := VersionLine(), "AI Monitor v1.2.3"; got != want {
		t.Errorf("dev-stamp line = %q, want %q", got, want)
	}

	Commit, BuildDate = "abc1234", "2026-10-30T12:00:00Z"
	if got, want := VersionLine(), "AI Monitor v1.2.3 commit abc1234 built 2026-10-30T12:00:00Z"; got != want {
		t.Errorf("release line = %q, want %q", got, want)
	}
}
