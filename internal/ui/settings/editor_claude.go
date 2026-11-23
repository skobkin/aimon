// The claude.ai editor fields: the credentials file path (Claude Code's own
// login file, read-only) with a live found/missing status. Nothing here is a
// secret; the file is only stat'ed in the editor.
package settings

import (
	"os"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
)

// buildClaudeFields appends the credentials-file entry and its status.
func (e *editor) buildClaudeFields(items *[]*widget.FormItem, s config.ClaudeSettings) {
	e.credsFile = widget.NewEntry()
	e.credsFile.PlaceHolder = config.DefaultClaudeCredentialsFile
	e.credsFile.SetText(s.CredentialsFile)
	e.credsStatus = widget.NewLabel("")
	e.credsStatus.Wrapping = fyne.TextWrapWord
	e.credsFile.OnChanged = func(string) { e.refreshCredsStatus() }
	e.refreshCredsStatus()

	help := widget.NewLabel("Read-only: AI Monitor only reads the access token from Claude Code's login file and never refreshes it. Run claude to log in or refresh.")
	help.TextStyle = fyne.TextStyle{Italic: true}
	help.Wrapping = fyne.TextWrapWord

	*items = append(*items,
		widget.NewFormItem("Credentials file", e.credsFile),
		widget.NewFormItem("", e.credsStatus),
		widget.NewFormItem("", help),
	)
}

func (e *editor) draftClaudeSettings() config.ClaudeSettings {
	return config.ClaudeSettings{CredentialsFile: strings.TrimSpace(e.credsFile.Text)}
}

func (e *editor) refreshCredsStatus() {
	if e.credsStatus == nil {
		return
	}
	path := e.draftClaudeSettings().EffectiveCredentialsFile()
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		e.credsStatus.SetText("Credentials file not found.")

		return
	}
	e.credsStatus.SetText("Credentials file found.")
}
