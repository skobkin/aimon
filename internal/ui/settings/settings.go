// Package settings implements the decorated Settings window. All network
// and keychain work runs in background goroutines; the UI thread only
// mutates widgets, and every background result crosses back through
// fyne.Do. Saving never blocks the UI thread (the manager's Apply may
// wait out a request timeout, so it runs in the save goroutine).
package settings

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/resources"
)

// testTimeout bounds one Settings connection test.
const testTimeout = 10 * time.Second

// Deps is everything Settings needs from the app. Test functions receive
// the value typed into the password entry (override); an empty override
// means "use the stored credential".
type Deps struct {
	LoadConfig func() config.Config
	// OnSave persists the config and reconfigures the source manager.
	// It may block (manager Apply); it runs on a background goroutine.
	OnSave func(config.Config) error

	Creds credentials.Store

	TestAMDGPU    func(ctx context.Context, baseURL string) ([]domain.GPUInfo, error)
	TestLlamaSwap func(ctx context.Context, baseURL, override string) error
	TestMiniMax   func(ctx context.Context, override string) error
	TestZAI       func(ctx context.Context, baseURL, override string) error

	// Done closes when the app begins quitting; background goroutines
	// bail out instead of touching the UI during teardown.
	Done <-chan struct{}
}

// Settings is the settings window. It is created once and reused;
// Show() resets all widgets from the stored config and discards any
// unsaved edits.
type Settings struct {
	deps Deps
	win  fyne.Window

	// amdgputop-web
	amdEnabled   *widget.Check
	amdURL       *widget.Entry
	amdGPU       *widget.Select
	amdInterval  *widget.Select
	amdTestLabel *widget.Label
	amdGPUs      []domain.GPUInfo // discovery results behind amdGPU options

	// llama-swap
	swapEnabled   *widget.Check
	swapURL       *widget.Entry
	swapInterval  *widget.Select
	swapCred      credRow
	swapTestLabel *widget.Label

	// MiniMax
	mmEnabled   *widget.Check
	mmInterval  *widget.Select
	mmCred      credRow
	mmTestLabel *widget.Label

	// Z.AI
	zaiEnabled   *widget.Check
	zaiURL       *widget.Entry
	zaiInterval  *widget.Select
	zaiCred      credRow
	zaiTestLabel *widget.Label

	keychainLabel *widget.Label
	saveStatus    *widget.Label
	saveBtn       *widget.Button
	cancelBtn     *widget.Button
}

// credRow bundles the credential editing widgets for one account.
type credRow struct {
	account credentials.AccountID
	entry   *widget.Entry // password, always starts empty
	state   *widget.Label // "Credential stored" / "No credential stored" / store state
	remove  *widget.Button
	// enabled tracks whether key editing is currently allowed (keychain
	// availability); mirrored into the widgets.
}

func newCredRow(account credentials.AccountID) credRow {
	entry := widget.NewPasswordEntry()
	entry.PlaceHolder = "API key (leave empty to keep the stored one)"
	return credRow{
		account: account,
		entry:   entry,
		state:   widget.NewLabel(""),
		remove:  widget.NewButtonWithIcon("Remove", theme.DeleteIcon(), nil),
	}
}

