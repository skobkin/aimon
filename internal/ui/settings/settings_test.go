package settings

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
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
// collapse into a sliver. Each line is measured inside its own tab, because
// AppTabs lays out only the selected tab's content.
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
	if w := s.status.Size().Width; w < 200 {
		t.Errorf("status line width = %.0f after a long message, want the width left of the Add button", w)
	}

	s.tabs.SelectIndex(1)
	s.keychain.SetText(longStatusLine)
	if w := s.keychain.Size().Width; w < 200 {
		t.Errorf("keychain line width = %.0f, want the full window width", w)
	}
}

// A source's name is the user's own free text: a long one wraps inside its
// row instead of widening the Settings window to the name's single line.
// The name is long enough to dwarf the row's action cluster, so the list
// width only stays below it if the name itself wraps.
func TestSettingsListRowNameWraps(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	name := "amdgputop-web on the media box over the LAN, second floor, rack B"
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

// The Updates check syncs from the preference on Show and persists toggles
// through Deps — the same immediate-apply contract as the privacy check.
func TestSettingsUpdatesCheckSyncsAndPersists(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{} // Show() probes the keychain
	automatic := true
	var setCalls []bool
	stub.CheckUpdatesAutomatically = func() bool { return automatic }
	stub.SetCheckUpdatesAutomatically = func(on bool) {
		setCalls = append(setCalls, on)
		automatic = on
	}
	s := NewSettings(test.NewTempApp(t), stub)

	s.Show()
	if !s.updates.Checked {
		t.Error("Show must sync the check from the preference")
	}

	automatic = false
	s.Show()
	if s.updates.Checked {
		t.Error("Show must reload the preference")
	}

	s.updates.SetChecked(true)
	if len(setCalls) < 1 || !setCalls[len(setCalls)-1] {
		t.Errorf("toggling the check must reach SetCheckUpdatesAutomatically, got %v", setCalls)
	}
}

// The notification checks follow the same immediate-apply contract as the
// privacy and update checks: sync from the preferences on Show, persist
// each toggle through Deps on change.
func TestSettingsNotificationChecksSyncAndPersist(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	state := map[string]bool{"master": true, "low": true, "exhausted": true}
	var calls []string
	stub.NotificationsEnabled = func() bool { return state["master"] }
	stub.SetNotificationsEnabled = func(on bool) {
		calls = append(calls, "master")
		state["master"] = on
	}
	stub.NotificationsQuotaLowEnabled = func() bool { return state["low"] }
	stub.SetNotificationsQuotaLowEnabled = func(on bool) {
		calls = append(calls, "low")
		state["low"] = on
	}
	stub.NotificationsQuotaExhaustedEnabled = func() bool { return state["exhausted"] }
	stub.SetNotificationsQuotaExhaustedEnabled = func(on bool) {
		calls = append(calls, "exhausted")
		state["exhausted"] = on
	}
	s := NewSettings(test.NewTempApp(t), stub)

	s.Show()
	if !s.notifs.Checked || !s.notifsLow.Checked || !s.notifsExhausted.Checked {
		t.Error("Show must sync all three notification checks from the preferences")
	}

	state["master"], state["low"], state["exhausted"] = false, false, false
	s.Show()
	if s.notifs.Checked || s.notifsLow.Checked || s.notifsExhausted.Checked {
		t.Error("Show must reload the notification preferences")
	}

	// Show's own sync calls fired the setters too; only the explicit
	// toggles from here on are asserted — each check toggled from the
	// state Show left it in.
	calls = nil
	s.notifs.SetChecked(true)
	s.notifsLow.SetChecked(true)
	s.notifsExhausted.SetChecked(true)
	if len(calls) != 3 || calls[0] != "master" || calls[1] != "low" || calls[2] != "exhausted" {
		t.Errorf("toggles reached %v, want each check's own setter", calls)
	}
}

// The Startup toggles: Show syncs the show-monitor check from the
// preference and the autostart check from the entry's actual presence; an
// unsupported binary disables the autostart check with the hint shown;
// toggling persists through the setters.
func TestSettingsStartupControls(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	present, supported := true, true
	startupState := true
	var autostartCalls []bool
	stub.ShowMonitorOnStartup = func() bool { return startupState }
	stub.SetShowMonitorOnStartup = func(on bool) { startupState = on }
	stub.AutostartState = func() (bool, bool) { return present, supported }
	stub.SetAutostart = func(on bool) error {
		autostartCalls = append(autostartCalls, on)
		present = on

		return nil
	}
	s := NewSettings(test.NewTempApp(t), stub)

	// Supported and present: check on and enabled, hint hidden.
	s.Show()
	if !s.startupShow.Checked || !s.autostart.Checked {
		t.Error("Show must sync the startup checks from the app state")
	}
	if s.autostart.Disabled() {
		t.Error("a supported binary must leave the autostart check enabled")
	}
	if s.startupHint.Visible() {
		t.Error("the hint must stay hidden for a supported binary")
	}

	// Unsupported: check disabled and explained, presence still reported.
	present, supported = true, false
	s.Show()
	if !s.autostart.Disabled() {
		t.Error("an unsupported binary must disable the autostart check")
	}
	if !s.startupHint.Visible() {
		t.Error("the hint must explain a disabled autostart check")
	}

	// Back to supported-but-absent, then toggle both: each reaches its own
	// setter (Show's sync fires them too, so only explicit toggles count).
	present, supported = false, true
	s.Show()
	autostartCalls = nil
	s.startupShow.SetChecked(true)
	s.autostart.SetChecked(true)
	if !startupState {
		t.Error("the show-monitor toggle must persist")
	}
	if len(autostartCalls) != 1 || !autostartCalls[0] {
		t.Errorf("autostart toggle reached %v, want [true]", autostartCalls)
	}
	if !s.autostart.Checked {
		t.Error("a successful SetAutostart must leave the check on")
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

// findHyperlinks collects every hyperlink in the object tree.
func findHyperlinks(t *testing.T, obj fyne.CanvasObject) []*widget.Hyperlink {
	t.Helper()
	var out []*widget.Hyperlink
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch c := o.(type) {
		case *widget.Hyperlink:
			out = append(out, c)
		case fyne.Widget:
			for _, child := range test.TempWidgetRenderer(t, c).Objects() {
				walk(child)
			}
		case *fyne.Container:
			for _, child := range c.Objects {
				walk(child)
			}
		}
	}
	walk(obj)

	return out
}

// The About tab carries the fixed build identity and the two clickable
// on-disk paths; ShowAboutTab selects it for the tray's About entry.
func TestSettingsAboutTabShowsBuildAndPaths(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	stub.ConfigPaths = func() (string, string) {
		return "/home/tester/.config/fyne/com.skobkin.aimonitor",
			"/home/tester/.config/fyne/com.skobkin.aimonitor/logs/aimon.log"
	}
	// Release builds stamp these at link time; the tab must render both.
	commit, date := buildinfo.Commit, buildinfo.BuildDate
	buildinfo.Commit, buildinfo.BuildDate = "abc1234", "2026-10-30T12:00:00Z"
	t.Cleanup(func() { buildinfo.Commit, buildinfo.BuildDate = commit, date })

	s := NewSettings(test.NewTempApp(t), stub)

	s.ShowAboutTab()
	if got := s.tabs.Selected(); got != s.aboutItem {
		t.Fatalf("ShowAboutTab selected %v, want the About tab", got)
	}

	content := s.aboutItem.Content
	for _, want := range []string{
		"Version: " + buildinfo.Version,
		"Commit: abc1234",
		"Built: 2026-10-30T12:00:00Z",
		"Settings:",
		"Logs:",
	} {
		if labelByText(t, content, want) == nil {
			t.Errorf("About tab is missing the label %q", want)
		}
	}
	wantLinks := map[string]bool{
		"file:///home/tester/.config/fyne/com.skobkin.aimonitor":                false,
		"file:///home/tester/.config/fyne/com.skobkin.aimonitor/logs/aimon.log": false,
	}
	for _, l := range findHyperlinks(t, content) {
		if _, ok := wantLinks[l.URL.String()]; ok {
			wantLinks[l.URL.String()] = true
		}
	}
	for u, seen := range wantLinks {
		if !seen {
			t.Errorf("About tab is missing the hyperlink %s", u)
		}
	}
}

// Without a ConfigPaths provider the tab degrades to Unavailable instead
// of dead links — and a dev build shows no empty commit/build rows.
func TestSettingsAboutTabWithoutPathsAndStamps(t *testing.T) {
	stub := stubDeps() // ConfigPaths nil; buildinfo stamps empty in tests
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.tabs.Select(s.aboutItem)

	content := s.aboutItem.Content
	if got := len(findHyperlinks(t, content)); got != 0 {
		t.Errorf("found %d hyperlinks, want none without paths", got)
	}
	unavailable := 0
	walkCanvas(t, content, func(o fyne.CanvasObject) {
		if l, ok := o.(*widget.Label); ok && l.Text == "Unavailable" {
			unavailable++
		}
	})
	if unavailable != 2 {
		t.Errorf("found %d Unavailable labels, want one per path row", unavailable)
	}
	for _, absent := range []string{"Commit: ", "Built: "} {
		walkCanvas(t, content, func(o fyne.CanvasObject) {
			if l, ok := o.(*widget.Label); ok && strings.HasPrefix(l.Text, absent) {
				t.Errorf("dev build must not carry a %q row (got %q)", absent, l.Text)
			}
		})
	}
}

// duplicateInstance rekeys and copies: fresh ID, suffixed name, the
// source's interval/pace window/enabled flag, and its own settings block.
func TestDuplicateInstanceCopiesAndRekeys(t *testing.T) {
	inst := mm("m1", "MiniMax one", true)
	inst.PollInterval = 5 * time.Minute
	inst.PaceWindow = 45 * time.Minute

	dup, ok := duplicateInstance(inst)
	if !ok {
		t.Fatal("a known kind must produce a duplicate draft")
	}
	if dup.ID == inst.ID || dup.ID == "" {
		t.Errorf("duplicate ID = %q, want a fresh one", dup.ID)
	}
	if dup.Name != "MiniMax one (copy)" {
		t.Errorf("duplicate name = %q", dup.Name)
	}
	if !dup.Enabled {
		t.Error("the enabled flag must copy")
	}
	if dup.PollInterval != 5*time.Minute {
		t.Errorf("duplicate interval = %v", dup.PollInterval)
	}
	if dup.PaceWindow != 45*time.Minute {
		t.Errorf("duplicate pace window = %v", dup.PaceWindow)
	}
	if dup.MiniMax == nil {
		t.Error("duplicate must carry the kind's settings block")
	}

	// Provider settings copy by value: the draft's block is its own.
	g := gpu("g1", "GPU one", "http://127.0.0.1:49001")
	dup2, _ := duplicateInstance(g)
	if dup2.AMDGPUWeb == nil || dup2.AMDGPUWeb.BaseURL != "http://127.0.0.1:49001" {
		t.Errorf("duplicate settings = %+v", dup2.AMDGPUWeb)
	}

	// Two duplicates of one source are independent drafts.
	dup3, _ := duplicateInstance(g)
	if dup3.ID == dup2.ID {
		t.Error("each duplicate must get its own fresh ID")
	}

	// Disabled sources duplicate as disabled.
	g.Enabled = false
	dup4, _ := duplicateInstance(g)
	if dup4.Enabled {
		t.Error("the disabled flag must copy")
	}

	// An unknown kind has no draft to start from.
	if _, ok := duplicateInstance(config.SourceInstance{Kind: domain.SourceKind("nope")}); ok {
		t.Error("unknown kind must not produce a duplicate")
	}
}

// The duplicate-name hint is soft: it names the collision (case- and
// whitespace-insensitively, other instances only) and stays silent
// otherwise.
func TestDuplicateNameHint(t *testing.T) {
	instances := interleaved()

	if got := duplicateNameHint("miniMAX ONE", "g1", instances); got == "" {
		t.Error("a case-insensitive collision with another instance must hint")
	}
	if got := duplicateNameHint("  MiniMax one  ", "m1", instances); got != "" {
		t.Errorf("the instance's own name must not hint, got %q", got)
	}
	if got := duplicateNameHint("uniquely named", "g1", instances); got != "" {
		t.Errorf("a unique name must not hint, got %q", got)
	}
	if got := duplicateNameHint("", "g1", instances); got != "" {
		t.Errorf("an empty name must not hint, got %q", got)
	}
}

func TestNameValidator(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"", true},
		{"   ", true},
		{"MiniMax", false},
		{"  padded  ", false},
	}
	for _, c := range cases {
		err := nameValidator(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("nameValidator(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
		}
	}
}

// The duplicate row action opens the editor for a copy: the name entry
// prefilled with the suffixed name, untouched configuration behind it.
func TestDuplicateRowActionOpensEditorForCopy(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	stub.LoadInstances = func() []config.SourceInstance {
		return []config.SourceInstance{mm("m1", "MiniMax one", true)}
	}
	s := NewSettings(test.NewTempApp(t), stub)
	s.duplicate("m1")

	var nameEntry *widget.Entry
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			if e, ok := o.(*widget.Entry); ok && !e.Password && nameEntry == nil {
				nameEntry = e
			}
		})
	}
	if nameEntry == nil {
		t.Fatal("the duplicate editor did not open")
	}
	if nameEntry.Text != "MiniMax one (copy)" {
		t.Errorf("editor name = %q, want the suffixed copy", nameEntry.Text)
	}
}

