package components

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// NewMetricRow builds one "Label ……… Value" line from standard objects:
// a leading label, a spacer, and a trailing value colored by its metric
// level (neutral renders like plain label text). canvas.Text stands in
// for the value because widget.Label has no per-instance color API.
func NewMetricRow(row ui.MetricRowVM) fyne.CanvasObject {
	value := canvas.NewText(row.Value, LevelColor(row.Level))
	value.TextSize = bodyTextSize()
	return container.NewHBox(
		widget.NewLabel(row.Label),
		layout.NewSpacer(),
		value,
	)
}

// bodyTextSize matches the standard label text size. Outside a running
// app (headless tests) the theme lookup panics, so Fyne's default size
// stands in.
func bodyTextSize() float32 {
	if fyne.CurrentApp() == nil {
		return 14
	}
	return theme.Size(theme.SizeNameText)
}