// NewSettings builds the window (hidden until Show).
func NewSettings(a fyne.App, deps Deps) *Settings {
	s := &Settings{deps: deps}

	s.amdEnabled = widget.NewCheck("Enabled", nil)
	s.amdURL = widget.NewEntry()
	s.amdURL.PlaceHolder = "http://127.0.0.1:8080"
	s.amdURL.Validator = urlValidator
	s.amdGPU = widget.NewSelect(nil, nil)
	s.amdInterval = widget.NewSelect(nil, nil)
	s.amdTestLabel = widget.NewLabel("")

	s.swapEnabled = widget.NewCheck("Enabled", nil)
	s.swapURL = widget.NewEntry()
	s.swapURL.PlaceHolder = "http://127.0.0.1:8081"
	s.swapURL.Validator = urlValidator
	s.swapInterval = widget.NewSelect(nil, nil)
	s.swapCred = newCredRow(credentials.AccountLlamaSwap)
	s.swapTestLabel = widget.NewLabel("")

	s.mmEnabled = widget.NewCheck("Enabled", nil)
	s.mmInterval = widget.NewSelect(nil, nil)
	s.mmCred = newCredRow(credentials.AccountMiniMax)
	s.mmTestLabel = widget.NewLabel("")

	s.zaiEnabled = widget.NewCheck("Enabled", nil)
	s.zaiURL = widget.NewEntry()
	s.zaiURL.PlaceHolder = "https://api.z.ai"
	s.zaiURL.Validator = urlValidator
	s.zaiInterval = widget.NewSelect(nil, nil)
	s.zaiCred = newCredRow(credentials.AccountZAI)
	s.zaiTestLabel = widget.NewLabel("")

	s.keychainLabel = widget.NewLabel("")
	s.saveStatus = widget.NewLabel("")
	s.saveBtn = widget.NewButtonWithIcon("Save", theme.ConfirmIcon(), s.onSave)
	s.cancelBtn = widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() { s.hide() })

	s.wireRemove(s.swapCred)
	s.wireRemove(s.mmCred)
	s.wireRemove(s.zaiCred)

	localIntervals := intervalOptions(config.LocalIntervals)
	remoteIntervals := intervalOptions(config.RemoteIntervals)
	s.amdInterval.Options = localIntervals
	s.swapInterval.Options = localIntervals
	s.mmInterval.Options = remoteIntervals
	s.zaiInterval.Options = remoteIntervals

	amdForm := widget.NewForm(
		widget.NewFormItem("Enabled", s.amdEnabled),
		widget.NewFormItem("URL", s.amdURL),
		widget.NewFormItem("GPU", gpuField(s)),
		widget.NewFormItem("Refresh every", s.amdInterval),
		widget.NewFormItem("", testButton("Test connection", func() { s.testAMDGPU() })),
		widget.NewFormItem("", s.amdTestLabel),
	)
	swapForm := widget.NewForm(
		widget.NewFormItem("Enabled", s.swapEnabled),
		widget.NewFormItem("URL", s.swapURL),
		widget.NewFormItem("API key", credField(s, s.swapCred)),
		widget.NewFormItem("Refresh every", s.swapInterval),
		widget.NewFormItem("", testButton("Test connection", func() { s.testSwap() })),
		widget.NewFormItem("", s.swapTestLabel),
	)
	mmForm := widget.NewForm(
		widget.NewFormItem("Enabled", s.mmEnabled),
		widget.NewFormItem("API key", credField(s, s.mmCred)),
		widget.NewFormItem("Refresh every", s.mmInterval),
		widget.NewFormItem("", testButton("Test connection", func() { s.testMiniMax() })),
		widget.NewFormItem("", s.mmTestLabel),
	)
	zaiForm := widget.NewForm(
		widget.NewFormItem("Enabled", s.zaiEnabled),
		widget.NewFormItem("URL", s.zaiURL),
		widget.NewFormItem("API key", credField(s, s.zaiCred)),
		widget.NewFormItem("Refresh every", s.zaiInterval),
		widget.NewFormItem("", testButton("Test connection", func() { s.testZAI() })),
		widget.NewFormItem("", s.zaiTestLabel),
	)

	bottom := container.NewHBox(layout.NewSpacer(), s.cancelBtn, s.saveBtn)

	content := container.NewVScroll(container.NewVBox(
		s.keychainLabel,
		widget.NewSeparator(),
		sectionLabel("Local"),
		sectionLabel("amdgputop-web"),
		amdForm,
		sectionLabel("llama-swap"),
		swapForm,
		widget.NewSeparator(),
		sectionLabel("Remote"),
		sectionLabel("MiniMax Token Plan"),
		mmForm,
		sectionLabel("Z.AI / GLM Coding Plan"),
		zaiForm,
		widget.NewSeparator(),
		s.saveStatus,
		bottom,
	))

	win := a.NewWindow("Settings — AI Monitor")
	win.SetIcon(resources.Icon())
	win.SetContent(content)
	win.Resize(fyne.NewSize(560, 680))
	// Closing hides; unsaved edits are discarded and will be reset on
	// the next Show (UI_UX §13).
	win.SetCloseIntercept(func() { s.hide() })
	s.win = win
	return s
}