// The Add dialog explains the selected kind: an empty hint before a
// choice, the kind's description once one is selected.
func TestChooseKindShowsDescription(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.chooseKind()

	var sel *widget.Select
	var desc *widget.Label
	var form *widget.Form
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			switch w := o.(type) {
			case *widget.Select:
				if sel == nil {
					sel = w
				}
			case *widget.Form:
				if form == nil {
					form = w
				}
			case *widget.Label:
				if w.TextStyle.Italic && desc == nil {
					desc = w
				}
			}
		})
	}
	if sel == nil || desc == nil || form == nil {
		t.Fatal("the kind chooser dialog is missing the select, the form or the description label")
	}
	// The description must live outside the form: a form row shares the
	// label column, which would wrap the text in the narrow right cell
	// instead of across the dialog's full width.
	descInForm := false
	walkCanvas(t, form, func(o fyne.CanvasObject) {
		if o == desc {
			descInForm = true
		}
	})
	if descInForm {
		t.Error("the description must not be a form item; it would wrap in the form's label column")
	}
	if desc.Text != "" {
		t.Errorf("description before any selection = %q, want empty", desc.Text)
	}

	sel.SetSelected("MiniMax")
	if desc.Text != "MiniMax Token Plan quota windows." {
		t.Errorf("description after selection = %q", desc.Text)
	}
	sel.SetSelected("OpenRouter")
	if desc.Text != "Account spend and per-key usage and limits." {
		t.Errorf("description after switching = %q", desc.Text)
	}
}

