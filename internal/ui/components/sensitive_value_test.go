package components

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/pace"
	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// hiddenSensitive builds a value under an enabled privacy preference.
func hiddenSensitive(text string) *SensitiveValue {
	return NewSensitiveValue(text, func() bool { return true })
}

func TestSensitiveValueMasksUntilHover(t *testing.T) {
	v := hiddenSensitive("$73.42")
	if got := v.DisplayText(); got != maskText {
		t.Errorf("hidden and unhovered = %q, want %q", got, maskText)
	}
	if !v.Masked() {
		t.Error("Masked() must be true before hover")
	}

	v.MouseIn(&desktop.MouseEvent{})
	if got := v.DisplayText(); got != "$73.42" {
		t.Errorf("hovered = %q, want the real value", got)
	}

	v.MouseOut()
	if got := v.DisplayText(); got != maskText {
		t.Errorf("after mouse out = %q, want %q", got, maskText)
	}

	// Masking never mutates the underlying value.
	if v.Text() != "$73.42" {
		t.Errorf("stored text mutated by masking: %q", v.Text())
	}
}

func TestSensitiveValuePrivacyOffAlwaysShowsRealText(t *testing.T) {
	v := NewSensitiveValue("$73.42", func() bool { return false })
	if got := v.DisplayText(); got != "$73.42" {
		t.Errorf("privacy off = %q, want the real value", got)
	}
	if v.Masked() {
		t.Error("Masked() must be false when the preference shows values")
	}

	// Hover must not matter when the preference shows values.
	v.MouseIn(&desktop.MouseEvent{})
	v.MouseOut()
	if v.DisplayText() != "$73.42" || v.Masked() {
		t.Errorf("privacy off after hover = %q (masked=%v)", v.DisplayText(), v.Masked())
	}
}

// A nil predicate means "never hide", the fail-open default for callers
// that have no privacy wiring.
func TestSensitiveValueNilPredicateShowsRealText(t *testing.T) {
	v := NewSensitiveValue("$73.42", nil)
	if v.Masked() || v.DisplayText() != "$73.42" {
		t.Errorf("nil predicate = masked=%v text=%q", v.Masked(), v.DisplayText())
	}
}

func TestSensitiveValueSetText(t *testing.T) {
	v := hiddenSensitive("$1.00")
	v.SetText("$105.89")
	if v.Text() != "$105.89" {
		t.Errorf("SetText stored %q", v.Text())
	}
	if v.DisplayText() != maskText {
		t.Errorf("changed value still masks: %q", v.DisplayText())
	}
	v.MouseIn(&desktop.MouseEvent{})
	if v.DisplayText() != "$105.89" {
		t.Errorf("revealed changed value = %q", v.DisplayText())
	}
}

// The reason the renderer measures both strings at once: the widget's
// minimum size is identical concealed vs revealed, so toggling cannot
// reflow the row or shift it away under a stationary pointer.
func TestSensitiveValueGeometryStableAcrossReveal(t *testing.T) {
	test.NewTempApp(t)

	v := hiddenSensitive("$123456.78") // wider than the mask
	concealed := v.MinSize()
	v.MouseIn(&desktop.MouseEvent{})
	revealed := v.MinSize()

	if concealed != revealed {
		t.Errorf("concealed min = %v, revealed min = %v — layout would reflow",
			concealed, revealed)
	}
	if concealed.Width <= 0 {
		t.Fatal("the widget was never measured")
	}

	// Width covers whichever text is wider; here the real value is.
	maskedWidth := NewSensitiveValue("", func() bool { return true }).MinSize().Width
	if concealed.Width < maskedWidth {
		t.Errorf("min width %v below the masked text width %v", concealed.Width, maskedWidth)
	}
}

func TestNewMoneyRowSensitiveRowsUseSensitiveValue(t *testing.T) {
	sensitive := ui.MoneyMetricVM{Label: "Balance", Text: "$73.42", Sensitive: true}
	row := NewMoneyRow(sensitive, func() bool { return true })

	values := sensitiveValuesIn(t, row)
	if len(values) != 1 {
		t.Fatalf("found %d SensitiveValue(s), want 1", len(values))
	}
	if !values[0].Masked() || values[0].DisplayText() != maskText {
		t.Errorf("sensitive row not masked: %q", values[0].DisplayText())
	}

	plain := ui.MoneyMetricVM{Label: "Today", Text: "$0.42"}
	if sv := sensitiveValuesIn(t, NewMoneyRow(plain, nil)); len(sv) != 0 {
		t.Error("a non-sensitive row must not build a SensitiveValue")
	}
}