// Window returns the underlying window.
func (s *Settings) Window() fyne.Window { return s.win }

// Show resets every widget from the stored config and reveals the window.
func (s *Settings) Show() {
	s.reset()
	s.win.Show()
	s.win.RequestFocus()
}

func (s *Settings) hide() { s.win.Hide() }

// reset reloads the config into the widgets and probes the keychain.
func (s *Settings) reset() {
	cfg := s.deps.LoadConfig()

	s.amdEnabled.SetChecked(cfg.AMDGPUWeb.Enabled)
	s.amdURL.SetText(cfg.AMDGPUWeb.BaseURL)
	s.amdURL.Validate()
	s.setGPUOptions(nil, cfg.AMDGPUWeb.SelectedGPUID, "")
	selectInterval(s.amdInterval, cfg.AMDGPUWeb.PollInterval, config.LocalIntervals, config.DefaultLocalInterval)

	s.swapEnabled.SetChecked(cfg.LlamaSwap.Enabled)
	s.swapURL.SetText(cfg.LlamaSwap.BaseURL)
	s.swapURL.Validate()
	selectInterval(s.swapInterval, cfg.LlamaSwap.PollInterval, config.LocalIntervals, config.DefaultLocalInterval)

	s.mmEnabled.SetChecked(cfg.MiniMax.Enabled)
	selectInterval(s.mmInterval, cfg.MiniMax.PollInterval, config.RemoteIntervals, config.DefaultRemoteInterval)

	s.zaiEnabled.SetChecked(cfg.ZAI.Enabled)
	s.zaiURL.SetText(cfg.ZAI.BaseURL)
	s.zaiURL.Validate()
	selectInterval(s.zaiInterval, cfg.ZAI.PollInterval, config.RemoteIntervals, config.DefaultRemoteInterval)

	s.swapCred.entry.SetText("")
	s.mmCred.entry.SetText("")
	s.zaiCred.entry.SetText("")

	s.amdTestLabel.SetText("")
	s.swapTestLabel.SetText("")
	s.mmTestLabel.SetText("")
	s.zaiTestLabel.SetText("")
	s.saveStatus.SetText("")

	s.probeKeychain()
}

// probeKeychain refreshes the backend status line and the per-account
// stored-state labels from the live keychain (nothing is persisted as a
// hint; the secret value is never displayed).
func (s *Settings) probeKeychain() {
	s.keychainLabel.SetText("Checking keychain…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()

		status := s.deps.Creds.Status(ctx)
		stored := map[credentials.AccountID]string{}
		if status.State == credentials.StateAvailable {
			for _, acc := range []credentials.AccountID{s.swapCred.account, s.mmCred.account, s.zaiCred.account} {
				secret, err := s.deps.Creds.Get(ctx, acc)
				switch {
				case err == nil && secret != "":
					stored[acc] = "stored"
				default:
					stored[acc] = "missing"
				}
			}
		}
		if s.closing() {
			return
		}
		fyne.Do(func() { s.applyKeychainState(status, stored) })
	}()
}

