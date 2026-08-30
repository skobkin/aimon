package settings

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// longStatusLine is the shape of a status/keychain message: a full sentence
// of reasoning, not a short confirmation.
const longStatusLine = "Keychain: blocked (dbus: org.freedesktop.se was not reachable: connection refused after 3 attempts) — credentials unavailable"

// singleLineWidth measures the width a label demands when it does not wrap.
func singleLineWidth(t *testing.T, text string, style fyne.TextStyle) float32 {
	t.Helper()

	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, style).MinSize().Width
}

// labelByText returns the first label in the tree carrying exactly text.
func labelByText(t *testing.T, obj fyne.CanvasObject, text string) *widget.Label {
	t.Helper()
	var found *widget.Label
	walkCanvas(t, obj, func(o fyne.CanvasObject) {
		if l, ok := o.(*widget.Label); ok && l.Text == text && found == nil {
			found = l
		}
	})

	return found
}

// The status and keychain lines carry messages of unbounded length, so they
// wrap. The status line must also get the width left over from the Add
// button: in an HBox a wrapping label would be handed its minimum width and
// collapse into a sliver.
func TestSettingsStatusLinesWrapBesideTheAddButton(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.win.Show()

	if s.status.Wrapping != fyne.TextWrapWord {
		t.Errorf("status wrapping = %v, want word wrap", s.status.Wrapping)
	}
	if s.keychain.Wrapping != fyne.TextWrapWord {
		t.Errorf("keychain wrapping = %v, want word wrap", s.keychain.Wrapping)
	}

	s.status.SetText("Save failed: " + longStatusLine)
	s.keychain.SetText(longStatusLine)
	if w := s.status.Size().Width; w < 200 {
		t.Errorf("status line width = %.0f after a long message, want the width left of the Add button", w)
	}
	if w := s.keychain.Size().Width; w < 200 {
		t.Errorf("keychain line width = %.0f, want the full window width", w)
	}
}

// A source's name is the user's own free text: a long one wraps inside its
// row instead of widening the Settings window to the name's single line.
func TestSettingsListRowNameWraps(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	name := "amdgputop-web on the media box over the LAN"
	stub.LoadInstances = func() []config.SourceInstance {
		return []config.SourceInstance{gpu("g1", name, "http://127.0.0.1:1")}
	}
	s := NewSettings(test.NewTempApp(t), stub)
	s.refresh() // the list only; the keychain probe needs no window here

	assertWraps := func(l *widget.Label, what string) {
		t.Helper()
		if l == nil {
			t.Fatalf("%s label not found in the list", what)
		}
		if l.Wrapping != fyne.TextWrapWord {
			t.Errorf("%s wrapping = %v, want word wrap", what, l.Wrapping)
		}
		if want := singleLineWidth(t, name, l.TextStyle); s.listBox.MinSize().Width >= want {
			t.Errorf("list min width %.0f reaches %q's single-line width %.0f",
				s.listBox.MinSize().Width, name, want)
		}
	}
	assertWraps(labelByText(t, s.listBox, name), "the instance name")
}

func gpu(id, name, url string) config.SourceInstance {
	return config.SourceInstance{
		ID:           domain.InstanceID(id),
		Kind:         domain.KindAMDGPUWeb,
		Name:         name,
		Enabled:      true,
		PollInterval: 5 * time.Second,
		AMDGPUWeb:    &config.AMDGPUWebSettings{BaseURL: url},
	}
}

func mm(id, name string, enabled bool) config.SourceInstance {
	return config.SourceInstance{
		ID:           domain.InstanceID(id),
		Kind:         domain.KindMiniMax,
		Name:         name,
		Enabled:      enabled,
		PollInterval: time.Minute,
		MiniMax:      &config.MiniMaxSettings{},
	}
}

// interleaved is a list whose categories alternate; ordering operations
// must respect category boundaries without re-sorting the whole list.
func interleaved() []config.SourceInstance {
	return []config.SourceInstance{
		gpu("g1", "GPU one", "http://127.0.0.1:1"),
		mm("m1", "MiniMax one", true),
		gpu("g2", "GPU two", "http://127.0.0.1:2"),
		mm("m2", "MiniMax two", false),
	}
}

func ids(list []config.SourceInstance) []domain.InstanceID {
	out := make([]domain.InstanceID, len(list))
	for i, inst := range list {
		out[i] = inst.ID
	}

	return out
}

func TestMoveUpWithinCategorySkipsOtherKinds(t *testing.T) {
	// g2 moves above g1, jumping over the MiniMax instance between them.
	got := ids(MoveUp(interleaved(), "g2"))
	want := []domain.InstanceID{"g2", "m1", "g1", "m2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MoveUp = %v, want %v", got, want)
		}
	}
}

