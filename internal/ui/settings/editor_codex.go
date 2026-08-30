// The ChatGPT Codex editor fields: the execution mode, the launcher with
// its mandatory live discovery status (visible before any connection test),
// and the repeatable environment-override rows. Values are literal
// configuration, never secrets, and are validated on collect through the
// instance's own Validate.
package settings

import (
	"errors"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/source/codex"
)

// codexModeChoice pairs the select's display label with the stored mode.
type codexModeChoice struct {
	label string
	value config.CodexMode
}

var codexModeChoices = []codexModeChoice{
	{label: "On demand", value: config.CodexModeOnDemand},
	{label: "Persistent", value: config.CodexModePersistent},
}

func codexModeLabel(value config.CodexMode) string {
	for _, c := range codexModeChoices {
		if c.value == value {
			return c.label
		}
	}

	return string(value)
}

func codexModeValue(label string) config.CodexMode {
	for _, c := range codexModeChoices {
		if c.label == label {
			return c.value
		}
	}

	return config.CodexModeOnDemand
}

// envRow is one repeatable environment-override row: a portable variable
// name and its literal value. An empty value is a valid override; a
// name-less row is rejected at collect.
type envRow struct {
	name   *widget.Entry
	value  *widget.Entry
	remove *widget.Button
	box    *fyne.Container
}

// buildCodexFields appends the Codex-specific form items in display order.
func (e *editor) buildCodexFields(items *[]*widget.FormItem, s config.CodexSettings) {
	e.mode = widget.NewSelect([]string{
		codexModeLabel(config.CodexModeOnDemand),
		codexModeLabel(config.CodexModePersistent),
	}, nil)
	e.mode.SetSelected(codexModeLabel(s.EffectiveCodexMode()))

	e.exe = widget.NewEntry()
	e.exe.PlaceHolder = config.DefaultCodexExecutable
	e.exe.SetText(s.Executable)
	e.cli = widget.NewLabel("")
	e.cli.Wrapping = fyne.TextWrapWord
	e.exe.OnChanged = func(string) { e.refreshDiscovery() }
	e.refreshDiscovery()

	launcherHelp := widget.NewLabel("AI Monitor runs:\n<executable> app-server --stdio\nExecutable wrappers are supported.")
	launcherHelp.TextStyle = fyne.TextStyle{Italic: true}
	launcherHelp.Wrapping = fyne.TextWrapWord

	modeHelp := widget.NewLabel("On demand: starts Codex only while refreshing; uses no memory between refreshes.\n" +
		"Persistent: keeps a private Codex app-server running, avoiding repeated startup work at about 100 MB of memory.")
	modeHelp.TextStyle = fyne.TextStyle{Italic: true}
	modeHelp.Wrapping = fyne.TextWrapWord

	e.envBox = container.NewVBox()
	for _, override := range s.Env {
		e.appendEnvRow(override.Name, override.Value)
	}
	addEnv := widget.NewButtonWithIcon("Add override", theme.ContentAddIcon(), e.appendEmptyEnvRow)
	envWarning := widget.NewLabel("Values are stored in app settings. Do not put secrets here; " +
		"use a wrapper for secret-bearing proxy configuration.")
	envWarning.TextStyle = fyne.TextStyle{Italic: true}
	envWarning.Wrapping = fyne.TextWrapWord

	*items = append(*items,
		widget.NewFormItem("Mode", e.mode),
		widget.NewFormItem("", modeHelp),
		widget.NewFormItem("Codex executable", container.NewVBox(e.exe, e.cli)),
		widget.NewFormItem("", launcherHelp),
		widget.NewFormItem("Environment overrides (advanced)", container.NewVBox(e.envBox, addEnv)),
		widget.NewFormItem("", envWarning),
	)
}

// appendEnvRow adds one populated override row to the form.
func (e *editor) appendEnvRow(name, value string) {
	row := &envRow{
		name:   widget.NewEntry(),
		value:  widget.NewEntry(),
		remove: widget.NewButtonWithIcon("Remove", theme.DeleteIcon(), nil),
	}
	row.name.PlaceHolder = "VARIABLE_NAME"
	row.name.Validator = func(text string) error {
		if text == "" || config.ValidEnvName(text) {
			return nil
		}

		return errors.New("letters, digits and _ only, not starting with a digit")
	}
	row.value.PlaceHolder = "value (literal, no expansion)"
	// A PATH override changes which launcher a bare name resolves to, so
	// the discovery status follows the override rows as well as the entry.
	changed := func(string) { e.refreshDiscovery() }
	row.name.OnChanged = changed
	row.value.OnChanged = changed
	row.name.SetText(name)
	row.value.SetText(value)
	row.remove.OnTapped = func() {
		e.removeEnvRow(row)
		e.refreshDiscovery()
	}

	row.box = container.NewHBox(row.name, row.value, row.remove)
	e.env = append(e.env, row)
	e.envBox.Add(row.box)
}

// appendEmptyEnvRow adds a blank row for the next override.
func (e *editor) appendEmptyEnvRow() {
	e.appendEnvRow("", "")
	e.envBox.Refresh()
}

func (e *editor) removeEnvRow(row *envRow) {
	for i, candidate := range e.env {
		if candidate != row {
			continue
		}
		e.env = append(e.env[:i], e.env[i+1:]...)

		break
	}
	e.envBox.Remove(row.box)
	e.envBox.Refresh()
}

// draftCodexSettings assembles the editor's current values into typed
// settings. Both the live discovery status and collect go through it, so
// what the status line reports is exactly what a test or save would use.
func (e *editor) draftCodexSettings() config.CodexSettings {
	s := config.CodexSettings{
		Executable: strings.TrimSpace(e.exe.Text),
		Mode:       codexModeValue(e.mode.Selected),
	}
	for _, row := range e.env {
		name := strings.TrimSpace(row.name.Text)
		if name == "" && row.value.Text == "" {
			continue // a row the user never filled in is not an override
		}
		s.Env = append(s.Env, config.EnvOverride{Name: name, Value: row.value.Text})
	}

	return s
}

// refreshDiscovery updates the launcher status label from the current
// draft settings — a pure filesystem lookup, never a process start, and
// always visible before Test Connection (docs/S6_UI_UX.md §Settings).
func (e *editor) refreshDiscovery() {
	if e.cli == nil {
		return
	}
	draft := e.draftCodexSettings()
	d := codex.Discover(draft)
	if d.OK() {
		if strings.ContainsRune(draft.Executable, '/') {
			e.cli.SetText("Executable found")

			return
		}
		e.cli.SetText("Codex CLI found: " + d.Path)

		return
	}
	switch d.State {
	case codex.DiscoveryNotFoundInPath:
		e.cli.SetText("Codex CLI not found in PATH.\nInstall Codex CLI or choose its executable/wrapper below.")
	default:
		e.cli.SetText("Executable not found or not executable.")
	}
}
