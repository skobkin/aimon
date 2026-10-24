package settings

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
)

// stubDeps builds a Settings whose background operations are never run by
// layout tests.
func stubDeps() Deps {
	return Deps{
		LoadInstances:      func() []config.SourceInstance { return nil },
		SaveInstances:      func([]config.SourceInstance) error { return nil },
		CleanupCredentials: func(context.Context, config.SourceInstance) error { return nil },
		TestConnection: func(context.Context, config.SourceInstance, map[credentials.Slot]string) registry.TestReport {
			return registry.TestReport{}
		},
		DiscoverGPUs: func(context.Context, string) ([]domain.GPUInfo, error) { return nil, nil },
		Done:         make(chan struct{}),
	}
}

// findButtons collects every button in the object tree by its text.
func findButtons(t *testing.T, obj fyne.CanvasObject, into map[string]*widget.Button) {
	t.Helper()
	switch o := obj.(type) {
	case *widget.Button:
		into[o.Text] = o
	case *fyne.Container:
		for _, child := range o.Objects {
			findButtons(t, child, into)
		}
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			findButtons(t, child, into)
		}
	}
}

// The editor's Save/Cancel row must stretch across the dialog as equal
// halves. Wrapping it in an HBox of an HScroll once collapsed the whole
// row to the scrollbar's minimum size, clipping both buttons.
func TestEditorButtonsShareDialogBottomRow(t *testing.T) {
	s := NewSettings(test.NewTempApp(t), stubDeps())
	s.win.Show()

	s.editDraft(domain.KindAMDGPUWeb)

	buttons := map[string]*widget.Button{}
	for _, overlay := range s.win.Canvas().Overlays().List() {
		findButtons(t, overlay, buttons)
	}
	cancel, save := buttons["Cancel"], buttons["Save"]
	if cancel == nil || save == nil {
		t.Fatalf("editor buttons missing from the dialog overlay; found %v", keys(buttons))
	}
	if cancel.Size().IsZero() || save.Size().IsZero() {
		t.Fatalf("buttons were never laid out: cancel=%v save=%v", cancel.Size(), save.Size())
	}
	if math.Abs(float64(cancel.Position().Y-save.Position().Y)) > 0.5 {
		t.Errorf("Save and Cancel must sit on one row: cancel at %v, save at %v",
			cancel.Position(), save.Position())
	}
	if w := save.Size().Width; w < 100 || math.Abs(float64(w-cancel.Size().Width)) > 1 {
		t.Errorf("Save and Cancel must share the row as equal halves; cancel=%v save=%v",
			cancel.Size(), save.Size())
	}
}

func keys(m map[string]*widget.Button) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

// The dialog sizes itself to its content's minimum — a thin strip once the
// labels are short and the help text wraps. It must take most of the
// Settings window instead, never below the editor's minimum width.
func TestEditorDialogTakesMostOfSettingsWindow(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindCodex)

	var popup *widget.PopUp
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			if p, ok := o.(*widget.PopUp); ok {
				popup = p
			}
		})
	}
	if popup == nil {
		t.Fatal("editor dialog popup not found in the overlay")
	}
	if popup.Size().IsZero() {
		t.Fatal("the dialog was never laid out")
	}
	canvas := s.win.Canvas().Size()
	want := fyne.Max(canvas.Width*0.9, editorMinWidth)
	if w := popup.Size().Width; w < want-0.5 {
		t.Errorf("dialog width = %.0f, want at least %.0f (90%% of the %.0f-wide window, min %.0f)",
			w, want, canvas.Width, editorMinWidth)
	}
}

