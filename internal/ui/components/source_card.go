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
// composed from standard Fyne objects — bold title, right-aligned
// freshness — reproducing the wireframe's "Source name    5s ago".
//
// The title sits in a Border centre and wraps, while the freshness keeps
// its natural width in the right slot. Both are free text (a user-named
// source, an age with a "· stale" suffix) and neither may widen the
// window: an HBox hands out minimum widths, so a long title or a stale
// status there once pushed the paired overview columns past the fixed
// monitor width and clipped everything right of it.
//
// The second return sets the freshness label; the monitor's ticker calls
// it once a second while visible instead of rebuilding the card.
func NewSourceCard(vm ui.CardVM, body fyne.CanvasObject) (fyne.CanvasObject, func(status string)) {
	status := widget.NewLabel(vm.Status)
	header := container.NewBorder(nil, nil, nil, status,
		NewWrappedLabel(vm.Title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	)

	content := container.NewVBox(header, body)
	if vm.Footer != "" {
		// Plain error text; the wording carries the meaning, never
		// color alone (NFR). It wraps: a failure message is a full
		// sentence ("Last refresh failed: This Codex CLI does not
		// support …"), and an unwrapped line would widen the card's
		// scroll container — and with it the whole window — to that
		// sentence's single-line width.
		content.Add(NewWrappedLabel(vm.Footer, fyne.TextAlignLeading, fyne.TextStyle{Italic: true}))
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

// NewWrappedLabel builds a label carrying dynamic free text (failure
// messages, model names, help paragraphs). It wraps: a wrapping label's
// minimum width is one word, so its text never stretches the scroll
// container — and with it the window — to its own single-line width, and it
// still uses the full width the container offers.
//
// Only labels that already receive their container's full width (VBox
// children) may wrap: an HBox hands out minimum widths, which would squeeze
// a wrapping label into a sliver. Short static labels stay on NewLabelWith
// Style, which keeps them on one line.
func NewWrappedLabel(text string, alignment fyne.TextAlign, style fyne.TextStyle) *widget.Label {
	l := widget.NewLabelWithStyle(text, alignment, style)
	l.Wrapping = fyne.TextWrapWord

	return l
}
