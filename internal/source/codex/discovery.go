// Package codex adapts the ChatGPT Codex subscription's rate limits. It
// talks only to a locally installed Codex CLI: the configured launcher is
// spawned as `app-server --stdio` and spoken to with newline-delimited
// JSON-RPC (initialize → initialized → account/rateLimits/read). The CLI
// owns the ChatGPT authentication; AI Monitor never reads Codex auth or
// config files, never calls ChatGPT backend endpoints itself, and never
// consumes reset credits or calls any other account method.
package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.skobk.in/skobkin/aimonitor/internal/config"
)

// DiscoveryState classifies the outcome of resolving the configured
// launcher. The editor surfaces the states before any connection test is
// run; the adapter maps them onto its config-error messages.
type DiscoveryState string

const (
	// DiscoveryFound means the launcher resolved to an executable file.
	DiscoveryFound DiscoveryState = "found"
	// DiscoveryNotFoundInPath means a bare name was not found on the
	// effective PATH.
	DiscoveryNotFoundInPath DiscoveryState = "not-found-in-path"
	// DiscoveryExplicitMissing means an explicit path does not exist.
	DiscoveryExplicitMissing DiscoveryState = "explicit-missing"
	// DiscoveryNotExecutable means the resolved file is not an executable
	// regular file.
	DiscoveryNotExecutable DiscoveryState = "not-executable"
)

// Discovery is the resolved launcher state for one Codex configuration.
type Discovery struct {
	State DiscoveryState
	// Path is the resolved executable path; empty unless State is
	// DiscoveryFound.
	Path string
}

// OK reports whether a launcher was resolved.
func (d Discovery) OK() bool { return d.State == DiscoveryFound }

// Discover resolves the configured launcher without starting it. Bare names
// are searched on the effective PATH (the parent environment with the
// instance's overrides applied, which may itself override PATH); explicit
// paths are checked directly. The lookup is deliberately cheap — pure
// filesystem checks, no process is started (docs/S6_IMPLEMENTATION_SPEC.md
// §3).
func Discover(settings config.CodexSettings) Discovery {
	exe := settings.EffectiveCodexExecutable()
	if strings.ContainsRune(exe, '/') || strings.ContainsRune(exe, filepath.Separator) {
		state := checkExecutable(exe)
		if state == DiscoveryFound {
			return Discovery{State: DiscoveryFound, Path: exe}
		}

		return Discovery{State: state}
	}

	for _, dir := range effectivePATH(settings) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, exe)
		if checkExecutable(candidate) != DiscoveryFound {
			continue
		}
		// The result must be absolute before it reaches the exec layer: a
		// PATH entry like "." loses its prefix in Join's cleaning and would
		// come back a bare name, which the exec layer would look up again —
		// against the inherited PATH instead of the effective one.
		abs, err := filepath.Abs(candidate)
		if err != nil {
			abs = candidate
		}

		return Discovery{State: DiscoveryFound, Path: abs}
	}

	return Discovery{State: DiscoveryNotFoundInPath}
}

// checkExecutable reports whether path is an existing regular file with an
// execute bit. Existence and executability get distinct states so the editor
// can name the actual problem.
func checkExecutable(path string) DiscoveryState {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return DiscoveryExplicitMissing
	case err != nil:
		return DiscoveryNotExecutable
	case info.IsDir():
		return DiscoveryNotExecutable
	case info.Mode()&0o111 == 0:
		return DiscoveryNotExecutable
	}

	return DiscoveryFound
}

// effectiveEnv builds the child process environment: the parent environment
// with the instance's overrides applied. An override replaces every
// inherited entry of the same name, so the child sees exactly one value per
// overridden name and the override always wins — appending would not, since
// environment readers take the first match. Values are literal — no shell
// and no ${...} expansion ever happens — and are never logged.
func effectiveEnv(settings config.CodexSettings) []string {
	env := os.Environ()
	for _, override := range settings.Env {
		if override.Name == "" {
			continue
		}
		entry := override.Name + "=" + override.Value
		replaced := false
		filtered := env[:0]
		for _, existing := range env {
			if name, _, ok := strings.Cut(existing, "="); ok && name == override.Name {
				if !replaced {
					filtered = append(filtered, entry)
					replaced = true
				}

				continue
			}
			filtered = append(filtered, existing)
		}
		if !replaced {
			filtered = append(filtered, entry)
		}
		env = filtered
	}

	return env
}

// effectivePATH returns the PATH of the effective environment, where a PATH
// override wins over the inherited one. A bare executable name must be
// resolved against this PATH — the ordinary exec.LookPath reads the
// inherited process environment and would silently ignore an override.
func effectivePATH(settings config.CodexSettings) []string {
	for _, entry := range effectiveEnv(settings) {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name != "PATH" {
			continue
		}

		return strings.Split(value, string(os.PathListSeparator))
	}

	return nil
}

// spawnArgv is the exact argument vector AI Monitor appends to every Codex
// launcher. There is deliberately no shell and no way to inject extra
// arguments: custom launch chains belong in a wrapper executable.
func spawnArgv() []string {
	return []string{"app-server", "--stdio"}
}

// spawnError describes why a launcher could not be started.
type spawnError struct {
	state DiscoveryState
	err   error
}

func (e *spawnError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %v", e.state, e.err)
	}

	return string(e.state)
}
