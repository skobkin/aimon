package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
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

// The repository writes exactly one preference key: the document. Old
// v0.2 keys are never read or rewritten (no migration).
func TestRepositoryWritesOnlyTheDocumentKey(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	if err := repo.SaveInstances(nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, documentWritten := prefs.data[keySourceDocument]; !documentWritten {
		t.Fatalf("keys written = %v, want only the document key", keysOf(prefs))
	}
	if len(prefs.data) != 1 {
		t.Fatalf("SaveInstances must not touch other keys, got %v", keysOf(prefs))
	}
	if _, err := repo.LoadInstances(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(prefs.data) != 1 {
		t.Fatalf("load must not add keys, got %v", keysOf(prefs))
	}
}

// HideSensitiveInfo defaults to hidden and persists both ways
// (S4_IMPLEMENTATION_SPEC.md §9). Absent preference -> true; persisted
// false -> false; toggling round-trips.
func TestHideSensitiveInfoPreference(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	if !repo.HideSensitiveInfo() {
		t.Error("absent privacy preference must default to hiding sensitive info")
	}

	repo.SetHideSensitiveInfo(false)
	if repo.HideSensitiveInfo() {
		t.Error("persisted false must read back false")
	}
	if v := prefs.Bool(keyPrivacyShowSensitive); !v {
		t.Errorf("storing hide=false must write show-sensitive=true, got %v", v)
	}

	repo.SetHideSensitiveInfo(true)
	if !repo.HideSensitiveInfo() {
		t.Error("toggled hide=true must persist")
	}

	// An external show-sensitive=false (the legacy-looking spelling) still
	// means "hide", because the default is inverted-storage.
	prefs.SetBool(keyPrivacyShowSensitive, false)
	if !repo.HideSensitiveInfo() {
		t.Error("show-sensitive=false must read as hiding")
	}
}

// CheckUpdatesAutomatically defaults to enabled and persists both ways.
// Absent preference -> true (inverted storage: skip=false); a manually
// injected skip=false still reads as enabled.
func TestCheckUpdatesAutomaticallyPreference(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	if !repo.CheckUpdatesAutomatically() {
		t.Error("absent updates preference must default to automatic checks enabled")
	}

	repo.SetCheckUpdatesAutomatically(false)
	if repo.CheckUpdatesAutomatically() {
		t.Error("persisted false must read back false")
	}
	if v := prefs.Bool(keyUpdatesSkipAutomaticChecks); !v {
		t.Errorf("storing automatic=false must write skip-automatic-checks=true, got %v", v)
	}

	repo.SetCheckUpdatesAutomatically(true)
	if !repo.CheckUpdatesAutomatically() {
		t.Error("toggled automatic=true must persist")
	}
	if v := prefs.Bool(keyUpdatesSkipAutomaticChecks); v {
		t.Errorf("storing automatic=true must write skip-automatic-checks=false, got %v", v)
	}

	// An external skip=false still means "checks on", because the default is
	// inverted-storage.
	prefs.SetBool(keyUpdatesSkipAutomaticChecks, false)
	if !repo.CheckUpdatesAutomatically() {
		t.Error("skip-automatic-checks=false must read as enabled")
	}
}

// SourceDocument exposes exactly the stored JSON, and an export→import
// round-trip reconstructs the same instance list. An empty store exposes
// "" (the export path substitutes the minimal document).
func TestSourceDocumentRoundTrip(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	if got := repo.SourceDocument(); got != "" {
		t.Errorf("SourceDocument on an empty store = %q, want empty", got)
	}

	list := []SourceInstance{
		{
			ID:           "gpu-a",
			Kind:         domain.KindAMDGPUWeb,
			Name:         "GPU A",
			Enabled:      true,
			PollInterval: 30 * time.Second,
			AMDGPUWeb:    &AMDGPUWebSettings{BaseURL: "http://127.0.0.1:49001", SelectedGPUID: "amdgpu0"},
		},
		{
			ID:           "mm-1",
			Kind:         domain.KindMiniMax,
			Name:         "MiniMax A",
			Enabled:      false,
			PollInterval: time.Minute,
			MiniMax:      &MiniMaxSettings{},
		},
	}
	if err := repo.SaveInstances(list); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw := repo.SourceDocument()
	if raw == "" || raw != prefs.String(keySourceDocument) {
		t.Fatal("SourceDocument must expose exactly the stored JSON")
	}
	back, err := ParseSourceDocument(raw)
	if err != nil {
		t.Fatalf("ParseSourceDocument: %v", err)
	}
	if len(back) != len(list) || back[0].Name != "GPU A" || back[1].Name != "MiniMax A" {
		t.Errorf("round-trip = %+v, want the saved list", back)
	}
	if back[0].AMDGPUWeb == nil || back[0].AMDGPUWeb.SelectedGPUID != "amdgpu0" {
		t.Errorf("round-trip lost provider settings: %+v", back[0])
	}
}

// ParseSourceDocument is the load path's gatekeeper: bad JSON, a foreign
// schema version, and invalid instance lists are all rejected with a
// reason.
func TestParseSourceDocumentRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"bad json", "{not json"},
		{"wrong schema", `{"schema_version":99,"sources":[]}`},
		{"unknown kind", `{"schema_version":1,"sources":[{"id":"x","kind":"nope","name":"X","enabled":true,"poll_interval":"1m"}]}`},
		{"bad interval", `{"schema_version":1,"sources":[{"id":"x","kind":"minimax","name":"X","enabled":true,"poll_interval":"nope"}]}`},
	}
	for _, c := range cases {
		if _, err := ParseSourceDocument(c.raw); err == nil {
			t.Errorf("%s: ParseSourceDocument accepted the document", c.name)
		}
	}

	// An empty source list is the valid zero-source document.
	if _, err := ParseSourceDocument(`{"schema_version":1,"sources":[]}`); err != nil {
		t.Errorf("empty document rejected: %v", err)
	}
}

