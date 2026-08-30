// The Add/Edit editor dialog for one source instance. The instance's
// identity (its generated ID) is fixed for the dialog's lifetime: Add
// generated it when the editor opened, Edit uses the existing one. Test
// Connection runs against the editor's unsaved values and never persists
// anything; Save writes a typed secret (if any) and then commits the
// configuration through the guarded mutation path; Cancel discards
// everything.
package settings

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
)

// editor edits one instance (a draft for Add, the stored values for Edit).
type editor struct {
	s      *Settings
	inst   config.SourceInstance // ID and Kind never change here
	isNew  bool
	def    registry.Definition
	dlg    dialog.Dialog
	credOK bool // keychain available: credential widgets usable

	name     *widget.Entry
	enabled  *widget.Check
	interval *widget.Select

	url    *widget.Entry // amdgputop-web / llama-swap / zai / hindsight
	webURL *widget.Entry // hindsight only: optional independent Web UI URL
	gpu    *widget.Select
	gpus   []domain.GPUInfo
	creds  []*credRow // one per credential slot, definition order
	hasURL bool

	// Codex-only widgets, nil for every other kind: the execution mode, the
	// launcher with its live discovery status, and the env override rows.
	mode   *widget.Select
	exe    *widget.Entry
	cli    *widget.Label
	envBox *fyne.Container
	env    []*envRow

	testLabel *widget.Label
	saveBtn   *widget.Button
	cancelBtn *widget.Button
}

// credRow bundles the credential editing widgets for one instance's
// credential slot.
type credRow struct {
	slot    credentials.Slot
	account credentials.AccountID
	entry   *widget.Entry // password, always starts empty
	state   *widget.Label // "Credential stored" / "No credential stored" / store state
	remove  *widget.Button
}

func newCredRow(slot credentials.Slot, account credentials.AccountID) credRow {
	entry := widget.NewPasswordEntry()
	entry.PlaceHolder = fmt.Sprintf("%s (leave empty to keep the stored one)", slotDisplayName(slot))

	return credRow{
		slot:    slot,
		account: account,
		entry:   entry,
		state:   widget.NewLabel(""),
		remove:  widget.NewButtonWithIcon("Remove", theme.DeleteIcon(), nil),
	}
}

// slotDisplayName names a credential slot in the editor form and test
// output. Most kinds carry exactly one API-key slot; OpenRouter adds its
// independent Management-key slot.
func slotDisplayName(slot credentials.Slot) string {
	if slot == credentials.SlotManagementKey {
		return "Management key"
	}

	return "API key"
}