func (s *Settings) applyKeychainState(status credentials.Status, stored map[credentials.AccountID]string) {
	switch status.State {
	case credentials.StateAvailable:
		s.keychainLabel.SetText(fmt.Sprintf("Keychain: %s", status.Backend))
	case credentials.StateUnsupported:
		s.keychainLabel.SetText("Keychain: not supported on this platform — credentials cannot be stored")
	default:
		msg := status.Message
		if msg == "" {
			msg = string(status.State)
		}
		s.keychainLabel.SetText(fmt.Sprintf("Keychain: %s (%s) — credentials unavailable", status.Backend, msg))
	}

	usable := status.State == credentials.StateAvailable
	for _, row := range []credRow{s.swapCred, s.mmCred, s.zaiCred} {
		if !usable {
			row.state.SetText("")
			row.entry.Disable()
			row.remove.Disable()
			continue
		}
		row.entry.Enable()
		row.remove.Enable()
		if stored[row.account] == "stored" {
			row.state.SetText("Credential stored")
		} else {
			row.state.SetText("No credential stored")
		}
	}
}

// wireRemove wires the Remove button: deletes the stored credential
// immediately (async) and refreshes the label.
func (s *Settings) wireRemove(row credRow) {
	row.remove.OnTapped = func() {
		row.remove.Disable()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			err := s.deps.Creds.Delete(ctx, row.account)
			if s.closing() {
				return
			}
			fyne.Do(func() {
				row.remove.Enable()
				if err != nil {
					row.state.SetText("Remove failed")
					return
				}
				row.state.SetText("No credential stored")
			})
		}()
	}
}

// onSave validates, then performs all writes on one background goroutine:
// keychain writes first, then OnSave (config persist + manager apply),
// hiding the window only on success.
func (s *Settings) onSave() {
	cfg, errText := s.collectAndValidate()
	if errText != "" {
		s.saveStatus.SetText(errText)
		return
	}

	s.saveBtn.Disable()
	s.cancelBtn.Disable()
	s.saveStatus.SetText("Saving…")

	go func() {
		for _, row := range []credRow{s.swapCred, s.mmCred, s.zaiCred} {
			if v := row.entry.Text; v != "" {
				ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
				err := s.deps.Creds.Set(ctx, row.account, v)
				cancel()
				if err != nil {
					s.finishSave("Keychain write failed")
					return
				}
			}
		}
		if err := s.deps.OnSave(cfg); err != nil {
			s.finishSave("Save failed: " + err.Error())
			return
		}
		s.finishSave("")
	}()
}

// finishSave runs on the save goroutine; only the UI hop is on the UI
// thread.
func (s *Settings) finishSave(errText string) {
	if s.closing() {
		return
	}
	fyne.Do(func() {
		s.saveBtn.Enable()
		s.cancelBtn.Enable()
		if errText == "" {
			s.hide()
		} else {
			s.saveStatus.SetText(errText)
		}
	})
}

// collectAndValidate builds the config from the form and validates URLs.
func (s *Settings) collectAndValidate() (config.Config, string) {
	cfg := s.deps.LoadConfig()

	cfg.AMDGPUWeb.Enabled = s.amdEnabled.Checked
	cfg.AMDGPUWeb.BaseURL = s.amdURL.Text
	cfg.AMDGPUWeb.SelectedGPUID = s.selectedGPUID()
	cfg.AMDGPUWeb.PollInterval = selectedInterval(s.amdInterval, config.LocalIntervals, config.DefaultLocalInterval)

	cfg.LlamaSwap.Enabled = s.swapEnabled.Checked
	cfg.LlamaSwap.BaseURL = s.swapURL.Text
	cfg.LlamaSwap.PollInterval = selectedInterval(s.swapInterval, config.LocalIntervals, config.DefaultLocalInterval)

	cfg.MiniMax.Enabled = s.mmEnabled.Checked
	cfg.MiniMax.PollInterval = selectedInterval(s.mmInterval, config.RemoteIntervals, config.DefaultRemoteInterval)

	cfg.ZAI.Enabled = s.zaiEnabled.Checked
	cfg.ZAI.BaseURL = s.zaiURL.Text
	cfg.ZAI.PollInterval = selectedInterval(s.zaiInterval, config.RemoteIntervals, config.DefaultRemoteInterval)

	if errs := cfg.Validate(); len(errs) > 0 {
		return cfg, "Invalid " + string(errs[0].Source) + " " + errs[0].Field + ": " + errs[0].Message
	}
	return cfg, ""
}