// The dialog is bounded by the Settings window, so a kind whose editor is
// taller than it (the Codex one with its help text) scrolls its form: the
// dialog must fit the parent without resizing, the form must actually
// scroll (so Test connection, its last row, is reachable), and the pinned
// Save/Cancel row must stay inside the dialog without scrolling.
func TestEditorDialogScrollsTallForms(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindCodex)

	var popup *widget.PopUp
	var scroller *container.Scroll
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			switch w := o.(type) {
			case *widget.PopUp:
				popup = w
			case *container.Scroll:
				scroller = w
			}
		})
	}
	if popup == nil {
		t.Fatal("editor dialog popup not found in the overlay")
	}
	if canvas := s.win.Canvas().Size().Height; canvas < popup.Size().Height {
		t.Errorf("dialog height %.0f exceeds the %.0f window; it must fit without resizing",
			popup.Size().Height, canvas)
	}
	if scroller == nil {
		t.Fatal("the editor form is not wrapped in a scroll container")
	}
	if scroller.Content == nil || scroller.Content.MinSize().Height <= scroller.Size().Height {
		t.Errorf("form content %v does not scroll inside the %v viewport; Test connection would be unreachable",
			scroller.Content.MinSize(), scroller.Size())
	}

	buttons := map[string]*widget.Button{}
	for _, overlay := range s.win.Canvas().Overlays().List() {
		findButtons(t, overlay, buttons)
	}
	save := buttons["Save"]
	if save == nil || save.Size().IsZero() {
		t.Fatal("Save was never laid out")
	}
	dialogBottom := popup.Position().Y + popup.Size().Height
	if saveBottom := save.Position().Y + save.Size().Height; saveBottom > dialogBottom+0.5 {
		t.Errorf("Save bottom %.0f lies below the dialog bottom %.0f; the action row must stay pinned",
			saveBottom, dialogBottom)
	}
}

// blockedStore parks the keychain-status probe forever. In production the
// probe's results hop back through fyne.Do onto the UI thread; the headless
// test driver runs that callback inline on the probe goroutine, which would
// interleave text measuring with the test's own widget walks. Parking the
// probes keeps every dialog-shape test single-threaded; the parked
// goroutines die with the test binary.
type blockedStore struct{}

var parkedForever = make(chan struct{}) // never closed

func (blockedStore) Status(context.Context) credentials.Status {
	<-parkedForever

	return credentials.Status{State: credentials.StateAvailable}
}

func (blockedStore) Get(context.Context, credentials.AccountID) (string, error) {
	return "", credentials.ErrNotFound
}

func (blockedStore) Set(context.Context, credentials.AccountID, string) error { return nil }
func (blockedStore) Delete(context.Context, credentials.AccountID) error      { return nil }

// editorScan summarizes the widgets a fresh editor dialog built.
type editorScan struct {
	passwords    int            // credential password entries
	plainEntries int            // name / URL style entries
	buttons      map[string]int // button text -> count
}

func scanEditor(t *testing.T, s *Settings) editorScan {
	t.Helper()
	scan := editorScan{buttons: map[string]int{}}
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			switch w := o.(type) {
			case *widget.Entry:
				if w.Password {
					scan.passwords++
				} else {
					scan.plainEntries++
				}
			case *widget.Button:
				scan.buttons[w.Text]++
			}
		})
	}

	return scan
}

// walkCanvas visits widgets through their renderers, descending until the
// leaves; containers recurse through their objects.
func walkCanvas(t *testing.T, obj fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	t.Helper()
	switch o := obj.(type) {
	case *widget.Entry:
		visit(o)
	case *widget.Button:
		visit(o)
	case *widget.Label:
		visit(o)
	case *widget.Check:
		visit(o)
	case *widget.Select:
		visit(o)
	case *widget.Form: // visited so tests can inspect the form's items
		visit(o)
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			walkCanvas(t, child, visit)
		}
	case *widget.PopUp:
		visit(o)
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			walkCanvas(t, child, visit)
		}
	case *container.Scroll:
		visit(o)
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			walkCanvas(t, child, visit)
		}
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			walkCanvas(t, child, visit)
		}
	case *fyne.Container:
		for _, child := range o.Objects {
			walkCanvas(t, child, visit)
		}
	}
}