// openEditorFor builds and shows the editor dialog.
func (s *Settings) openEditorFor(inst config.SourceInstance, isNew bool) {
	def, ok := registry.Get(inst.Kind)
	if !ok {
		return
	}
	e := &editor{s: s, inst: inst, isNew: isNew, def: def}

	e.name = widget.NewEntry()
	e.name.SetText(inst.Name)
	e.enabled = widget.NewCheck("", nil)
	e.enabled.SetChecked(inst.Enabled)
	e.interval = widget.NewSelect(intervalOptions(def.Intervals), nil)
	selectInterval(e.interval, inst.PollInterval, def.Intervals, def.DefaultInterval)
	e.testLabel = widget.NewLabel("")
	e.testLabel.Wrapping = fyne.TextWrapWord

	items := []*widget.FormItem{
		widget.NewFormItem("Type", widget.NewLabel(def.DefaultName)), // kind is immutable
		widget.NewFormItem("Name", e.name),
		widget.NewFormItem("Enabled", e.enabled),
		widget.NewFormItem("Refresh every", e.interval),
	}

	// A URL field exists only for kinds whose settings have a base URL —
	// MiniMax and OpenRouter use fixed endpoints.
	settings, _ := inst.Settings()
	switch settings.(type) {
	case config.MiniMaxSettings, config.OpenRouterSettings, config.CodexSettings:
		e.hasURL = false
	default:
		e.hasURL = true
	}
	if e.hasURL {
		e.url = widget.NewEntry()
		e.url.Validator = urlValidator
		var placeholder string
		urlLabel := "URL"
		switch st := def.DefaultSettings().(type) {
		case config.AMDGPUWebSettings:
			placeholder = st.BaseURL
			e.url.SetText(inst.AMDGPUWeb.BaseURL)
		case config.LlamaSwapSettings:
			placeholder = st.BaseURL
			e.url.SetText(inst.LlamaSwap.BaseURL)
		case config.ZAISettings:
			placeholder = st.BaseURL
			e.url.SetText(inst.ZAI.BaseURL)
		case config.HindsightSettings:
			placeholder = st.BaseURL
			e.url.SetText(inst.Hindsight.BaseURL)
			// The Web UI URL is a separate field below; the API URL must
			// say it is the API one.
			urlLabel = "API URL"
		}
		e.url.PlaceHolder = placeholder
		// Force the validation state on load; the result is only shown.
		_ = e.url.Validate()
		items = append(items, widget.NewFormItem(urlLabel, e.url))
	}
	if inst.Kind == domain.KindHindsight {
		e.webURL = widget.NewEntry()
		e.webURL.Validator = urlValidator
		e.webURL.PlaceHolder = "http://127.0.0.1:9999 (optional)"
		e.webURL.SetText(inst.Hindsight.WebUIURL)
		_ = e.webURL.Validate()
		items = append(items, widget.NewFormItem("Web UI URL", e.webURL))
	}
	if inst.Kind == domain.KindAMDGPUWeb {
		e.gpu = widget.NewSelect(nil, nil)
		e.setGPUOptions(nil, inst.AMDGPUWeb.SelectedGPUID, "")
		items = append(items, widget.NewFormItem("GPU", e.gpu))
	}
	if inst.Kind == domain.KindCodex {
		e.buildCodexFields(&items, settings.(config.CodexSettings))
	}
	for _, slot := range def.CredentialSlots {
		account, err := credentials.AccountForInstance(inst.ID, slot)
		if err != nil {
			s.status.SetText(err.Error())

			return
		}
		row := newCredRow(slot, account)
		e.creds = append(e.creds, &row)
		items = append(items, widget.NewFormItem(slotDisplayName(slot), e.credField(&row)))
	}
	e.wireRemove()

	items = append(items,
		widget.NewFormItem("", widget.NewButton("Test connection", e.onTest)),
		widget.NewFormItem("", e.testLabel),
	)

	form := widget.NewForm(items...)

	e.saveBtn = widget.NewButtonWithIcon("Save", theme.ConfirmIcon(), e.onSave)
	e.cancelBtn = widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() { e.close() })

	title := "Add source"
	if !isNew {
		title = "Edit source"
	}
	e.dlg = dialog.NewCustomWithoutButtons(title, container.NewVBox(
		form,
		widget.NewSeparator(),
		// A grid row stretches the two buttons across the full content
		// width. An HBox shrinks them to their minimum, and an HScroll
		// wrapper collapses the whole row to the scrollbar strip.
		container.NewGridWithColumns(2, e.cancelBtn, e.saveBtn),
	), s.win)
	e.dlg.Show()
	s.probeEditorKeychain(e)
}

func (e *editor) close() { e.dlg.Hide() }

// credField lays out the credential widgets for one slot: the password
// entry, the stored-state/remove row, and the kind's optional usage hint.
func (e *editor) credField(row *credRow) fyne.CanvasObject {
	parts := []fyne.CanvasObject{row.entry, container.NewHBox(row.state, row.remove)}
	if e.def.SlotHint != nil {
		if hint := e.def.SlotHint(row.slot); hint != "" {
			hintLabel := widget.NewLabel(hint)
			hintLabel.TextStyle = fyne.TextStyle{Italic: true}
			hintLabel.Wrapping = fyne.TextWrapWord
			parts = append(parts, hintLabel)
		}
	}

	return container.NewVBox(parts...)
}

// probeEditorKeychain sets every credential row's stored-state label from
// the live keychain and enables or disables the credential widgets. One
// status probe and one read per slot — slots are independent.
func (s *Settings) probeEditorKeychain(e *editor) {
	if len(e.creds) == 0 {
		return
	}
	for _, row := range e.creds {
		row.state.SetText("Checking keychain…")
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, testTimeout)
		defer cancel()

		status := s.deps.Creds.Status(ctx)
		stored := make(map[credentials.Slot]bool, len(e.creds))
		if status.State == credentials.StateAvailable {
			for _, row := range e.creds {
				secret, err := s.deps.Creds.Get(ctx, row.account)
				stored[row.slot] = err == nil && secret != ""
			}
		}
		if s.closing() {
			return
		}
		fyne.Do(func() {
			e.credOK = status.State == credentials.StateAvailable
			for _, row := range e.creds {
				if !e.credOK {
					row.entry.Disable()
					row.remove.Disable()
					row.state.SetText("")

					continue
				}
				row.entry.Enable()
				row.remove.Enable()
				if stored[row.slot] {
					row.state.SetText("Credential stored")
				} else {
					row.state.SetText("No credential stored")
				}
			}
		})
	}()
}

