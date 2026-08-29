package app

// Build metadata stamped into release binaries by GoReleaser via -ldflags
// -X. Development builds keep the defaults below; the Fyne metadata in
// appMetadata/FyneApp.toml remains the packaged application version.
var (
	// Version is the release version (the git tag), or "dev" for
	// non-release builds.
	Version = "dev"

	// BuildDate is the RFC 3339 release build time, empty for
	// non-release builds.
	BuildDate = ""
)
