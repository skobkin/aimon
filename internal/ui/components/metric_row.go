package components

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

// NewMetricRow builds one "Label ……… Value" line from standard widgets:
// a leading label, a spacer, and a trailing value.
func NewMetricRow(label, value string) fyne.CanvasObject {
	return container.NewHBox(
		widget.NewLabel(label),
		layout.NewSpacer(),
		widget.NewLabel(value),
	)
}
