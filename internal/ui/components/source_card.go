package components

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// NewSourceCard builds a source card: a standard widget.Card whose content
// is a VBox of header row, body, and optional footer. The header row is
// composed from standard Fyne objects — bold title, spacer, right-aligned
// freshness — reproducing the wireframe's "Source name    5s ago".
//
// The second return sets the freshness label; the monitor's ticker calls
// it once a second while visible instead of rebuilding the card.
func NewSourceCard(vm ui.CardVM, body fyne.CanvasObject) (fyne.CanvasObject, func(status string)) {
	status := widget.NewLabel(vm.Status)
	header := container.NewHBox(
		widget.NewLabelWithStyle(vm.Title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		layout.NewSpacer(),
		status,
	)

	content := container.NewVBox(header, body)
	if vm.Footer != "" {
		// Plain error text; the wording carries the meaning, never
		// color alone (NFR).
		content.Add(widget.NewLabelWithStyle(vm.Footer, fyne.TextAlignLeading, fyne.TextStyle{Italic: true}))
	}

	return widget.NewCard("", "", content), func(s string) { setText(status, s) }
}

// NewEmptyState builds the placeholder shown in a tab whose sources are
// all disabled, with a shortcut into Settings.
func NewEmptyState(message string, onOpenSettings func()) fyne.CanvasObject {
	return container.NewVBox(
		container.NewPadded(widget.NewLabelWithStyle(message, fyne.TextAlignCenter, fyne.TextStyle{Italic: true})),
		container.NewHBox(
			layout.NewSpacer(),
			widget.NewButtonWithIcon("Open Settings", theme.SettingsIcon(), onOpenSettings),
			layout.NewSpacer(),
		),
	)
}

// NewSectionLabel builds a small section heading ("Local", "Remote").
func NewSectionLabel(text string) fyne.CanvasObject {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}
