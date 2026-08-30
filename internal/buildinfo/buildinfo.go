// Package buildinfo holds build-time application identity. Values are
// overridden at link time for tagged releases and default to dev otherwise.
package buildinfo

// AppName is the user-facing application name shown in logs and metadata.
const AppName = "AI Monitor"

// Version is overridden at build time for tagged releases and defaults to dev.
var Version = "dev"

// BuildDate is the RFC 3339 release build time, empty for non-release builds.
var BuildDate = ""