// A kind's editor builds exactly one credential row per definition slot —
// OpenRouter gets independent API-key and Management-key rows while the
// single-slot kinds keep exactly one, and fixed-endpoint kinds show no URL
// field. Structure is asserted before any asynchronous probe lands.
func TestEditorBuildsOneCredentialRowPerSlot(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}

	cases := []struct {
		kind             domain.SourceKind
		wantPasswords    int
		wantRemove       int
		wantPlainEntries int // name (+ URL when the kind has a base URL)
	}{
		{domain.KindMiniMax, 1, 1, 1},
		{domain.KindZAI, 1, 1, 2},
		{domain.KindOpenRouter, 2, 2, 1},
		{domain.KindAMDGPUWeb, 0, 0, 2},
		// API URL + Web UI URL alongside the name, one optional key row.
		{domain.KindHindsight, 1, 1, 3},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			s := NewSettings(test.NewTempApp(t), stub)
			s.editDraft(c.kind)

			scan := scanEditor(t, s)
			if scan.passwords != c.wantPasswords {
				t.Errorf("password entries = %d, want %d", scan.passwords, c.wantPasswords)
			}
			if got := scan.buttons["Remove"]; got != c.wantRemove {
				t.Errorf("Remove buttons = %d, want %d", got, c.wantRemove)
			}
			if scan.plainEntries != c.wantPlainEntries {
				t.Errorf("plain entries = %d, want %d", scan.plainEntries, c.wantPlainEntries)
			}
		})
	}
}

// The Hindsight editor shows the API URL, the optional Web UI URL, and the
// optional key row with its hint — placeholders included, so the fields are
// self-explanatory before anything is typed.
func TestEditorHindsightFields(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindHindsight)

	var apiURL, webURL, apikey *widget.Entry
	var hint *widget.Label
	var remove *widget.Button
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			switch w := o.(type) {
			case *widget.Entry:
				switch w.PlaceHolder {
				case config.DefaultHindsightURL:
					apiURL = w
				case "http://127.0.0.1:9999 (optional)":
					webURL = w
				default:
					if w.Password {
						apikey = w
					}
				}
			case *widget.Label:
				if strings.Contains(w.Text, "Optional Bearer token") {
					hint = w
				}
			case *widget.Button:
				if w.Text == "Remove" {
					remove = w
				}
			}
		})
	}
	if apiURL == nil || webURL == nil || apikey == nil {
		t.Fatalf("missing editor entries: api-url=%v web-url=%v api-key=%v", apiURL, webURL, apikey)
	}
	if !apikey.Password {
		t.Error("the key row must be a password entry")
	}
	if apiURL.Text != config.DefaultHindsightURL || webURL.Text != "" {
		t.Errorf("prefill = %q / %q, want the default API URL and an empty Web UI URL", apiURL.Text, webURL.Text)
	}
	if remove == nil {
		t.Error("the key row is missing its Remove button")
	}
	if hint == nil {
		t.Error("the optional key slot is missing its hint label")
	}
}

// The editor's collected Hindsight instance carries both URL fields, with
// the Web UI URL trimmed; an invalid Web UI URL blocks the test locally.
func TestEditorHindsightCollect(t *testing.T) {
	delivered := make(chan config.SourceInstance, 1)
	stub := stubDeps()
	stub.Creds = blockedStore{}
	stub.TestConnection = func(ctx context.Context, inst config.SourceInstance,
		typed map[credentials.Slot]string) registry.TestReport {
		delivered <- inst

		return registry.TestReport{Lines: []string{"x"}, OK: true}
	}

	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindHindsight)

	var apiURL, webURL *widget.Entry
	var testBtn *widget.Button
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			switch w := o.(type) {
			case *widget.Entry:
				if w.Password {
					return
				}
				switch w.PlaceHolder {
				case config.DefaultHindsightURL:
					apiURL = w
				case "http://127.0.0.1:9999 (optional)":
					webURL = w
				}
			case *widget.Button:
				if w.Text == "Test connection" {
					testBtn = w
				}
			}
		})
	}
	if apiURL == nil || webURL == nil || testBtn == nil {
		t.Fatalf("missing editor widgets: api=%v web=%v test=%v", apiURL, webURL, testBtn)
	}

	apiURL.SetText("http://127.0.0.1:9001")
	webURL.SetText("  http://127.0.0.1:9002  ")
	testBtn.OnTapped()

	select {
	case got := <-delivered:
		if got.Hindsight == nil {
			t.Fatal("collected instance lost its settings")
		}
		if got.Hindsight.BaseURL != "http://127.0.0.1:9001" {
			t.Errorf("BaseURL = %q, want the API URL field", got.Hindsight.BaseURL)
		}
		if got.Hindsight.WebUIURL != "http://127.0.0.1:9002" {
			t.Errorf("WebUIURL = %q, want the trimmed Web UI URL field", got.Hindsight.WebUIURL)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TestConnection was never invoked")
	}

	// An invalid Web UI URL is refused before any request or delivery.
	webURL.SetText("not a url")
	testBtn.OnTapped()
	select {
	case got := <-delivered:
		t.Fatalf("invalid Web UI URL still reached TestConnection: %+v", got.Hindsight)
	case <-time.After(100 * time.Millisecond):
	}
}

