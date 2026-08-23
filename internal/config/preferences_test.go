package config

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// memPreferences is a map-backed fake of the Fyne preferences surface the
// repository depends on. Tests never touch the real preference store.
type memPreferences struct {
	data map[string]any
}

func newMemPreferences() *memPreferences {
	return &memPreferences{data: map[string]any{}}
}

func (p *memPreferences) Bool(key string) bool {
	v, _ := p.data[key].(bool)
	return v
}

func (p *memPreferences) String(key string) string {
	v, _ := p.data[key].(string)
	return v
}

func (p *memPreferences) SetBool(key string, value bool) {
	p.data[key] = value
}

func (p *memPreferences) SetString(key string, value string) {
	p.data[key] = value
}

func (p *memPreferences) RemoveValue(key string) {
	delete(p.data, key)
}

func TestRepositoryRoundTrip(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	// Empty store yields sanitized defaults.
	if cfg := repo.Load(); cfg != Default() {
		t.Fatalf("empty store must produce defaults, got %+v", cfg)
	}

	stored := Config{
		AMDGPUWeb: AMDGPUWeb{
			Enabled:       true,
			BaseURL:       "http://192.168.1.10:8080",
			SelectedGPUID: "gpu-42",
			PollInterval:  15 * time.Second,
		},
		LlamaSwap: LlamaSwap{
			Enabled:      true,
			BaseURL:      "http://127.0.0.1:8081",
			PollInterval: 30 * time.Second,
		},
		MiniMax: MiniMax{Enabled: true, PollInterval: 2 * time.Minute},
		ZAI:     ZAI{Enabled: false, BaseURL: "https://open.bigmodel.cn", PollInterval: 5 * time.Minute},
	}
	repo.Save(stored)

	loaded := repo.Load()
	if loaded != stored {
		t.Fatalf("round-trip mismatch:\n stored: %+v\n loaded: %+v", stored, loaded)
	}
}

func TestRepositoryCorruptIntervalFallsBack(t *testing.T) {
	prefs := newMemPreferences()
	prefs.SetString(keySwapEvery, "not-a-duration")
	prefs.SetString(keyMiniMaxEvery, "7s") // valid duration, not a preset

	cfg := NewRepository(prefs).Load()

	if cfg.LlamaSwap.PollInterval != DefaultLocalInterval {
		t.Errorf("unparseable interval must fall back to default, got %v", cfg.LlamaSwap.PollInterval)
	}
	if cfg.MiniMax.PollInterval != DefaultRemoteInterval {
		t.Errorf("non-preset interval must sanitize to default, got %v", cfg.MiniMax.PollInterval)
	}
}

func TestStableKeyList(t *testing.T) {
	// The exact set of preference keys ever written. Adding a key is a
	// compatibility decision; changing or removing one silently discards
	// user settings.
	want := []string{
		"source.amdgputop-web.enabled",
		"source.amdgputop-web.gpu",
		"source.amdgputop-web.interval",
		"source.amdgputop-web.url",
		"source.llama-swap.enabled",
		"source.llama-swap.interval",
		"source.llama-swap.url",
		"source.minimax.enabled",
		"source.minimax.interval",
		"source.zai.enabled",
		"source.zai.interval",
		"source.zai.url",
	}

	prefs := newMemPreferences()
	NewRepository(prefs).Save(Default())

	got := make([]string, 0, len(prefs.data))
	for k := range prefs.data {
		got = append(got, k)
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("key set mismatch: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// No key may look like it stores a secret.
	for _, k := range got {
		for _, fragment := range []string{"key", "token", "secret", "password"} {
			if strings.Contains(k, fragment) {
				t.Errorf("preference key %q looks credential-related", k)
			}
		}
	}
}