func TestMoveDownWithinCategorySkipsOtherKinds(t *testing.T) {
	got := ids(MoveDown(interleaved(), "m1"))
	want := []domain.InstanceID{"g1", "m2", "g2", "m1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MoveDown = %v, want %v", got, want)
		}
	}
}

func TestMoveCategoryEdgesAreNoOps(t *testing.T) {
	list := interleaved()
	if got := MoveUp(list, "g1"); len(got) != 4 || got[0].ID != "g1" {
		t.Errorf("first local must not move up: %v", ids(got))
	}
	if got := MoveUp(list, "m1"); len(got) != 4 || got[1].ID != "m1" {
		t.Errorf("first remote must not move up: %v", ids(got))
	}
	if got := MoveDown(list, "g2"); len(got) != 4 || got[2].ID != "g2" {
		t.Errorf("last local must not move down: %v", ids(got))
	}
	if got := MoveDown(list, "m2"); len(got) != 4 || got[3].ID != "m2" {
		t.Errorf("last remote must not move down: %v", ids(got))
	}
	if got := MoveUp(list, "missing"); len(got) != 4 || got[0].ID != "g1" {
		t.Errorf("unknown ID must be a no-op: %v", ids(got))
	}
}

func TestCanMoveEdges(t *testing.T) {
	list := interleaved()
	cases := []struct {
		id       domain.InstanceID
		up, down bool
	}{
		{"g1", false, true},
		{"g2", true, false},
		{"m1", false, true},
		{"m2", true, false},
		{"missing", false, false},
	}
	for _, c := range cases {
		if got := CanMoveUp(list, c.id); got != c.up {
			t.Errorf("CanMoveUp(%s) = %v, want %v", c.id, got, c.up)
		}
		if got := CanMoveDown(list, c.id); got != c.down {
			t.Errorf("CanMoveDown(%s) = %v, want %v", c.id, got, c.down)
		}
	}
}

func TestSetEnabledKeyedByID(t *testing.T) {
	list := interleaved()
	got := SetEnabled(list, "m2", true)
	if !got[3].Enabled {
		t.Error("SetEnabled must flip the target")
	}
	// A rename-simulation: the same ID with a different name still matches.
	renamed := append([]config.SourceInstance(nil), list...)
	renamed[3] = gpu("m2", "renamed", "http://x") // wrong kind, same ID: still keyed
	got = SetEnabled(renamed, "m2", false)
	if got[3].Enabled {
		t.Error("SetEnabled keys by instance ID, not name or kind")
	}
	if list[3].Enabled {
		t.Error("the source list must not be mutated in place")
	}
}

func TestRemoveInstanceRemovesExactlyOne(t *testing.T) {
	got := RemoveInstance(interleaved(), "m1")
	if len(got) != 3 {
		t.Fatalf("RemoveInstance left %d entries", len(got))
	}
	for _, inst := range got {
		if inst.ID == "m1" {
			t.Error("the target survived")
		}
	}
	if got := RemoveInstance(interleaved(), "missing"); len(got) != 4 {
		t.Errorf("removing an unknown ID changed the list: %v", ids(got))
	}
}

func TestReplaceInstanceEditsInPlaceAndAppendsNew(t *testing.T) {
	list := interleaved()
	edited := list[1]
	edited.Name = "MiniMax renamed"
	edited.Enabled = false

	got := ReplaceInstance(list, edited)
	if len(got) != 4 {
		t.Fatalf("edit changed the list length: %d", len(got))
	}
	if got[1].Name != "MiniMax renamed" || got[1].Enabled {
		t.Errorf("edit did not land in place: %+v", got[1])
	}

	fresh := gpu("new-1", "New GPU", "http://127.0.0.1:9")
	got = ReplaceInstance(list, fresh)
	if len(got) != 5 || got[4].ID != "new-1" {
		t.Fatalf("append = %v", ids(got))
	}
	// Appending at the flat end renders last within its category section.
	if got[4].Kind.Category() != domain.CategoryLocal {
		t.Errorf("appended instance kind = %s", got[4].Kind)
	}
}

// The Add editor's draft: default name prefilled, default interval, valid
// default settings, and a unique stable ID generated at editor-open time.
func TestNewDraftPrefillsAndGeneratesID(t *testing.T) {
	draft, ok := newDraft(domain.KindMiniMax)
	if !ok {
		t.Fatal("known kind must produce a draft")
	}
	if draft.Name != "MiniMax" {
		t.Errorf("draft name = %q, want the kind default", draft.Name)
	}
	if draft.PollInterval != config.DefaultRemoteInterval {
		t.Errorf("draft interval = %v, want the category default", draft.PollInterval)
	}
	if !draft.Enabled {
		t.Error("drafts start enabled")
	}
	if draft.MiniMax == nil {
		t.Error("draft must carry the kind's default settings")
	}
	if draft.ID == "" {
		t.Fatal("draft must have a generated instance ID")
	}

	other, _ := newDraft(domain.KindMiniMax)
	if other.ID == draft.ID {
		t.Error("each Add editor must get its own fresh ID")
	}

	amd, _ := newDraft(domain.KindAMDGPUWeb)
	if amd.AMDGPUWeb == nil || amd.AMDGPUWeb.BaseURL != config.DefaultAMDGPUWebURL {
		t.Errorf("amdgputop-web draft settings = %+v", amd.AMDGPUWeb)
	}
	if _, ok := newDraft(domain.SourceKind("nope")); ok {
		t.Error("unknown kind must not produce a draft")
	}
}