// A Hindsight editor offers its own shifted-up ladder, and a stored
// interval that is no longer on it falls back to the kind's 10s default
// rather than to the local 5s one. The selection stays a standard Select.
func TestSelectIntervalHindsightFallback(t *testing.T) {
	presets, def := config.HindsightIntervals, config.DefaultHindsightInterval

	sel := widget.NewSelect(intervalOptions(presets), nil)
	selectInterval(sel, 15*time.Second, presets, def)
	if sel.Selected != "15s" {
		t.Errorf("offered interval selected %q, want 15s", sel.Selected)
	}

	selectInterval(sel, 5*time.Second, presets, def) // the old stored default
	if sel.Selected != config.IntervalLabel(def) {
		t.Errorf("stale interval fell back to %q, want %q", sel.Selected, config.IntervalLabel(def))
	}

	// Round-tripping the selection keeps the chosen preset.
	if got := selectedInterval(sel, presets, def); got != def {
		t.Errorf("selectedInterval = %v, want %v", got, def)
	}
}

// The Management-key row must be distinguishable from the API-key row
// even with both empty.
func TestEditorSeparatesOpenRouterSlotsInTestPayload(t *testing.T) {
	delivered := make(chan map[credentials.Slot]string, 1)
	stub := stubDeps()
	stub.Creds = blockedStore{}
	stub.TestConnection = func(ctx context.Context, inst config.SourceInstance,
		typed map[credentials.Slot]string) registry.TestReport {
		delivered <- typed

		return registry.TestReport{Lines: []string{"x"}, OK: true}
	}

	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindOpenRouter)

	var mgmtEntry, apiEntry *widget.Entry
	var testBtn *widget.Button
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			switch e := o.(type) {
			case *widget.Entry:
				if !e.Password {
					return
				}
				if strings.Contains(e.PlaceHolder, "Management") {
					mgmtEntry = e
				} else {
					apiEntry = e
				}
			case *widget.Button:
				if e.Text == "Test connection" {
					testBtn = e
				}
			}
		})
	}
	if apiEntry == nil || mgmtEntry == nil || testBtn == nil {
		t.Fatalf("missing editor widgets: api=%v management=%v test=%v", apiEntry, mgmtEntry, testBtn)
	}
	apiEntry.SetText("sk-or-v1-typed-api")
	mgmtEntry.SetText("sk-or-mgmt-typed")

	testBtn.OnTapped()

	select {
	case got := <-delivered:
		if got[credentials.SlotAPIKey] != "sk-or-v1-typed-api" ||
			got[credentials.SlotManagementKey] != "sk-or-mgmt-typed" {
			t.Errorf("typed payload mixed slots up: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TestConnection was never invoked")
	}
}

// --- Codex editor ---------------------------------------------------------

// editorLabels collects every label of the window's open overlay dialogs.
func editorLabels(t *testing.T, s *Settings) []*widget.Label {
	t.Helper()
	var out []*widget.Label
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			if l, ok := o.(*widget.Label); ok {
				out = append(out, l)
			}
		})
	}

	return out
}

