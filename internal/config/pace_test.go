package config

import (
	"strings"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// validMiniMax is a minimal valid MiniMax instance for pace tests.
func validMiniMax(id domain.InstanceID, name string) SourceInstance {
	return SourceInstance{
		ID:           id,
		Kind:         domain.KindMiniMax,
		Name:         name,
		Enabled:      true,
		PollInterval: DefaultRemoteInterval,
		MiniMax:      &MiniMaxSettings{},
	}
}

// Pace is a quota-source feature: the three subscription providers plus
// OpenRouter's per-key spend. Local kinds are excluded entirely.
func TestPaceCapable(t *testing.T) {
	cases := []struct {
		kind domain.SourceKind
		want bool
	}{
		{domain.KindMiniMax, true},
		{domain.KindZAI, true},
		{domain.KindCodex, true},
		{domain.KindOpenRouter, true},
		{domain.KindAMDGPUWeb, false},
		{domain.KindLlamaSwap, false},
		{domain.KindHindsight, false},
		{domain.SourceKind("nope"), false},
	}
	for _, tc := range cases {
		if got := PaceCapable(tc.kind); got != tc.want {
			t.Errorf("PaceCapable(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// The default sits on the ladder, and every preset is a whole number of
// minutes so a window boundary aligns with whole remote poll samples.
func TestPaceWindowPresets(t *testing.T) {
	onList := false
	for _, p := range PaceWindows {
		if p%time.Minute != 0 {
			t.Errorf("preset %v is not whole minutes", p)
		}
		if p == DefaultPaceWindow {
			onList = true
		}
	}
	if !onList {
		t.Errorf("default pace window %v is not an offered preset", DefaultPaceWindow)
	}
	for i := 1; i < len(PaceWindows); i++ {
		if PaceWindows[i-1] >= PaceWindows[i] {
			t.Fatalf("presets not ascending: %v", PaceWindows)
		}
	}
}

// Zero (never configured) and every preset are valid; anything else a capable
// kind stores is rejected with the preset list in the message.
func TestPaceWindowValidate(t *testing.T) {
	base := validMiniMax("mm-pace", "MiniMax")

	for _, zero := range []time.Duration{0, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 60 * time.Minute} {
		inst := base
		inst.PaceWindow = zero
		if errs := inst.Validate(); len(errs) != 0 {
			t.Errorf("PaceWindow %v: unexpected errors %v", zero, errs)
		}
	}

	inst := base
	inst.PaceWindow = 15 * time.Minute
	errs := inst.Validate()
	if len(errs) != 1 {
		t.Fatalf("off-list pace window errors = %v, want exactly one", errs)
	}
	if errs[0].Field != "Pace window" || !strings.Contains(errs[0].Message, "10m") {
		t.Errorf("error = %v, want the Pace window field and the preset list", errs[0])
	}

	// A local kind's pace window is never read: garbage in the field is not
	// a validation error and must not break saving an otherwise valid source.
	local := validAMDGPU("gpu-pace", "GPU", "http://127.0.0.1:8080")
	local.PaceWindow = 999 * time.Minute
	if errs := local.Validate(); len(errs) != 0 {
		t.Errorf("local kind with a stray pace window: errors %v", errs)
	}
}

// Load-time snapping mirrors the interval rules: zero means the default, a
// drifted value snaps to the nearest preset, and an exact tie snaps longer.
func TestNormalizePaceWindow(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{0, DefaultPaceWindow},
		{10 * time.Minute, 10 * time.Minute},
		{20 * time.Minute, 20 * time.Minute},
		{60 * time.Minute, 60 * time.Minute},
		// 15m is equidistant between 10m and 20m: toward the longer window.
		{15 * time.Minute, 20 * time.Minute},
		{45 * time.Minute, 60 * time.Minute},
		// Corrupt extremes snap to the boundary presets.
		{time.Second, 10 * time.Minute},
		{5 * time.Hour, 60 * time.Minute},
	}
	for _, tc := range cases {
		if got := normalizePaceWindow(tc.in); got != tc.want {
			t.Errorf("normalizePaceWindow(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The pace window round-trips for capable kinds only: capable records carry
// the field (absence means the default and loads back explicit), local
// records never grow it.
func TestPaceWindowRoundTrip(t *testing.T) {
	prefs := newMemPreferences()
	repo := NewRepository(prefs)

	mm := validMiniMax("mm-pace", "MiniMax")
	mm.PaceWindow = 30 * time.Minute
	local := validAMDGPU("gpu-pace", "GPU", "http://127.0.0.1:8080")
	if err := repo.SaveInstances([]SourceInstance{mm, local}); err != nil {
		t.Fatalf("save: %v", err)
	}

	doc := prefs.String(keySourceDocument)
	if !strings.Contains(doc, `"pace_window": "30m0s"`) {
		t.Errorf("document lacks the pace window:\n%s", doc)
	}
	if strings.Contains(doc, "gpu-pace") && strings.Count(doc, "pace_window") != 1 {
		t.Errorf("a local source record carries a pace window:\n%s", doc)
	}

	loaded, err := repo.LoadInstances()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byID := map[domain.InstanceID]SourceInstance{}
	for _, inst := range loaded {
		byID[inst.ID] = inst
	}
	if got := byID["mm-pace"].PaceWindow; got != 30*time.Minute {
		t.Errorf("loaded pace window = %v, want 30m", got)
	}
	// Absence on the wire means the default, made explicit on load.
	if got := byID["gpu-pace"].Kind; got != domain.KindAMDGPUWeb {
		t.Fatalf("loaded local kind = %q", got)
	}
	defaulted := validMiniMax("mm-default", "MiniMax default")
	if err := repo.SaveInstances([]SourceInstance{defaulted}); err != nil {
		t.Fatalf("save zero-window instance: %v", err)
	}
	loaded, err = repo.LoadInstances()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, inst := range loaded {
		if inst.ID == "mm-default" && inst.PaceWindow != DefaultPaceWindow {
			t.Errorf("absent pace window loaded as %v, want the explicit default", inst.PaceWindow)
		}
	}
}

// A drifted stored value snaps on load (in-memory) and only a valid list
// saves — the offered presets are never widened behind the user's back.
func TestPaceWindowLoadSnapsAndSaveRefusesOffList(t *testing.T) {
	raw := `{"schema_version":1,"sources":[{"id":"mm-drift","kind":"minimax","name":"MiniMax","enabled":true,"poll_interval":"1m0s","pace_window":"18m0s","minimax":{}}]}`
	prefs := newMemPreferences()
	prefs.SetString(keySourceDocument, raw)

	loaded, err := NewRepository(prefs).LoadInstances()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].PaceWindow != 20*time.Minute {
		t.Fatalf("loaded = %+v, want the 18m value snapped to 20m", loaded)
	}
	if stored := prefs.String(keySourceDocument); stored != raw {
		t.Errorf("load rewrote the stored document")
	}

	drifted := validMiniMax("mm-drift", "MiniMax")
	drifted.PaceWindow = 18 * time.Minute
	if err := NewRepository(prefs).SaveInstances([]SourceInstance{drifted}); err == nil {
		t.Fatal("saving an off-list pace window must fail")
	}
}
