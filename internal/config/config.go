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

	DefaultLocalInterval     = 5 * time.Second
	DefaultHindsightInterval = 10 * time.Second
	DefaultRemoteInterval    = 60 * time.Second
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

// IntervalLabel renders a polling interval for display ("5s" … "60s",
// "1m" … "10m"). Preset values get compact labels; anything else falls back
// to Duration.String().
func IntervalLabel(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.String()
}