// findEditorForm returns the open editor's form.
func findEditorForm(t *testing.T, s *Settings) *widget.Form {
	t.Helper()
	var form *widget.Form
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			if f, ok := o.(*widget.Form); ok {
				form = f
			}
		})
	}

	return form
}

// An editor's free-text labels carry messages of unbounded length — the
// launcher, mode and env help, the live discovery status, slot hints, and
// the connection-test result. Each must wrap: an unwrapped line would widen
// the dialog, and with it the Settings window, to its single-line width.
// Short static labels (titles, kind names) stay on one line.
func TestEditorFreeTextLabelsWrap(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindCodex)

	// A host with an installed launcher shows the short "found" status, so
	// the fourth long label only exists in the not-found state: force it
	// with a bare name nothing installs (the entry's change callback
	// re-runs the discovery lookup synchronously).
	scan := scanCodexEditor(t, s)
	if scan.exe == nil {
		t.Fatal("launcher entry not found in the editor")
	}
	scan.exe.SetText("aimon-no-such-cli")

	wrapped := 0
	for _, l := range editorLabels(t, s) {
		if len([]rune(l.Text)) < 40 {
			continue
		}
		if l.Wrapping != fyne.TextWrapWord {
			t.Errorf("label %q does not wrap; it would widen the dialog to its own width", l.Text)
		}
		wrapped++
	}
	if wrapped < 4 { // launcher + mode help, no-secrets warning, discovery status
		t.Fatalf("only %d long labels scanned; the walk must reach the editor's free text", wrapped)
	}

	form := findEditorForm(t, s)
	if form == nil {
		t.Fatal("editor form not found in the overlay")
	}
	last := form.Items[len(form.Items)-1]
	result, ok := last.Widget.(*widget.Label)
	if !ok {
		t.Fatalf("the form's last row is %T, want the connection-test result label", last.Widget)
	}
	if result.Wrapping != fyne.TextWrapWord {
		t.Errorf("connection-test result wrapping = %v, want word wrap", result.Wrapping)
	}
}

// A credential slot's hint explains the slot in a full sentence; it wraps
// with the rest of the editor's free text.
func TestEditorSlotHintWraps(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindHindsight)

	var hint *widget.Label
	for _, l := range editorLabels(t, s) {
		if strings.HasPrefix(l.Text, "Optional Bearer token") {
			hint = l
		}
	}
	if hint == nil {
		t.Fatal("the Hindsight key row shows no slot hint")
	}
	if hint.Wrapping != fyne.TextWrapWord {
		t.Errorf("slot hint wrapping = %v, want word wrap", hint.Wrapping)
	}
}

// codexEditorScan locates the Codex-specific widgets of an open editor by
// their stable placeholders, options and label prefixes.
type codexEditorScan struct {
	mode    *widget.Select // the select offering On demand/Persistent
	exe     *widget.Entry  // launcher entry (placeholder "codex")
	status  *widget.Label  // live discovery status
	help    *widget.Label  // launcher help
	warning *widget.Label  // no-secrets warning
	addEnv  *widget.Button // Add override
	envName []*widget.Entry
	envVal  []*widget.Entry
}

func scanCodexEditor(t *testing.T, s *Settings) codexEditorScan {
	t.Helper()
	var scan codexEditorScan
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			switch w := o.(type) {
			case *widget.Select:
				for _, opt := range w.Options {
					if opt == "On demand" || opt == "Persistent" {
						scan.mode = w
					}
				}
			case *widget.Entry:
				switch w.PlaceHolder {
				case config.DefaultCodexExecutable:
					scan.exe = w
				case "VARIABLE_NAME":
					scan.envName = append(scan.envName, w)
				case "value (literal, no expansion)":
					scan.envVal = append(scan.envVal, w)
				}
			case *widget.Label:
				switch {
				case strings.HasPrefix(w.Text, "Codex CLI "),
					strings.HasPrefix(w.Text, "Executable "):
					scan.status = w
				case strings.Contains(w.Text, "app-server --stdio"):
					scan.help = w
				case strings.Contains(w.Text, "Do not put secrets here"):
					scan.warning = w
				}
			case *widget.Button:
				if w.Text == "Add override" {
					scan.addEnv = w
				}
			}
		})
	}

	return scan
}

