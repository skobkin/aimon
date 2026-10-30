package config

import (
	"testing"
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
