// Package config holds the non-secret application configuration: which
// sources are enabled, where they live, and how often they are polled.
// Credentials are deliberately absent — they live only in the system
// keychain (DECISIONS D12).
package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Default endpoints (UI_UX §13 defaults).
const (
	DefaultAMDGPUWebURL = "http://127.0.0.1:8080"
	DefaultLlamaSwapURL = "http://127.0.0.1:8081"
	DefaultZAIURL       = "https://api.z.ai"
)

// Polling presets and defaults (DECISIONS D8). Free-text interval entry is
// forbidden; users pick from these lists.
var (
	LocalIntervals  = []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}
	RemoteIntervals = []time.Duration{30 * time.Second, 1 * time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

	DefaultLocalInterval  = 5 * time.Second
	DefaultRemoteInterval = 60 * time.Second
)

// AMDGPUWeb configures the local amdgputop-web telemetry source.
type AMDGPUWeb struct {
	Enabled       bool
	BaseURL       string
	SelectedGPUID string
	PollInterval  time.Duration
}

// LlamaSwap configures the local llama-swap source. An API key, when used,
// is stored in the keychain under credentials.AccountLlamaSwap — never here.
type LlamaSwap struct {
	Enabled      bool
	BaseURL      string
	PollInterval time.Duration
}

// MiniMax configures the MiniMax Token Plan source. The endpoint is the
// fixed official one, so there is no URL setting.
type MiniMax struct {
	Enabled      bool
	PollInterval time.Duration
}

// ZAI configures the Z.AI / GLM Coding Plan source. The base URL is
// configurable to allow the bigmodel.cn mirror.
type ZAI struct {
	Enabled      bool
	BaseURL      string
	PollInterval time.Duration
}

// Config is the whole non-secret configuration. It is comparable, which the
// app exploits for per-source change detection on apply.
type Config struct {
	AMDGPUWeb AMDGPUWeb
	LlamaSwap LlamaSwap
	MiniMax   MiniMax
	ZAI       ZAI
}

// Default returns the configuration used when nothing has been stored:
// correct endpoints and intervals, every source disabled.
func Default() Config {
	return Config{
		AMDGPUWeb: AMDGPUWeb{
			BaseURL:      DefaultAMDGPUWebURL,
			PollInterval: DefaultLocalInterval,
		},
		LlamaSwap: LlamaSwap{
			BaseURL:      DefaultLlamaSwapURL,
			PollInterval: DefaultLocalInterval,
		},
		MiniMax: MiniMax{
			PollInterval: DefaultRemoteInterval,
		},
		ZAI: ZAI{
			BaseURL:      DefaultZAIURL,
			PollInterval: DefaultRemoteInterval,
		},
	}
}

// ValidationError describes one invalid field.
type ValidationError struct {
	Source  string
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Source, e.Field, e.Message)
}

// Validate checks every field (the settings UI validates the whole form on
// save). URLs must be http/https with a host; intervals must be one of the
// offered presets for their source class.
func (c Config) Validate() []ValidationError {
	var errs []ValidationError

	errs = append(errs, validateURL("amdgputop-web", c.AMDGPUWeb.BaseURL)...)
	errs = append(errs, validateURL("llama-swap", c.LlamaSwap.BaseURL)...)
	errs = append(errs, validateURL("Z.AI / GLM", c.ZAI.BaseURL)...)

	errs = append(errs, validateInterval("amdgputop-web", c.AMDGPUWeb.PollInterval, LocalIntervals)...)
	errs = append(errs, validateInterval("llama-swap", c.LlamaSwap.PollInterval, LocalIntervals)...)
	errs = append(errs, validateInterval("MiniMax", c.MiniMax.PollInterval, RemoteIntervals)...)
	errs = append(errs, validateInterval("Z.AI / GLM", c.ZAI.PollInterval, RemoteIntervals)...)

	return errs
}

// Sanitize returns a copy with whitespace trimmed, trailing slashes removed
// from URLs, and any invalid value replaced by its default. It runs on
// every load so stored garbage can never wedge the app.
func (c Config) Sanitize() Config {
	def := Default()

	c.AMDGPUWeb.BaseURL = sanitizeURL(c.AMDGPUWeb.BaseURL, def.AMDGPUWeb.BaseURL)
	c.AMDGPUWeb.SelectedGPUID = strings.TrimSpace(c.AMDGPUWeb.SelectedGPUID)
	c.AMDGPUWeb.PollInterval = sanitizeInterval(c.AMDGPUWeb.PollInterval, LocalIntervals, def.AMDGPUWeb.PollInterval)

	c.LlamaSwap.BaseURL = sanitizeURL(c.LlamaSwap.BaseURL, def.LlamaSwap.BaseURL)
	c.LlamaSwap.PollInterval = sanitizeInterval(c.LlamaSwap.PollInterval, LocalIntervals, def.LlamaSwap.PollInterval)

	c.MiniMax.PollInterval = sanitizeInterval(c.MiniMax.PollInterval, RemoteIntervals, def.MiniMax.PollInterval)

	c.ZAI.BaseURL = sanitizeURL(c.ZAI.BaseURL, def.ZAI.BaseURL)
	c.ZAI.PollInterval = sanitizeInterval(c.ZAI.PollInterval, RemoteIntervals, def.ZAI.PollInterval)

	return c
}

func validateURL(source, raw string) []ValidationError {
	if raw == "" {
		return []ValidationError{{Source: source, Field: "URL", Message: "must not be empty"}}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return []ValidationError{{Source: source, Field: "URL", Message: "must be an http(s) URL with a host"}}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return []ValidationError{{Source: source, Field: "URL", Message: "scheme must be http or https"}}
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

func sanitizeURL(raw, def string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimRight(trimmed, "/")
	if trimmed == "" {
		return def
	}
	if u, err := url.Parse(trimmed); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return def
	}
	return trimmed
}

func sanitizeInterval(d time.Duration, presets []time.Duration, def time.Duration) time.Duration {
	for _, p := range presets {
		if d == p {
			return d
		}
	}
	return def
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
