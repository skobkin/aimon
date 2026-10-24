package components

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// neutralText builds a plain text object matching NewMetricRow's value
// styling (theme foreground, body size).
func neutralText(s string) *canvas.Text {
	t := canvas.NewText(s, foregroundColor())
	t.TextSize = bodyTextSize()

	return t
}

// NewMoneyRow builds one "Label ……… $x.xx" line. Sensitive rows render
// their value through SensitiveValue so the global privacy preference
// conceals them until hover; plain rows mirror NewMetricRow.
func NewMoneyRow(vm ui.MoneyMetricVM, hidden func() bool) fyne.CanvasObject {
	var value fyne.CanvasObject
	if vm.Sensitive {
		value = NewSensitiveValue(vm.Text, hidden)
	} else {
		value = neutralText(vm.Text)
	}

	return container.NewHBox(
		widget.NewLabel(vm.Label),
		layout.NewSpacer(),
		value,
	)
}

// NewSpendOverviewBody builds an OpenRouter overview card's compact body:
// usage money rows, the optional key-limit bar, and the Balance row when
// account data exists (S4 UI/UX §4–7).
func NewSpendOverviewBody(vm ui.SpendOverviewVM, hidden func() bool) fyne.CanvasObject {
	if !vm.HasData {
		return noDataLabel()
	}

	body := container.NewVBox()
	for _, row := range vm.Usage {
		body.Add(NewMoneyRow(row, hidden))
	}
	if vm.Limit != nil {
		body.Add(spendLimitBlock(vm.Limit, false))
	}
	if vm.Balance != nil {
		body.Add(NewMoneyRow(*vm.Balance, hidden))
	}

	return body
}

// NewSpendDetailBody builds an OpenRouter Remote detail body: a Usage
// section (Today/Week/Month/Total), the optional limit block with its raw
// amount line, and an Account section when Management-key data exists
// (S4 UI/UX §8–10).
func NewSpendDetailBody(vm ui.SpendVM, hidden func() bool) fyne.CanvasObject {
	if !vm.HasData {
		return noDataLabel()
	}

	body := container.NewVBox(NewSectionLabel("Usage"))
	for _, row := range vm.Usage {
		body.Add(NewMoneyRow(row, hidden))
	}
	// The derived spend speed ("Pace  $1.24/h") follows the usage rows
	// through the same monetary masking as every other money value; it has
	// no bar and never a projection (S7_UI_UX). Absent until the first
	// usable estimate — no collecting placeholder on a money row.
	if vm.Pace != nil && vm.Pace.SpeedText != "" {
		body.Add(NewMoneyRow(ui.MoneyMetricVM{
			Label:     "Pace",
			Text:      vm.Pace.SpeedText,
			Sensitive: true,
		}, hidden))
	}
	if vm.Limit != nil {
		body.Add(spendLimitBlock(vm.Limit, true))
	}
	if len(vm.Account) > 0 {
		body.Add(NewSectionLabel("Account"))
		for _, row := range vm.Account {
			body.Add(NewMoneyRow(row, hidden))
		}
	}

	return body
}

// spendLimitBlock reuses the quota visual language for the key limit: one
// labeled bar ("Monthly limit  NN% used"), never a reset countdown or a
// second fake donut. The raw "used / limit" amount line is detail-only.
func spendLimitBlock(l *ui.SpendLimitVM, detail bool) fyne.CanvasObject {
	row := ui.QuotaWindowVM{
		Name:      l.Label,
		UsedText:  l.UsedText,
		Used:      l.Used,
		Remaining: l.Remaining,
	}
	if detail {
		row.Secondary = l.RawText
	}
	// Limit rows never carry a reset countdown, so the tick refresher is
	// always nil here.
	rowObj, _ := NewQuotaRow(row)

	return rowObj
}

func noDataLabel() fyne.CanvasObject {
	return widget.NewLabelWithStyle("No data yet", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
}
