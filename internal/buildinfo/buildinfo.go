// Package buildinfo holds build-time application identity. Values are
// overridden at link time for tagged releases and default to dev otherwise.
package buildinfo

import "strings"

// AppName is the user-facing application name shown in logs and metadata.
const AppName = "AI Monitor"

// DevVersion is the version of a non-release build. It is not a semver the
// update checker can compare, and automatic update checks never run on it.
const DevVersion = "dev"

// Version is overridden at build time for tagged releases and defaults to
// DevVersion.
var Version = DevVersion

// BuildDate is the RFC 3339 release build time, empty for non-release builds.
var BuildDate = ""

// Commit is the release build's VCS revision, empty for non-release builds.
var Commit = ""

// VersionLine reports the one-line build identity printed by --version
// and logged at launch: name and version always, then the commit and
// build date when the build carries them.
func VersionLine() string {
	parts := []string{AppName, Version}
	if Commit != "" {
		parts = append(parts, "commit "+Commit)
	}
	if BuildDate != "" {
		parts = append(parts, "built "+BuildDate)
	}

	return strings.Join(parts, " ")
}
