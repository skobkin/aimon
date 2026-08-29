package settings

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
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
