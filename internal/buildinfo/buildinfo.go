// Package buildinfo holds build-time application identity. Values are
// overridden at link time for tagged releases and default to dev otherwise.
package buildinfo

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