// wireRemove wires each credential row's Remove button: it deletes that
// slot's stored secret immediately (async) and refreshes its label. A
// missing credential already satisfies the request. Removing one slot never
// touches another.
func (e *editor) wireRemove() {
	for _, row := range e.creds {
		row := row
		row.remove.OnTapped = func() {
			row.remove.Disable()
			go func() {
				ctx, cancel := context.WithTimeout(e.s.ctx, testTimeout)
				defer cancel()
				err := e.s.deps.Creds.Delete(ctx, row.account)
				if e.s.closing() {
					return
				}
				fyne.Do(func() {
					row.remove.Enable()
					switch {
					case err == nil, errors.Is(err, credentials.ErrNotFound):
						row.state.SetText("No credential stored")
					default:
						row.state.SetText("Remove failed")
					}
				})
			}()
		}
	}
}

// collect builds the instance from the editor's current values and
// validates it. The ID and kind come from the dialog's fixed instance.
func (e *editor) collect() (config.SourceInstance, error) {
	inst := e.inst
	inst.Name = strings.TrimSpace(e.name.Text)
	inst.Enabled = e.enabled.Checked
	inst.PollInterval = selectedInterval(e.interval, e.def.Intervals, e.def.DefaultInterval)

	switch inst.Kind {
	case domain.KindAMDGPUWeb:
		s := *inst.AMDGPUWeb
		s.BaseURL = e.url.Text
		s.SelectedGPUID = e.selectedGPUID()
		inst.AMDGPUWeb = &s
	case domain.KindLlamaSwap:
		s := *inst.LlamaSwap
		s.BaseURL = e.url.Text
		inst.LlamaSwap = &s
	case domain.KindMiniMax:
		s := *inst.MiniMax
		inst.MiniMax = &s
	case domain.KindZAI:
		s := *inst.ZAI
		s.BaseURL = e.url.Text
		inst.ZAI = &s
	case domain.KindOpenRouter:
		s := *inst.OpenRouter
		inst.OpenRouter = &s
	case domain.KindHindsight:
		s := *inst.Hindsight
		s.BaseURL = e.url.Text
		// An empty Web UI URL stays empty: the card's "Open" button and the
		// tray entry only appear when one is configured.
		s.WebUIURL = strings.TrimSpace(e.webURL.Text)
		inst.Hindsight = &s
	case domain.KindCodex:
		s := e.draftCodexSettings()
		inst.Codex = &s
	}

	if errs := inst.Validate(); len(errs) > 0 {
		return inst, fmt.Errorf("invalid: %s", errs[0].Message)
	}

	return inst, nil
}

// onTest runs the connection test with the editor's current values. It
// never persists anything; for amdgputop-web it doubles as GPU discovery.
func (e *editor) onTest() {
	inst, err := e.collect()
	if err != nil {
		e.testLabel.SetText(err.Error() + " — fix the fields before testing")

		return
	}
	typed := make(map[credentials.Slot]string, len(e.creds))
	for _, row := range e.creds {
		typed[row.slot] = row.entry.Text
	}
	// One Codex probe spans spawn + initialize + read + reap, so the caller
	// (not the adapter) owns the longer Codex-specific budget; every other
	// kind keeps the shared test timeout.
	timeout := testTimeout
	if inst.Kind == domain.KindCodex {
		timeout = codexTestTimeout
	}
	e.testLabel.SetText("Testing…")
	go func() {
		ctx, cancel := context.WithTimeout(e.s.ctx, timeout)
		defer cancel()

		if inst.Kind == domain.KindAMDGPUWeb {
			gpus, derr := e.s.deps.DiscoverGPUs(ctx, inst.AMDGPUWeb.BaseURL)
			if e.s.closing() {
				return
			}
			fyne.Do(func() {
				if derr != nil {
					e.testLabel.SetText(testFailure(derr))

					return
				}
				keep := e.gpu.Selected
				e.setGPUOptions(gpus, "", keep)
				e.testLabel.SetText(fmt.Sprintf("OK — %d GPU(s) discovered", len(gpus)))
			})

			return
		}

		report := e.s.deps.TestConnection(ctx, inst, typed)
		if e.s.closing() {
			return
		}
		fyne.Do(func() {
			e.testLabel.SetText(strings.Join(report.Lines, "\n"))
		})
	}()
}