// The import body validates before anything else: a bad document reports
// and never reaches the commit path, a valid one asks first, and only an
// affirmative answer commits through the guarded save path. An empty
// document is valid (the zero-source import).
func TestSettingsImportDocument(t *testing.T) {
	doc := `{"schema_version":1,"sources":[{"id":"m1","kind":"minimax","name":"Imported","enabled":true,"poll_interval":"1m","minimax":{}}]}`

	t.Run("bad document reports and stops", func(t *testing.T) {
		s, savedCh := importTestSettings(t)
		var report string
		asked := false
		s.importDocument("{not json",
			func([]config.SourceInstance, func()) { asked = true },
			func(msg string) { report = msg })
		if report == "" {
			t.Error("a parse failure must report")
		}
		if asked {
			t.Error("a parse failure must not ask to commit")
		}
		select {
		case list := <-savedCh:
			t.Errorf("a parse failure must not save: %+v", list)
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("valid document asks then commits", func(t *testing.T) {
		s, savedCh := importTestSettings(t)
		var gotList []config.SourceInstance
		var commit func()
		asked := make(chan struct{})
		s.importDocument(doc,
			func(list []config.SourceInstance, commitFunc func()) {
				gotList = list
				commit = commitFunc
				close(asked)
			},
			func(msg string) { t.Errorf("a valid document must not report a failure: %s", msg) })

		select {
		case <-asked:
		case <-time.After(2 * time.Second):
			t.Fatal("import never asked to commit")
		}
		if len(gotList) != 1 || gotList[0].Name != "Imported" {
			t.Fatalf("ask received %+v", gotList)
		}

		commit()
		select {
		case list := <-savedCh:
			if len(list) != 1 || list[0].ID != "m1" {
				t.Errorf("committed %+v", list)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("commit never saved")
		}
	})

	t.Run("declined commit saves nothing", func(t *testing.T) {
		s, savedCh := importTestSettings(t)
		s.importDocument(doc,
			func([]config.SourceInstance, func()) {}, // user declines: never commits
			func(msg string) { t.Errorf("a valid document must not report a failure: %s", msg) })
		select {
		case list := <-savedCh:
			t.Errorf("a declined import saved %+v", list)
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("empty document imports zero sources", func(t *testing.T) {
		s, savedCh := importTestSettings(t)
		s.importDocument(`{"schema_version":1,"sources":[]}`,
			func(list []config.SourceInstance, commit func()) {
				commit()
			},
			func(string) { t.Error("an empty document must not report a failure") })
		select {
		case list := <-savedCh:
			if len(list) != 0 {
				t.Errorf("empty import saved %+v, want zero sources", list)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("empty import never saved")
		}
	})
}

// exportDocument renders the stored document verbatim, or the minimal
// valid document when nothing is stored — so an export always imports
// back.
func TestExportDocumentBody(t *testing.T) {
	if got := exportDocument(""); got != "{\n  \"schema_version\": 1\n}" {
		t.Errorf("empty export = %q", got)
	}
	raw := "{\n  \"schema_version\": 1,\n  \"sources\": []\n}"
	if got := exportDocument(raw); got != raw {
		t.Errorf("stored export = %q, want verbatim", got)
	}
}

// importTestSettings wires a Settings whose saves funnel into a channel:
// mutate persists on a background goroutine, so tests synchronize on the
// channel instead of sleeping. SaveInstances is replaced before
// NewSettings because Deps is stored by value.
func importTestSettings(t *testing.T) (*Settings, <-chan []config.SourceInstance) {
	t.Helper()
	stub := stubDeps()
	stub.Creds = blockedStore{}
	savedCh := make(chan []config.SourceInstance, 4)
	stub.SaveInstances = func(list []config.SourceInstance) error {
		savedCh <- list

		return nil
	}

	return NewSettings(test.NewTempApp(t), stub), savedCh
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