// The Codex editor shows the mode select, the launcher entry with its
// discovery status visible immediately, the launcher and mode help, and the
// override rows' controls — and no URL and no credential row at all.
func TestEditorCodexFields(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindCodex)

	scan := scanCodexEditor(t, s)
	if scan.mode == nil || scan.exe == nil || scan.status == nil {
		t.Fatalf("missing codex widgets: mode=%v exe=%v status=%v", scan.mode, scan.exe, scan.status)
	}
	if len(scan.mode.Options) != 2 {
		t.Fatalf("mode options = %v, want On demand and Persistent", scan.mode.Options)
	}
	if scan.mode.Selected != "On demand" {
		t.Errorf("default mode = %q, want \"On demand\"", scan.mode.Selected)
	}
	if scan.help == nil || scan.warning == nil || scan.addEnv == nil {
		t.Fatalf("missing codex help/warning/override controls: help=%v warning=%v add=%v", scan.help, scan.warning, scan.addEnv)
	}
	if len(scan.envName) != 0 || len(scan.envVal) != 0 {
		t.Errorf("a fresh draft starts with %d override row(s), want none", len(scan.envName))
	}
	if scan.status.Text == "" {
		t.Error("the discovery status must be visible before any connection test")
	}

	// A draft has no URL field and no credential row: the CLI owns the login.
	full := scanEditor(t, s)
	if full.passwords != 0 || full.buttons["Remove"] != 0 {
		t.Errorf("codex editor built credential widgets: %d passwords, %d removes", full.passwords, full.buttons["Remove"])
	}
}

// The discovery status follows the typed launcher: an explicit existing
// executable reports "Executable found", an explicit missing one reports
// the not-executable message, and with the override rows driving the
// effective PATH a bare name resolves (or not) through them.
func TestEditorCodexDiscoveryStatus(t *testing.T) {
	stub := stubDeps()
	stub.Creds = blockedStore{}
	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindCodex)
	scan := scanCodexEditor(t, s)
	if scan.exe == nil || scan.status == nil {
		t.Fatal("codex editor widgets missing")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	scan.exe.SetText(exe)
	if scan.status.Text != "Executable found" {
		t.Errorf("status = %q, want \"Executable found\"", scan.status.Text)
	}

	scan.exe.SetText(filepath.Join(t.TempDir(), "no-such-codex"))
	if scan.status.Text != "Executable not found or not executable." {
		t.Errorf("status = %q, want the explicit-path failure", scan.status.Text)
	}

	// A bare name resolves only through the effective PATH, which the
	// override rows can replace entirely.
	scan.exe.SetText("codex-fake")
	if !strings.HasPrefix(scan.status.Text, "Codex CLI not found in PATH") {
		t.Errorf("status = %q, want the not-in-PATH message", scan.status.Text)
	}
	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "codex-fake"), "#!/bin/sh\nexit 0\n")
	scan.addEnv.OnTapped()
	scan.addEnv.OnTapped() // one empty row remains, one is filled below
	scan = scanCodexEditor(t, s)
	if len(scan.envName) != 2 || len(scan.envVal) != 2 {
		t.Fatalf("override rows = %d/%d, want 2/2", len(scan.envName), len(scan.envVal))
	}
	scan.envName[0].SetText("PATH")
	scan.envVal[0].SetText(bin)
	scan = scanCodexEditor(t, s)
	if !strings.HasPrefix(scan.status.Text, "Codex CLI found: ") {
		t.Errorf("status = %q, want the resolved PATH hit", scan.status.Text)
	}
	if !strings.Contains(scan.status.Text, filepath.Join(bin, "codex-fake")) {
		t.Errorf("status = %q, want the launcher path inside %q", scan.status.Text, bin)
	}
}

