// Package config holds the non-secret application configuration: the
// ordered list of configured source instances and their provider settings.
// Credentials are deliberately absent — they live only in the system
// keychain (DECISIONS D12).
package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Default endpoints (UI_UX §13 defaults), reused as new-instance defaults
// by the registry.
const (
	DefaultAMDGPUWebURL = "http://127.0.0.1:8080"
	DefaultLlamaSwapURL = "http://127.0.0.1:8081"
	DefaultHindsightURL = "http://127.0.0.1:8888"
	DefaultZAIURL       = "https://api.z.ai"
)

// Polling presets and defaults (DECISIONS D8). Free-text interval entry is
// forbidden; users pick from these lists. The lists are per kind, not per
// category: Hindsight is a memory-service aggregate rather than GPU
// telemetry, so its ladder starts one step higher — re-polling an aggregate
// that only changes when the memory bank does wastes requests (2026-08-29).
var (
	LocalIntervals  = []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}
	RemoteIntervals = []time.Duration{30 * time.Second, 1 * time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

	// HindsightIntervals is the shifted-up ladder for the Hindsight kind
	// only; the other local kinds keep LocalIntervals.
	HindsightIntervals = []time.Duration{10 * time.Second, 15 * time.Second, 30 * time.Second, 1 * time.Minute, 5 * time.Minute}

	// CodexIntervals is the Codex kind's own ladder: every on-demand refresh
	// spawns a Codex app-server process (measurable local bookkeeping), so
	// sub-minute polling is not offered; persistent users may still pick 1m
	// (docs/S6_AGENT_RULES.md §15).
	CodexIntervals = []time.Duration{1 * time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

	DefaultLocalInterval     = 5 * time.Second
	DefaultHindsightInterval = 10 * time.Second
	DefaultRemoteInterval    = 60 * time.Second
	DefaultCodexInterval     = 5 * time.Minute

	// PaceWindows are the offered usage-pace estimation windows (S7): how
	// much recent history the pace average spans. They are evenly divisible
	// by every remote poll-interval preset so a window boundary aligns with
	// whole samples.
	PaceWindows = []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 60 * time.Minute}
	// DefaultPaceWindow balances smoothing against reaction time: long
	// enough to average a burst, short enough to follow the user starting
	// or stopping agents within minutes (the idle rule handles the stop
	// case regardless).
	DefaultPaceWindow = 20 * time.Minute
)

// ValidationError describes one invalid field.
type ValidationError struct {
	Source  string
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Source, e.Field, e.Message)
}

func validateURL(source, raw string) []ValidationError {
	if raw == "" {
		return []ValidationError{{Source: source, Field: "URL", Message: "must not be empty"}}
	}

	return validateURLField(source, "URL", raw)
}

// validateOptionalURL validates a URL field that may legitimately be left
// empty; a non-empty value must still be a usable http(s) URL.
func validateOptionalURL(source, raw string) []ValidationError {
	return validateURLField(source, "Web UI URL", raw)
}

func validateURLField(source, field, raw string) []ValidationError {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return []ValidationError{{Source: source, Field: field, Message: "must be an http(s) URL with a host"}}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return []ValidationError{{Source: source, Field: field, Message: "scheme must be http or https"}}
	}

	return nil
}

func validateInterval(source string, d time.Duration, presets []time.Duration) []ValidationError {
	for _, p := range presets {
		if d == p {
			return nil
		}
	}

	return []ValidationError{{Source: source, Field: "Poll interval", Message: fmt.Sprintf("must be one of %s", intervalList(presets))}}
}

func intervalList(presets []time.Duration) string {
	parts := make([]string, len(presets))
	for i, p := range presets {
		parts[i] = IntervalLabel(p)
	}

	return strings.Join(parts, ", ")
}

// ValidateEnvOverrides checks a Codex instance's environment override list:
// portable variable names, no duplicates, no NUL bytes in names or values,
// and no blank entries. Values are literal — no shell or ${...} expansion
// ever happens — and are stored as ordinary non-secret configuration, which
// is why users are told not to put secrets here (docs/S6_AGENT_RULES.md §5).
func ValidateEnvOverrides(source string, envs []EnvOverride) []ValidationError {
	var errs []ValidationError
	seen := make(map[string]bool, len(envs))
	for i, env := range envs {
		field := fmt.Sprintf("Environment override %d", i+1)
		bad := func(message string) {
			errs = append(errs, ValidationError{Source: source, Field: field, Message: message})
		}
		switch {
		case env.Name == "":
			bad("variable name must not be empty")
		case !ValidEnvName(env.Name):
			bad(fmt.Sprintf("%q is not a portable variable name", env.Name))
		case seen[env.Name]:
			bad(fmt.Sprintf("variable %s is set more than once", env.Name))
		case strings.ContainsRune(env.Value, 0):
			bad(fmt.Sprintf("value of %s must not contain NUL", env.Name))
		}
		seen[env.Name] = true
	}

	return errs
}

// ValidEnvName reports whether name is a portable environment variable name
// ([A-Za-z_][A-Za-z0-9_]*). Checked by hand: the rule is one small character
// class, and a regexp would be heavier than the rule. The settings editor
// reuses it for live per-row validation.
func ValidEnvName(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}

	return len(name) > 0
}

// IntervalLabel renders a polling interval for display ("5s" … "60s",
// "1m" … "10m"). Preset values get compact labels; anything else falls back
// to Duration.String().
func IntervalLabel(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}

	return d.String()
}
