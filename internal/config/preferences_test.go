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
	if len(prefs.data) != 1 {
		t.Fatalf("keys written = %v, want only the document key", keysOf(prefs))
	}
	if _, err := repo.LoadInstances(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(prefs.data) != 1 {
		t.Fatalf("load must not add keys, got %v", keysOf(prefs))
	}
}

func keysOf(p *memPreferences) []string {
	out := make([]string, 0, len(p.data))
	for k := range p.data {
		out = append(out, k)
	}
	return out
}
