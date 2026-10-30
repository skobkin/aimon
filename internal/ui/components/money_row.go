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

// SensitiveRows hands the spend bodies their per-row privacy predicates and
// receives hover reports from the masked values. The monitor implements it
// with per-row reveal tracking, so a tree rebuild restores the reveal of
// the row the pointer is still over (Fyne recomputes hover only on pointer
// movement); StaticSensitiveRows provides the plain-predicate behavior for
// callers without rebuild-tracking.
type SensitiveRows interface {
	// Hidden returns the privacy predicate ("concealed") for one row label.
	Hidden(label string) func() bool
	// Hover reports that the pointer entered or left the row's value.
	Hover(label string, revealed bool)
}

// staticRows adapts one bare privacy predicate for every row.
type staticRows func() bool

// StaticSensitiveRows wraps a bare privacy predicate (nil = never hide)
// as a SensitiveRows without per-row reveal tracking.
func StaticSensitiveRows(hidden func() bool) SensitiveRows {
	if hidden == nil {
		hidden = func() bool { return false }
	}

	return staticRows(hidden)
}

func (s staticRows) Hidden(string) func() bool { return func() bool { return s() } }

func (s staticRows) Hover(string, bool) {}

// sensitiveValueFor builds one row's masked value wired to rows: the
// predicate comes from the row label and hover transitions are reported
// back so the owner can carry the reveal across rebuilds.
func sensitiveValueFor(vm ui.MoneyMetricVM, rows SensitiveRows) *SensitiveValue {
	v := NewSensitiveValue(vm.Text, rows.Hidden(vm.Label))
	v.SetHoverCallback(func(revealed bool) { rows.Hover(vm.Label, revealed) })

	return v
}

// NewMoneyRow builds one "Label ……… $x.xx" line. Sensitive rows render
// their value through SensitiveValue so the global privacy preference
// conceals them until hover; plain rows mirror NewMetricRow.
func NewMoneyRow(vm ui.MoneyMetricVM, rows SensitiveRows) fyne.CanvasObject {
	var value fyne.CanvasObject
	if vm.Sensitive {
		value = sensitiveValueFor(vm, rows)
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
func NewSpendOverviewBody(vm ui.SpendOverviewVM, rows SensitiveRows) fyne.CanvasObject {
	if !vm.HasData {
		return noDataLabel()
	}

	body := container.NewVBox()
	for _, row := range vm.Usage {
		body.Add(NewMoneyRow(row, rows))
	}
	if vm.Limit != nil {
		body.Add(spendLimitBlock(vm.Limit, false))
	}
	if vm.Balance != nil {
		body.Add(NewMoneyRow(*vm.Balance, rows))
	}

	return body
}

// NewSpendDetailBody builds an OpenRouter Remote detail body: a Usage
// section (Today/Week/Month/Total), the optional limit block with its raw
// amount line, and an Account section when Management-key data exists
// (S4 UI/UX §8–10).
func NewSpendDetailBody(vm ui.SpendVM, rows SensitiveRows) fyne.CanvasObject {
	if !vm.HasData {
		return noDataLabel()
	}

	body := container.NewVBox(NewSectionLabel("Usage"))
	for _, row := range vm.Usage {
		body.Add(NewMoneyRow(row, rows))
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
		}, rows))
	}
	if vm.Limit != nil {
		body.Add(spendLimitBlock(vm.Limit, true))
	}
	if len(vm.Account) > 0 {
		body.Add(NewSectionLabel("Account"))
		for _, row := range vm.Account {
			body.Add(NewMoneyRow(row, rows))
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