// writeScript creates an executable file for discovery assertions.
func writeScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil { //nolint:gosec // a launcher script must be executable
		t.Fatalf("write %s: %v", path, err)
	}
}

// The collected Codex instance carries the mode, the trimmed launcher and
// the literal override rows; a value containing '=' survives intact, an
// untouched empty row is dropped, and a name-less row is refused locally.
func TestEditorCodexCollect(t *testing.T) {
	delivered := make(chan config.SourceInstance, 1)
	stub := stubDeps()
	stub.Creds = blockedStore{}
	stub.TestConnection = func(ctx context.Context, inst config.SourceInstance,
		typed map[credentials.Slot]string) registry.TestReport {
		delivered <- inst

		return registry.TestReport{Lines: []string{"x"}, OK: true}
	}
	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(domain.KindCodex)
	scan := scanCodexEditor(t, s)
	if scan.mode == nil || scan.exe == nil || scan.addEnv == nil {
		t.Fatal("codex editor widgets missing")
	}

	scan.mode.SetSelected("Persistent")
	scan.exe.SetText("  /opt/codex-wrapper  ")
	scan.addEnv.OnTapped()
	scan.addEnv.OnTapped()
	scan.addEnv.OnTapped()
	scan = scanCodexEditor(t, s)
	scan.envName[0].SetText("HTTPS_PROXY")
	scan.envVal[0].SetText("socks5h://user=pass@proxy.lan:1080")
	scan.envName[1].SetText("EMPTY_BUT_SET") // value stays empty: valid
	// row 3 stays completely empty and must be dropped
	scan = scanCodexEditor(t, s)
	findTestButton(t, s).OnTapped()

	select {
	case got := <-delivered:
		if got.Codex == nil {
			t.Fatal("collected instance lost its codex settings")
		}
		if got.Codex.Executable != "/opt/codex-wrapper" || got.Codex.Mode != config.CodexModePersistent {
			t.Errorf("collected = %+v, want the wrapper path in persistent mode", *got.Codex)
		}
		if len(got.Codex.Env) != 2 {
			t.Fatalf("env overrides = %+v, want two", got.Codex.Env)
		}
		if got.Codex.Env[0].Name != "HTTPS_PROXY" || got.Codex.Env[0].Value != "socks5h://user=pass@proxy.lan:1080" {
			t.Errorf("override[0] = %+v, want the literal value with '=' intact", got.Codex.Env[0])
		}
		if got.Codex.Env[1].Name != "EMPTY_BUT_SET" || got.Codex.Env[1].Value != "" {
			t.Errorf("override[1] = %+v, want the empty-valued override kept", got.Codex.Env[1])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TestConnection was never invoked")
	}

	// A name-less override is refused before any test runs.
	scan = scanCodexEditor(t, s)
	scan.envName[0].SetText("1ST_BAD_NAME")
	findTestButton(t, s).OnTapped()
	select {
	case got := <-delivered:
		t.Fatalf("an invalid override name still reached TestConnection: %+v", got.Codex.Env)
	case <-time.After(100 * time.Millisecond):
	}
}

func findTestButton(t *testing.T, s *Settings) *widget.Button {
	t.Helper()
	var found *widget.Button
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			if b, ok := o.(*widget.Button); ok && b.Text == "Test connection" {
				found = b
			}
		})
	}
	if found == nil {
		t.Fatal("Test connection button not found")
	}

	return found
}

// editorSelects collects every select in the editor overlay.
func editorSelects(t *testing.T, s *Settings) []*widget.Select {
	t.Helper()
	var out []*widget.Select
	for _, overlay := range s.win.Canvas().Overlays().List() {
		walkCanvas(t, overlay, func(o fyne.CanvasObject) {
			if sel, ok := o.(*widget.Select); ok {
				out = append(out, sel)
			}
		})
	}

	return out
}