func TestSpendBodiesShape(t *testing.T) {
	test.NewTempApp(t)

	limit := &ui.SpendLimitVM{
		Label: "Monthly limit", UsedText: "50% used", Used: 0.5, Remaining: 0.5,
		RawText: "$12.47 / $25.00",
	}
	usage := []ui.MoneyMetricVM{
		{Label: "Today", Text: "$0.42"},
		{Label: "Week", Text: "$3.81"},
		{Label: "Month", Text: "$12.47"},
		{Label: "Total", Text: "$41.08"},
	}
	account := []ui.MoneyMetricVM{
		{Label: "Balance", Text: "$73.42", Sensitive: true},
		{Label: "Purchased credits", Text: "$105.89", Sensitive: true},
		{Label: "Account usage", Text: "$32.47", Sensitive: true},
	}
	isHidden := func() bool { return true }

	detail := NewSpendDetailBody(ui.SpendVM{
		Usage: usage, Account: account, Limit: limit, HasData: true,
	}, isHidden)
	if sv := sensitiveValuesIn(t, detail); len(sv) != 3 {
		t.Errorf("detail masked values = %d, want the three account rows", len(sv))
	}
	for _, heading := range []string{"Usage", "Account"} {
		if !containsLabelText(t, detail, heading) {
			t.Errorf("detail body lacks the %q section heading", heading)
		}
	}
	if !containsLabelText(t, detail, "Total") || !containsLabelText(t, detail, limit.RawText) {
		t.Error("detail body must include the Total row and the raw limit amount line")
	}

	overview := NewSpendOverviewBody(ui.SpendOverviewVM{
		Usage:   usage[:3],
		Limit:   limit,
		Balance: &account[0],
		HasData: true,
	}, isHidden)
	if sv := sensitiveValuesIn(t, overview); len(sv) != 1 {
		t.Errorf("overview masked values = %d, want only Balance", len(sv))
	}
	if containsLabelText(t, overview, "Total") || containsLabelText(t, overview, "Account") {
		t.Error("an overview card has no Total row and no Account heading")
	}
	if !containsLabelText(t, overview, "Monthly limit") {
		t.Error("overview body must show the limit bar label")
	}

	if !containsLabelText(t, NewSpendDetailBody(ui.SpendVM{}, nil), noDataYet) {
		t.Error("no-data detail must show the placeholder")
	}
	if !containsLabelText(t, NewSpendOverviewBody(ui.SpendOverviewVM{}, nil), noDataYet) {
		t.Error("no-data overview must show the placeholder")
	}
}

const noDataYet = "No data yet"

func sensitiveValuesIn(t *testing.T, obj fyne.CanvasObject) []*SensitiveValue {
	t.Helper()
	var found []*SensitiveValue
	walkObjects(t, obj, func(o fyne.CanvasObject) {
		if sv, ok := o.(*SensitiveValue); ok {
			found = append(found, sv)
		}
	})

	return found
}

func containsLabelText(t *testing.T, obj fyne.CanvasObject, s string) bool {
	t.Helper()
	found := false
	walkObjects(t, obj, func(o fyne.CanvasObject) {
		switch txt := o.(type) {
		case *widget.Label:
			if txt.Text == s {
				found = true
			}
		case *canvas.Text:
			if txt.Text == s {
				found = true
			}
		}
	})

	return found
}

// walkObjects visits an object tree, descending through widget renderers
// (headless access via test.TempWidgetRenderer, as in editor_test.go).
func walkObjects(t *testing.T, obj fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	t.Helper()
	visit(obj)
	switch o := obj.(type) {
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			walkObjects(t, child, visit)
		}
	case *fyne.Container:
		for _, child := range o.Objects {
			walkObjects(t, child, visit)
		}
	}
}

// The OpenRouter detail card's pace row is a monetary value: masked under
// the privacy preference like every other money row, absent until a
// usable estimate exists.
func TestSpendDetailPaceRow(t *testing.T) {
	test.NewTempApp(t)
	isHidden := func() bool { return true }
	detail := NewSpendDetailBody(ui.SpendVM{
		HasData: true,
		Usage:   []ui.MoneyMetricVM{{Label: "Today", Text: "$0.42"}},
		Pace:    &ui.PaceVM{State: pace.StateActive, SpeedText: "$1.24/h"},
	}, isHidden)
	if !containsLabelText(t, detail, "Pace") {
		t.Error("detail body lacks the Pace row")
	}
	values := sensitiveValuesIn(t, detail)
	if len(values) != 1 {
		t.Fatalf("masked values = %d, want the pace row", len(values))
	}
	if !values[0].Masked() {
		t.Error("the pace value must be masked while the privacy flag is set")
	}

	// Privacy off: the real value shows.
	open := NewSpendDetailBody(ui.SpendVM{
		HasData: true,
		Usage:   []ui.MoneyMetricVM{{Label: "Today", Text: "$0.42"}},
		Pace:    &ui.PaceVM{State: pace.StateActive, SpeedText: "$1.24/h"},
	}, func() bool { return false })
	for _, v := range sensitiveValuesIn(t, open) {
		if v.DisplayText() != "$1.24/h" {
			t.Errorf("revealed pace value = %q", v.DisplayText())
		}
	}

	// No estimate yet: no pace row at all, not even a placeholder.
	collecting := NewSpendDetailBody(ui.SpendVM{
		HasData: true,
		Usage:   []ui.MoneyMetricVM{{Label: "Today", Text: "$0.42"}},
		Pace:    &ui.PaceVM{State: pace.StateCollecting},
	}, isHidden)
	if containsLabelText(t, collecting, "Pace") {
		t.Error("collecting pace must not render a money row")
	}
}