// The Hindsight draft starts from the local default API URL with no Web UI
// URL, so a new instance never inherits an unrelated UI endpoint.
func TestNewDraftHindsight(t *testing.T) {
	draft, ok := newDraft(domain.KindHindsight)
	if !ok {
		t.Fatal("hindsight must produce a draft")
	}
	if draft.Name != "Hindsight" {
		t.Errorf("draft name = %q, want Hindsight", draft.Name)
	}
	if draft.PollInterval != config.DefaultHindsightInterval {
		t.Errorf("draft interval = %v, want the hindsight default", draft.PollInterval)
	}
	if draft.Hindsight == nil {
		t.Fatal("draft must carry hindsight settings")
	}
	if draft.Hindsight.BaseURL != config.DefaultHindsightURL || draft.Hindsight.WebUIURL != "" {
		t.Errorf("draft settings = %+v, want the default API URL and no Web UI URL", draft.Hindsight)
	}
}

// The mutation gate: at most one configuration mutation may be in flight;
// a second entry while one runs is rejected until the first leaves.
func TestMutationGateSingleFlight(t *testing.T) {
	s := &Settings{}
	if !s.mutBusy.CompareAndSwap(false, true) {
		t.Error("first entry must succeed")
	}
	if s.mutBusy.CompareAndSwap(false, true) {
		t.Error("a concurrent second entry must be rejected")
	}
	s.mutBusy.Store(false)
	if !s.mutBusy.CompareAndSwap(false, true) {
		t.Error("the gate must reopen after the mutation finished")
	}
}

func TestURLValidator(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"", false},
		{"http://127.0.0.1:8080", false},
		{"https://api.z.ai", false},
		{"ftp://host", true},
		{"not a url", true},
		{"http://", true}, // no host
	}
	for _, c := range cases {
		err := urlValidator(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("urlValidator(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
		}
	}
}

func TestIntervalOptions(t *testing.T) {
	opts := intervalOptions(config.LocalIntervals)
	want := []string{"5s", "10s", "15s", "30s", "1m"} // 60s labels as 1m
	if len(opts) != len(want) {
		t.Fatalf("options = %v", opts)
	}
	for i := range want {
		if opts[i] != want[i] {
			t.Errorf("option[%d] = %q, want %q", i, opts[i], want[i])
		}
	}
	remote := intervalOptions(config.RemoteIntervals)
	if remote[0] != "30s" || remote[1] != "1m" || remote[4] != "10m" {
		t.Errorf("remote options = %v", remote)
	}
	// Hindsight has its own shifted-up ladder.
	hindsight := intervalOptions(config.HindsightIntervals)
	wantHindsight := []string{"10s", "15s", "30s", "1m", "5m"}
	for i := range wantHindsight {
		if hindsight[i] != wantHindsight[i] {
			t.Errorf("hindsight option[%d] = %q, want %q", i, hindsight[i], wantHindsight[i])
		}
	}
}

func TestGPUOption(t *testing.T) {
	if got := gpuOption(domain.GPUInfo{ID: "amdgpu0", Name: "Radeon RX 580"}); got != "Radeon RX 580 (amdgpu0)" {
		t.Errorf("named = %q", got)
	}
	if got := gpuOption(domain.GPUInfo{ID: "amdgpu1"}); got != "amdgpu1" {
		t.Errorf("unnamed = %q", got)
	}
}

func TestOptionForID(t *testing.T) {
	gpus := []domain.GPUInfo{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	if opt, ok := optionForID(gpus, "b"); !ok || opt != "B (b)" {
		t.Errorf("optionForID = %q, %v", opt, ok)
	}
	if _, ok := optionForID(gpus, "zz"); ok {
		t.Error("unknown ID must not match")
	}
}

func TestFormatDurationsRoundTrip(t *testing.T) {
	for _, d := range append(append(append([]time.Duration{}, config.LocalIntervals...), config.HindsightIntervals...), config.RemoteIntervals...) {
		if got := config.IntervalLabel(d); got == "" {
			t.Errorf("IntervalLabel(%v) empty", d)
		}
	}
}