// paceSelect finds the select offering exactly the pace presets.
func paceSelect(sels []*widget.Select) *widget.Select {
	for _, sel := range sels {
		if len(sel.Options) == len(config.PaceWindows) && sel.Options[0] == config.IntervalLabel(config.PaceWindows[0]) {
			return sel
		}
	}

	return nil
}

// The "Pace window" select exists exactly for the pace-capable kinds,
// defaulting to the 20m preset; local kinds never offer it.
func TestEditorPaceSelectPerKind(t *testing.T) {
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
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			stub := stubDeps()
			stub.Creds = blockedStore{}
			s := NewSettings(test.NewTempApp(t), stub)
			s.editDraft(c.kind)

			sel := paceSelect(editorSelects(t, s))
			if !c.want {
				if sel != nil {
					t.Errorf("%s editor offers a pace select", c.kind)
				}

				return
			}
			if sel == nil {
				t.Fatalf("%s editor lacks the pace select", c.kind)
			}
			if sel.Selected != config.IntervalLabel(config.DefaultPaceWindow) {
				t.Errorf("default pace selection = %q, want %q",
					sel.Selected, config.IntervalLabel(config.DefaultPaceWindow))
			}
		})
	}
}

// Saving maps the selected preset onto the instance: a chosen 30m lands
// as 30 minutes, an untouched select as the default, and a non-pace kind
// keeps the field zero.
func TestEditorPaceSelectCollects(t *testing.T) {
	t.Run("chosen preset", func(t *testing.T) {
		saved := tapEditorSave(t, stubDeps(), func(s *Settings) {
			sel := paceSelect(editorSelects(t, s))
			if sel == nil {
				t.Fatal("pace select missing")
			}
			sel.SetSelected("30m")
		}, domain.KindMiniMax)
		if got := saved[0].PaceWindow; got != 30*time.Minute {
			t.Errorf("saved pace window = %v, want 30m", got)
		}
	})

	t.Run("untouched select saves the default", func(t *testing.T) {
		saved := tapEditorSave(t, stubDeps(), func(*Settings) {}, domain.KindOpenRouter)
		if got := saved[0].PaceWindow; got != config.DefaultPaceWindow {
			t.Errorf("saved pace window = %v, want the default %v", got, config.DefaultPaceWindow)
		}
	})

	t.Run("non-pace kind keeps the field zero", func(t *testing.T) {
		saved := tapEditorSave(t, stubDeps(), func(*Settings) {}, domain.KindAMDGPUWeb)
		if got := saved[0].PaceWindow; got != 0 {
			t.Errorf("saved pace window = %v, want 0", got)
		}
	})
}

// tapEditorSave opens a draft editor of the given kind, lets the caller
// interact with it, taps Save and waits for the asynchronous save to
// reach SaveInstances (the save goroutine writes credentials first).
func tapEditorSave(t *testing.T, stub Deps, act func(*Settings), kind domain.SourceKind) []config.SourceInstance {
	t.Helper()
	stub.Creds = blockedStore{}
	saved := make(chan []config.SourceInstance, 1)
	stub.SaveInstances = func(list []config.SourceInstance) error {
		saved <- list

		return nil
	}
	s := NewSettings(test.NewTempApp(t), stub)
	s.editDraft(kind)
	act(s)

	buttons := map[string]*widget.Button{}
	for _, overlay := range s.win.Canvas().Overlays().List() {
		findButtons(t, overlay, buttons)
	}
	save := buttons["Save"]
	if save == nil {
		t.Fatal("Save button missing")
	}
	save.OnTapped()

	select {
	case list := <-saved:
		if len(list) != 1 {
			t.Fatalf("saved %d instances, want 1", len(list))
		}
		// The save goroutine still owes the UI one hop (close + refresh);
		// let it finish while this test's app is alive, or it lands in
		// whatever test is running by then.
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if len(s.win.Canvas().Overlays().List()) == 0 {
				return list
			}
		}
		t.Fatal("the editor dialog never closed after the save")

		return list
	case <-time.After(2 * time.Second):
		t.Fatal("the save never reached SaveInstances")

		return nil
	}
}
