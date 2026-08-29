package components

import (
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// rowLabels walks the tree of a quota row and returns its labels in order.
// Concrete cases must precede fyne.Widget: *widget.Label implements the
// Widget interface and would otherwise match first.
func rowLabels(t *testing.T, obj fyne.CanvasObject) []*widget.Label {
	t.Helper()
	var out []*widget.Label
	switch o := obj.(type) {
	case *widget.Label:
		out = append(out, o)
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			out = append(out, rowLabels(t, child)...)
		}
	case *fyne.Container:
		for _, child := range o.Objects {
			out = append(out, rowLabels(t, child)...)
		}
	}

	return out
}

func TestThresholdColorBoundaries(t *testing.T) {
	cases := []struct {
		remaining float64
		want      color.Color
	}{
		{0.00, colorCritical},
		{0.10, colorCritical},
		{0.11, colorLow},
		{0.20, colorLow},
		{0.21, colorWarning},
		{0.50, colorWarning},
		{0.51, colorHealthy},
		{1.00, colorHealthy},
	}
	for _, c := range cases {
		if got := ThresholdColor(c.remaining); got != c.want {
			t.Errorf("ThresholdColor(%v) = %v, want %v", c.remaining, got, c.want)
		}
	}
}

func TestThresholdColorExactValues(t *testing.T) {
	// The exact palette from DECISIONS D9.
	if got := ThresholdColor(0.05); got != (color.RGBA{R: 0xF4, G: 0x43, B: 0x36, A: 0xFF}) {
		t.Errorf("critical color = %v", got)
	}
	if got := ThresholdColor(0.15); got != (color.RGBA{R: 0xFF, G: 0x98, B: 0x00, A: 0xFF}) {
		t.Errorf("low color = %v", got)
	}
	if got := ThresholdColor(0.30); got != (color.RGBA{R: 0xFF, G: 0xC1, B: 0x07, A: 0xFF}) {
		t.Errorf("warning color = %v", got)
	}
	if got := ThresholdColor(0.80); got != (color.RGBA{R: 0x4C, G: 0xAF, B: 0x50, A: 0xFF}) {
		t.Errorf("healthy color = %v", got)
	}
}

func TestLevelColor(t *testing.T) {
	cases := []struct {
		level ui.MetricLevel
		want  color.Color
	}{
		{ui.MetricGood, colorHealthy},
		{ui.MetricWarning, colorWarning},
		{ui.MetricLow, colorLow},
		{ui.MetricCritical, colorCritical},
	}
	for _, c := range cases {
		if got := LevelColor(c.level); got != c.want {
			t.Errorf("LevelColor(%v) = %v, want %v", c.level, got, c.want)
		}
	}
	// Neutral falls back to the theme foreground; headless (no app) it
	// is the fixed near-black stand-in.
	if got := LevelColor(ui.MetricNeutral); got == nil {
		t.Error("LevelColor(neutral) = nil")
	}
}

func TestNewQuotaBarClamps(t *testing.T) {
	b := NewQuotaBar(1.5, -3)
	if got := b.Used(); got != 1 {
		t.Errorf("used over range = %v, want 1", got)
	}
	if got := b.Remaining(); got != 0 {
		t.Errorf("remaining under range = %v, want 0", got)
	}
	if got := NewQuotaBar(0.42, 0.58).Used(); got != 0.42 {
		t.Errorf("used in range = %v, want 0.42", got)
	}
}

func TestSetFractionsClamps(t *testing.T) {
	b := NewQuotaBar(0.5, 0.5)
	b.SetFractions(2, 2)
	if got := b.Used(); got != 1 {
		t.Errorf("set used over = %v, want 1", got)
	}
	if got := b.Remaining(); got != 1 {
		t.Errorf("set remaining over = %v, want 1", got)
	}
	b.SetFractions(-1, -1)
	if got := b.Used(); got != 0 {
		t.Errorf("set used under = %v, want 0", got)
	}
	if got := b.Remaining(); got != 0 {
		t.Errorf("set remaining under = %v, want 0", got)
	}
}

// A row with a reset instant must re-derive its title (name · countdown)
// from the new time, so the monitor's ticker can age it in place without
// rebuilding the row.
func TestNewQuotaRowTickAdvancesCountdown(t *testing.T) {
	reset := time.Unix(1770000000, 0).Add(2 * time.Hour)
	row, tick := NewQuotaRow(ui.QuotaWindowVM{
		Name:      "Credits-5h",
		UsedText:  "17% used",
		Used:      0.17,
		Remaining: 0.83,
		ResetText: "2h0m",
		ResetAt:   &reset,
	})
	if tick == nil {
		t.Fatal("row with ResetAt returned a nil tick refresher")
	}

	tick(time.Unix(1770000000, 0))
	before := rowLabels(t, row)[0].Text

	tick(time.Unix(1770000000, 0).Add(1*time.Hour + 5*time.Minute))
	after := rowLabels(t, row)[0].Text

	if before != "Credits-5h · 2h0m" {
		t.Errorf("title at t0 = %q", before)
	}
	if after != "Credits-5h · 55m" {
		t.Errorf("title one hour later = %q, want the aged countdown", after)
	}

	// Re-deriving an unchanged second must be a no-op text write.
	tick(time.Unix(1770000000, 0).Add(1*time.Hour + 5*time.Minute))
	if again := rowLabels(t, row)[0].Text; again != after {
		t.Errorf("second tick at the same instant changed the title to %q", again)
	}
}

// A row without a reset instant shows no countdown and must hand the
// monitor a nil refresher.
func TestNewQuotaRowWithoutResetHasNoTick(t *testing.T) {
	row, tick := NewQuotaRow(ui.QuotaWindowVM{Name: "Credits-5h", UsedText: "17% used"})
	if tick != nil {
		t.Error("row without ResetAt returned a non-nil tick refresher")
	}
	labels := rowLabels(t, row)
	if len(labels) == 0 || labels[0].Text != "Credits-5h" {
		t.Errorf("title = %q, want the bare window name", labels[0].Text)
	}
}