// testAMDGPU runs the connection test and populates the GPU picker from
// discovery.
func (s *Settings) testAMDGPU() {
	s.amdTestLabel.SetText("Testing…")
	base := s.amdURL.Text
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		gpus, err := s.deps.TestAMDGPU(ctx, base)
		if s.closing() {
			return
		}
		fyne.Do(func() {
			if err != nil {
				s.amdTestLabel.SetText(testFailure(err))
				return
			}
			s.setGPUOptions(gpus, "", s.amdGPU.Selected)
			s.amdTestLabel.SetText(fmt.Sprintf("OK — %d GPU(s) discovered", len(gpus)))
		})
	}()
}

func (s *Settings) testSwap() {
	s.runTest(s.swapTestLabel, func(ctx context.Context) error {
		return s.deps.TestLlamaSwap(ctx, s.swapURL.Text, s.swapCred.entry.Text)
	})
}

func (s *Settings) testMiniMax() {
	s.runTest(s.mmTestLabel, func(ctx context.Context) error {
		return s.deps.TestMiniMax(ctx, s.mmCred.entry.Text)
	})
}

func (s *Settings) testZAI() {
	s.runTest(s.zaiTestLabel, func(ctx context.Context) error {
		return s.deps.TestZAI(ctx, s.zaiURL.Text, s.zaiCred.entry.Text)
	})
}

// runTest performs one async connection test; the result label is the
// only UI change and the test never saves anything.
func (s *Settings) runTest(label *widget.Label, test func(ctx context.Context) error) {
	label.SetText("Testing…")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		err := test(ctx)
		if s.closing() {
			return
		}
		fyne.Do(func() {
			if err != nil {
				label.SetText(testFailure(err))
				return
			}
			label.SetText("OK")
		})
	}()
}

func (s *Settings) closing() bool {
	select {
	case <-s.deps.Done:
		return true
	default:
		return false
	}
}

// setGPUOptions fills the GPU picker. Options come from discovery (after a
// test) or from the stored selection alone (before any test this session).
func (s *Settings) setGPUOptions(gpus []domain.GPUInfo, selectedID, keep string) {
	s.amdGPUs = gpus
	options := make([]string, 0, len(gpus)+1)
	options = append(options, "(auto)")
	byOption := map[string]string{}
	for _, g := range gpus {
		opt := gpuOption(g)
		byOption[opt] = g.ID
		options = append(options, opt)
	}
	s.amdGPU.Options = options

	want := ""
	switch {
	case keep != "" && containsOption(options, keep):
		want = keep
	case selectedID != "":
		if opt, ok := optionForID(gpus, selectedID); ok && containsOption(options, opt) {
			want = opt
		}
	}
	if want == "" {
		want = "(auto)"
	}
	s.amdGPU.SetSelected(want)
}

func (s *Settings) selectedGPUID() string {
	if s.amdGPU.Selected == "(auto)" || s.amdGPU.Selected == "" {
		return ""
	}
	for _, g := range s.amdGPUs {
		if gpuOption(g) == s.amdGPU.Selected {
			return g.ID
		}
	}
	// The selection may predate this session's discovery; fall back to
	// matching by ID through the stored config.
	return s.deps.LoadConfig().AMDGPUWeb.SelectedGPUID
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

func sectionLabel(text string) fyne.CanvasObject {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

func testButton(label string, onTap func()) fyne.CanvasObject {
	return widget.NewButton(label, onTap)
}

func gpuField(s *Settings) fyne.CanvasObject { return s.amdGPU }

func credField(s *Settings, row credRow) fyne.CanvasObject {
	return container.NewVBox(row.entry, container.NewHBox(row.state, row.remove))
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

// testFailure renders a test failure using the source error's user
// message when available.
func testFailure(err error) string {
	return "Failed: " + err.Error()
}

// urlValidator validates a base URL entry inline.
func urlValidator(s string) error {
	if s == "" {
		return nil // empty falls back to the default on sanitize
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must be an http(s) URL with a host")
	}
	return nil
}