// onSave commits the editor: a typed secret is written to the instance's
// keychain account first (with the editor's fixed ID — a draft's secret
// belongs to the ID Save will persist), then the configuration goes
// through the guarded mutation path. For a new instance whose config
// persist failed, the just-written secret is removed again so no orphaned
// credential survives a failed Add.
func (e *editor) onSave() {
	inst, err := e.collect()
	if err != nil {
		e.testLabel.SetText(err.Error())

		return
	}
	if !e.s.mutBusy.CompareAndSwap(false, true) {
		e.testLabel.SetText("Another change is still saving…")

		return
	}
	e.saveBtn.Disable()
	e.cancelBtn.Disable()
	e.testLabel.SetText("Saving…")

	go func() {
		// Each typed secret is stored under its own slot's account, in
		// definition order. The first failure aborts the save entirely; the
		// config document has not been touched yet.
		wroteAny := false
		for _, row := range e.creds {
			if row.entry.Text == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			err := e.s.deps.Creds.Set(ctx, row.account, row.entry.Text)
			cancel()
			if err != nil {
				e.finishSave("Keychain write failed: " + err.Error())

				return
			}
			wroteAny = true
		}

		saveErr := e.s.deps.SaveInstances(ReplaceInstance(e.s.deps.LoadInstances(), inst))
		if saveErr != nil {
			if e.isNew && wroteAny {
				ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
				_ = e.s.deps.CleanupCredentials(ctx, inst) // best effort; the add failed
				cancel()
			}
			e.finishSave("Save failed: " + saveErr.Error())

			return
		}
		e.finishSave("")
	}()
}

// finishSave runs on the save goroutine; only the UI hop is on the UI
// thread. Success closes the editor and refreshes the list.
func (e *editor) finishSave(errText string) {
	if e.s.closing() {
		return
	}
	fyne.Do(func() {
		e.s.mutBusy.Store(false)
		e.saveBtn.Enable()
		e.cancelBtn.Enable()
		if errText == "" {
			e.close()
			e.s.status.SetText("")
			e.s.refresh()

			return
		}
		e.testLabel.SetText(errText)
	})
}

// setGPUOptions fills the GPU picker. Options come from discovery (after a
// test) or from the stored selection alone (before any test this session).
func (e *editor) setGPUOptions(gpus []domain.GPUInfo, selectedID, keep string) {
	e.gpus = gpus
	options := make([]string, 0, len(gpus)+1)
	options = append(options, "(auto)")
	for _, g := range gpus {
		options = append(options, gpuOption(g))
	}
	if selectedID != "" && !containsOption(options, selectedID) {
		// No discovery yet this session: offer the stored ID verbatim so
		// the selection stays visible.
		options = append(options, selectedID)
	}
	e.gpu.Options = options

	want := ""
	switch {
	case keep != "" && containsOption(options, keep):
		want = keep
	case selectedID != "":
		if opt, ok := optionForID(gpus, selectedID); ok && containsOption(options, opt) {
			want = opt
		} else {
			want = selectedID
		}
	}
	if want == "" {
		want = "(auto)"
	}
	e.gpu.SetSelected(want)
}

// selectedGPUID maps the picker selection back to a GPU ID: "(auto)" is
// empty; a discovered option maps through this session's GPUs; anything
// else is a stored ID shown verbatim.
func (e *editor) selectedGPUID() string {
	if e.gpu.Selected == "(auto)" || e.gpu.Selected == "" {
		return ""
	}
	for _, g := range e.gpus {
		if gpuOption(g) == e.gpu.Selected {
			return g.ID
		}
	}

	return e.gpu.Selected
}

func gpuOption(g domain.GPUInfo) string {
	if g.Name != "" {
		return fmt.Sprintf("%s (%s)", g.Name, g.ID)
	}

	return g.ID
}

func optionForID(gpus []domain.GPUInfo, id string) (string, bool) {
	for _, g := range gpus {
		if g.ID == id {
			return gpuOption(g), true
		}
	}

	return "", false
}

func containsOption(options []string, want string) bool {
	for _, o := range options {
		if o == want {
			return true
		}
	}

	return false
}

// intervalOptions maps preset durations onto their display labels.
func intervalOptions(presets []time.Duration) []string {
	opts := make([]string, 0, len(presets))
	for _, d := range presets {
		opts = append(opts, config.IntervalLabel(d))
	}

	return opts
}

// selectInterval selects the option matching d, falling back to the
// category default.
func selectInterval(sel *widget.Select, d time.Duration, presets []time.Duration, def time.Duration) {
	for _, p := range presets {
		if p == d {
			sel.SetSelected(config.IntervalLabel(p))

			return
		}
	}
	sel.SetSelected(config.IntervalLabel(def))
}

// selectedInterval maps the selected label back to a preset duration.
func selectedInterval(sel *widget.Select, presets []time.Duration, def time.Duration) time.Duration {
	for _, p := range presets {
		if config.IntervalLabel(p) == sel.Selected {
			return p
		}
	}

	return def
}

// urlValidator validates a base URL entry inline.
func urlValidator(s string) error {
	if s == "" {
		return nil // the config validator reports required URLs with context
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must be an http(s) URL with a host")
	}

	return nil
}