// The exported document carries no credential-shaped field names. This is
// a structural guard over the secret-free claim: if a future schema adds a
// field that could hold a secret, this test fails the export feature
// before it can leak.
func TestSourceDocumentHasNoCredentialFields(t *testing.T) {
	// One record per kind, every provider block populated.
	list := []SourceInstance{
		{ID: "g", Kind: domain.KindAMDGPUWeb, Name: "G", Enabled: true, PollInterval: 30 * time.Second, AMDGPUWeb: &AMDGPUWebSettings{BaseURL: "http://h", SelectedGPUID: "gpu0"}},
		{ID: "l", Kind: domain.KindLlamaSwap, Name: "L", Enabled: true, PollInterval: 30 * time.Second, LlamaSwap: &LlamaSwapSettings{BaseURL: "http://h"}},
		{ID: "h", Kind: domain.KindHindsight, Name: "H", Enabled: true, PollInterval: 30 * time.Second, Hindsight: &HindsightSettings{BaseURL: "http://h", WebUIURL: "http://h/ui"}},
		{ID: "m", Kind: domain.KindMiniMax, Name: "M", Enabled: true, PollInterval: time.Minute, MiniMax: &MiniMaxSettings{}},
		{ID: "z", Kind: domain.KindZAI, Name: "Z", Enabled: true, PollInterval: time.Minute, ZAI: &ZAISettings{BaseURL: "http://h"}},
		{ID: "o", Kind: domain.KindOpenRouter, Name: "O", Enabled: true, PollInterval: time.Minute, OpenRouter: &OpenRouterSettings{}},
		{ID: "c", Kind: domain.KindCodex, Name: "C", Enabled: true, PollInterval: time.Minute, Codex: &CodexSettings{Executable: "codex", Mode: CodexModePersistent, Env: []EnvOverride{{Name: "EDITOR", Value: "vi"}}}},
	}
	repo := NewRepository(newMemPreferences())
	if err := repo.SaveInstances(list); err != nil {
		t.Fatalf("save: %v", err)
	}

	var walk func(v any)
	walk = func(v any) {
		switch val := v.(type) {
		case map[string]any:
			for k, sub := range val {
				lk := strings.ToLower(k)
				for _, banned := range []string{"secret", "token", "password", "apikey", "api_key", "credential"} {
					if strings.Contains(lk, banned) {
						t.Errorf("document field %q looks credential-shaped", k)
					}
				}
				walk(sub)
			}
		case []any:
			for _, sub := range val {
				walk(sub)
			}
		}
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(repo.SourceDocument()), &doc); err != nil {
		t.Fatalf("stored document is not valid JSON: %v", err)
	}
	walk(doc)
}

// The notification preferences default to enabled (all three, inverted
// storage like privacy and update checks) and persist both ways: master
// switch first, then the two per-event toggles.
func TestNotificationsPreferences(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	if !repo.NotificationsEnabled() {
		t.Error("absent master preference must default to notifications enabled")
	}
	if !repo.NotificationsQuotaLowEnabled() {
		t.Error("absent quota-low preference must default to enabled")
	}
	if !repo.NotificationsQuotaExhaustedEnabled() {
		t.Error("absent quota-exhausted preference must default to enabled")
	}

	repo.SetNotificationsEnabled(false)
	if repo.NotificationsEnabled() {
		t.Error("persisted master=false must read back false")
	}
	if v := prefs.Bool(keyNotificationsDisabled); !v {
		t.Errorf("storing enabled=false must write notifications.disabled=true, got %v", v)
	}

	repo.SetNotificationsQuotaLowEnabled(false)
	if repo.NotificationsQuotaLowEnabled() {
		t.Error("persisted quota-low=false must read back false")
	}
	if v := prefs.Bool(keyNotificationsQuotaLowDisabled); !v {
		t.Errorf("storing quota-low enabled=false must write the disabled key, got %v", v)
	}

	repo.SetNotificationsQuotaExhaustedEnabled(false)
	if repo.NotificationsQuotaExhaustedEnabled() {
		t.Error("persisted quota-exhausted=false must read back false")
	}

	// Round-trip back on; the inverted storage of each key follows its own
	// state.
	repo.SetNotificationsEnabled(true)
	repo.SetNotificationsQuotaLowEnabled(true)
	repo.SetNotificationsQuotaExhaustedEnabled(true)
	if !repo.NotificationsEnabled() || !repo.NotificationsQuotaLowEnabled() || !repo.NotificationsQuotaExhaustedEnabled() {
		t.Error("toggling all three back on must persist")
	}
}

func keysOf(p *memPreferences) []string {
	out := make([]string, 0, len(p.data))
	for k := range p.data {
		out = append(out, k)
	}

	return out
}
