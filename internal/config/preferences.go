package config

import (
	"time"
)

// Preferences is the storage backend the repository needs. It is satisfied
// structurally by fyne.Preferences, which keeps this package Fyne-free and
// trivially fake-able in tests.
type Preferences interface {
	Bool(key string) bool
	String(key string) string
	SetBool(key string, value bool)
	SetString(key string, value string)
	RemoveValue(key string)
}

// Preference keys. These are stable across releases; renaming one silently
// discards user settings. Defined here and nowhere else.
const (
	keyAMDGPUEnabled = "source.amdgputop-web.enabled"
	keyAMDGPUURL     = "source.amdgputop-web.url"
	keyAMDGPUGPU     = "source.amdgputop-web.gpu"
	keyAMDGPUEvery   = "source.amdgputop-web.interval"

	keySwapEnabled = "source.llama-swap.enabled"
	keySwapURL     = "source.llama-swap.url"
	keySwapEvery   = "source.llama-swap.interval"

	keyMiniMaxEnabled = "source.minimax.enabled"
	keyMiniMaxEvery   = "source.minimax.interval"

	keyZAIEnabled = "source.zai.enabled"
	keyZAIURL     = "source.zai.url"
	keyZAIEvery   = "source.zai.interval"

	// keyConfigured marks that the user has saved settings at least
	// once; until then the app treats every launch as a first run.
	keyConfigured = "app.configured"
)

// Repository loads and saves the non-secret configuration through the
// Preferences interface. No credential material ever passes through here.
type Repository struct {
	prefs Preferences
}

// NewRepository wraps a Preferences backend.
func NewRepository(prefs Preferences) *Repository {
	return &Repository{prefs: prefs}
}

// Load reads the stored configuration and sanitizes it. Missing values
// fall back to defaults; corrupt values are replaced, never fatal.
func (r *Repository) Load() Config {
	def := Default()

	cfg := Config{
		AMDGPUWeb: AMDGPUWeb{
			Enabled:       r.prefs.Bool(keyAMDGPUEnabled),
			BaseURL:       r.prefs.String(keyAMDGPUURL),
			SelectedGPUID: r.prefs.String(keyAMDGPUGPU),
			PollInterval:  loadInterval(r.prefs, keyAMDGPUEvery, def.AMDGPUWeb.PollInterval),
		},
		LlamaSwap: LlamaSwap{
			Enabled:      r.prefs.Bool(keySwapEnabled),
			BaseURL:      r.prefs.String(keySwapURL),
			PollInterval: loadInterval(r.prefs, keySwapEvery, def.LlamaSwap.PollInterval),
		},
		MiniMax: MiniMax{
			Enabled:      r.prefs.Bool(keyMiniMaxEnabled),
			PollInterval: loadInterval(r.prefs, keyMiniMaxEvery, def.MiniMax.PollInterval),
		},
		ZAI: ZAI{
			Enabled:      r.prefs.Bool(keyZAIEnabled),
			BaseURL:      r.prefs.String(keyZAIURL),
			PollInterval: loadInterval(r.prefs, keyZAIEvery, def.ZAI.PollInterval),
		},
	}
	return cfg.Sanitize()
}

// Save writes the full configuration. Intervals are stored as
// Duration.String().
func (r *Repository) Save(cfg Config) {
	r.prefs.SetBool(keyAMDGPUEnabled, cfg.AMDGPUWeb.Enabled)
	r.prefs.SetString(keyAMDGPUURL, cfg.AMDGPUWeb.BaseURL)
	r.prefs.SetString(keyAMDGPUGPU, cfg.AMDGPUWeb.SelectedGPUID)
	r.prefs.SetString(keyAMDGPUEvery, cfg.AMDGPUWeb.PollInterval.String())

	r.prefs.SetBool(keySwapEnabled, cfg.LlamaSwap.Enabled)
	r.prefs.SetString(keySwapURL, cfg.LlamaSwap.BaseURL)
	r.prefs.SetString(keySwapEvery, cfg.LlamaSwap.PollInterval.String())

	r.prefs.SetBool(keyMiniMaxEnabled, cfg.MiniMax.Enabled)
	r.prefs.SetString(keyMiniMaxEvery, cfg.MiniMax.PollInterval.String())

	r.prefs.SetBool(keyZAIEnabled, cfg.ZAI.Enabled)
	r.prefs.SetString(keyZAIURL, cfg.ZAI.BaseURL)
	r.prefs.SetString(keyZAIEvery, cfg.ZAI.PollInterval.String())

	r.prefs.SetBool(keyConfigured, true)
}

// Configured reports whether the user has ever saved settings. Until the
// first Save every launch is a first run (Settings opens at startup).
func (r *Repository) Configured() bool {
	return r.prefs.Bool(keyConfigured)
}

func loadInterval(prefs Preferences, key string, def time.Duration) time.Duration {
	raw := prefs.String(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return def
	}
	return d
}
