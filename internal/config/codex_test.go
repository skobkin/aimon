package config

import (
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// validCodex is a minimal valid Codex instance for list tests.
func validCodex(id domain.InstanceID, name string) SourceInstance {
	return SourceInstance{
		ID:           id,
		Kind:         domain.KindCodex,
		Name:         name,
		Enabled:      true,
		PollInterval: DefaultCodexInterval,
		Codex:        &CodexSettings{},
	}
}

func TestCodexValidate(t *testing.T) {
	valid := []struct {
		name string
		inst SourceInstance
	}{
		{
			name: "default launcher and mode",
			inst: validCodex("cx-1", "ChatGPT Codex"),
		},
		{
			name: "explicit wrapper path and persistent mode",
			inst: SourceInstance{
				ID: "cx-2", Kind: domain.KindCodex, Name: "Codex wrapper", Enabled: true,
				PollInterval: time.Minute,
				Codex: &CodexSettings{
					Executable: "/usr/local/bin/codex-via-proxy",
					Mode:       CodexModePersistent,
				},
			},
		},
		{
			name: "environment overrides",
			inst: SourceInstance{
				ID: "cx-3", Kind: domain.KindCodex, Name: "Codex proxied", Enabled: true,
				PollInterval: DefaultCodexInterval,
				Codex: &CodexSettings{
					Env: []EnvOverride{
						{Name: "HTTP_PROXY", Value: "socks5://127.0.0.1:1080"},
						{Name: "CODEX_HOME", Value: "/tmp/codex-home"},
					},
				},
			},
		},
	}
	for _, c := range valid {
		t.Run("valid "+c.name, func(t *testing.T) {
			if errs := c.inst.Validate(); len(errs) > 0 {
				t.Errorf("unexpected errors %v", errs)
			}
		})
	}

	invalid := []struct {
		name   string
		modify func(*SourceInstance)
		field  string
	}{
		{
			name:   "unknown mode",
			modify: func(i *SourceInstance) { i.Codex.Mode = "sometimes" },
			field:  "Mode",
		},
		{
			name:   "missing settings",
			modify: func(i *SourceInstance) { i.Codex = nil },
			field:  "Settings",
		},
		{
			name:   "duplicate env name",
			modify: func(i *SourceInstance) { i.Codex.Env = []EnvOverride{{Name: "A", Value: "1"}, {Name: "A", Value: "2"}} },
			field:  "Environment override 2",
		},
		{
			name:   "invalid env name",
			modify: func(i *SourceInstance) { i.Codex.Env = []EnvOverride{{Name: "NOT PORTABLE", Value: "1"}} },
			field:  "Environment override 1",
		},
		{
			name:   "empty env name",
			modify: func(i *SourceInstance) { i.Codex.Env = []EnvOverride{{Name: "", Value: "1"}} },
			field:  "Environment override 1",
		},
		{
			name:   "digit-leading env name",
			modify: func(i *SourceInstance) { i.Codex.Env = []EnvOverride{{Name: "1PATH", Value: "x"}} },
			field:  "Environment override 1",
		},
		{
			name:   "nul in env value",
			modify: func(i *SourceInstance) { i.Codex.Env = []EnvOverride{{Name: "PATH", Value: "a\x00b"}} },
			field:  "Environment override 1",
		},
	}
	for _, c := range invalid {
		t.Run("invalid "+c.name, func(t *testing.T) {
			inst := validCodex("cx-x", "x")
			c.modify(&inst)
			errs := inst.Validate()
			for _, e := range errs {
				if e.Field == c.field {
					return
				}
			}
			t.Errorf("errors %v: want a %q error", errs, c.field)
		})
	}
}

func TestCodexEffectiveDefaults(t *testing.T) {
	var zero CodexSettings
	if got := zero.EffectiveCodexExecutable(); got != DefaultCodexExecutable {
		t.Errorf("executable = %q, want %q", got, DefaultCodexExecutable)
	}
	if got := zero.EffectiveCodexMode(); got != CodexModeOnDemand {
		t.Errorf("mode = %q, want %q", got, CodexModeOnDemand)
	}
	s := CodexSettings{Executable: " /opt/wrapper ", Mode: CodexModePersistent}
	if got := s.EffectiveCodexExecutable(); got != "/opt/wrapper" {
		t.Errorf("executable = %q, want the trimmed value", got)
	}
	if got := s.EffectiveCodexMode(); got != CodexModePersistent {
		t.Errorf("mode = %q, want the configured value", got)
	}
}

func TestCodexSameSettings(t *testing.T) {
	base := CodexSettings{}
	if !SameSettings(base, CodexSettings{}) {
		t.Error("zero codex settings must compare equal")
	}
	if !SameSettings(CodexSettings{Env: []EnvOverride{{Name: "A", Value: "=v"}}}, CodexSettings{Env: []EnvOverride{{Name: "A", Value: "=v"}}}) {
		t.Error("equal env overrides must compare equal (a value containing '=' round-trips)")
	}
	if SameSettings(CodexSettings{Executable: "a"}, CodexSettings{Executable: "b"}) {
		t.Error("a changed executable must compare unequal (it rebuilds the adapter)")
	}
	if SameSettings(CodexSettings{Mode: CodexModeOnDemand}, CodexSettings{Mode: CodexModePersistent}) {
		t.Error("a changed mode must compare unequal (it rebuilds the adapter)")
	}
	if SameSettings(CodexSettings{Env: []EnvOverride{{Name: "A"}}}, CodexSettings{Env: []EnvOverride{{Name: "B"}}}) {
		t.Error("a changed env name must compare unequal")
	}
	if SameSettings(CodexSettings{Env: []EnvOverride{{Name: "A"}, {Name: "B"}}}, CodexSettings{Env: []EnvOverride{{Name: "B"}, {Name: "A"}}}) {
		t.Error("reordered overrides are a configuration change and must compare unequal")
	}
	if SameSettings(base, MiniMaxSettings{}) {
		t.Error("codex must not compare equal to another empty kind")
	}
}

func TestCodexIntervals(t *testing.T) {
	if got := KindIntervals(domain.KindCodex); !sameDurations(got, CodexIntervals) {
		t.Errorf("KindIntervals(codex) = %v, want the codex ladder", got)
	}
	for _, d := range CodexIntervals {
		if d < time.Minute {
			t.Errorf("preset %v is sub-minute; the codex ladder must not offer it", d)
		}
	}
	if DefaultCodexInterval != 5*time.Minute {
		t.Errorf("DefaultCodexInterval = %v, want 5m", DefaultCodexInterval)
	}
	inst := validCodex("cx-i", "i")
	if errs := inst.Validate(); len(errs) != 0 {
		t.Errorf("default interval must validate, got %v", errs)
	}
}

func sameDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestCodexRoundTrip(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	stored := []SourceInstance{
		validCodex("cx-round-1", "ChatGPT Codex"),
		{
			ID: "cx-round-2", Kind: domain.KindCodex, Name: "Codex wrapper", Enabled: false,
			PollInterval: 10 * time.Minute,
			Codex: &CodexSettings{
				Executable: "/usr/local/bin/codex-via-graftcp",
				Mode:       CodexModePersistent,
				Env: []EnvOverride{
					{Name: "ALL_PROXY", Value: "socks5h://user=pass@proxy.lan:1080"},
					{Name: "NO_PROXY", Value: "127.0.0.1,::1"},
				},
			},
		},
	}
	if err := repo.SaveInstances(stored); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := repo.LoadInstances()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != len(stored) {
		t.Fatalf("loaded %d instances, want %d", len(loaded), len(stored))
	}
	for i := range stored {
		if !SameSettings(mustSettings(t, stored[i]), mustSettings(t, loaded[i])) {
			t.Errorf("instance %d settings = %#v, want %#v", i, mustSettings(t, loaded[i]), mustSettings(t, stored[i]))
		}
		if loaded[i].PollInterval != stored[i].PollInterval {
			t.Errorf("instance %d interval = %v, want %v", i, loaded[i].PollInterval, stored[i].PollInterval)
		}
		if loaded[i].Enabled != stored[i].Enabled {
			t.Errorf("instance %d enabled = %v, want %v", i, loaded[i].Enabled, stored[i].Enabled)
		}
	}
	// A value containing '=' must survive verbatim: everything after the
	// first '=' belongs to the value.
	proxy := loaded[1].Codex.Env[0].Value
	if !strings.HasPrefix(proxy, "socks5h://user=pass@") {
		t.Errorf("env value = %q, want the full literal including the inner '='", proxy)
	}
}

func mustSettings(t *testing.T, inst SourceInstance) ProviderSettings {
	t.Helper()
	s, ok := inst.Settings()
	if !ok {
		t.Fatalf("instance %s has no settings block", inst.ID)
	}

	return s
}

func TestCodexLoadNormalizesInterval(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)
	// A document saved with the shared remote presets (30s is not offered to
	// codex) must still load, snapped onto the nearest codex preset.
	raw := `{"schema_version":1,"sources":[{"id":"cx-old","kind":"codex","name":"Codex","enabled":true,"poll_interval":"30s","codex":{}}]}`
	prefs.SetString(keySourceDocument, raw)

	list, err := repo.LoadInstances()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("loaded %d instances, want 1", len(list))
	}
	if list[0].PollInterval != time.Minute {
		t.Errorf("interval = %v, want the nearest codex preset 1m", list[0].PollInterval)
	}
	if errs := list[0].Validate(); len(errs) != 0 {
		t.Errorf("normalized instance must validate, got %v", errs)
	}
}
