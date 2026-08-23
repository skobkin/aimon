package resources

import "testing"

func TestIconEmbedded(t *testing.T) {
	r := Icon()
	if r.Name() != "icon.svg" {
		t.Errorf("name = %q, want icon.svg", r.Name())
	}
	if len(r.Content()) == 0 {
		t.Error("embedded icon is empty")
	}
}

func TestIconStableAcrossCalls(t *testing.T) {
	if Icon() != Icon() {
		t.Error("Icon must return the same resource instance")
	}
}
