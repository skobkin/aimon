package components

import (
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/pace"
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
	// The exact palette from legacy decision D9 (docs/DECISION_LOG.md).
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

// projectionBars walks a tree collecting ProjectionBar widgets. Concrete
// cases must precede fyne.Widget: *ProjectionBar implements the Widget
// interface and would otherwise match first.
func projectionBars(t *testing.T, obj fyne.CanvasObject) []*ProjectionBar {
	t.Helper()
	var out []*ProjectionBar
	switch o := obj.(type) {
	case *ProjectionBar:
		out = append(out, o)
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			out = append(out, projectionBars(t, child)...)
		}
	case *fyne.Container:
		for _, child := range o.Objects {
			out = append(out, projectionBars(t, child)...)
		}
	}

	return out
}

// paceRowVM builds a detail quota row with a fresh, projectable pace
// estimate one hour from the given instant.
func paceRowVM(at time.Time) ui.QuotaWindowVM {
	reset := at.Add(time.Hour)

	return ui.QuotaWindowVM{
		Name: "5-hour", UsedText: "31% used", Used: 0.31, Remaining: 0.69,
		ResetText: "1h0m", ResetAt: &reset, Secondary: "2051 / 12.0k credits",
		Pace: &ui.PaceVM{
			State: pace.StateActive, SpeedText: "3.4%/h",
			CanProject: true, ProjectedText: "≈51% at reset", Projected: 0.51,
			ResetAt: &reset,
		},
	}
}

func labelTexts(labels []*widget.Label) []string {
	out := make([]string, len(labels))
	for i, l := range labels {
		out[i] = l.Text
	}

	return out
}

// A projectable row: the caption reads "Pace <speed> · <projection>", the
// bar sits beneath it, the raw-counts line stays last. A row without pace
// data renders none of that.
func TestNewQuotaRowPaceLine(t *testing.T) {
	at := time.Unix(1770000000, 0)
	row, tick := NewQuotaRow(paceRowVM(at))
	if tick == nil {
		t.Fatal("projectable row lost its countdown tick")
	}

	texts := labelTexts(rowLabels(t, row))
	if !slices.Contains(texts, "Pace 3.4%/h · ≈51% at reset") {
		t.Errorf("row labels = %v, want the pace caption", texts)
	}
	for _, l := range rowLabels(t, row) {
		if strings.HasPrefix(l.Text, "Pace ") && l.Wrapping != fyne.TextWrapWord {
			t.Errorf("pace caption wrapping = %v, want word wrap", l.Wrapping)
		}
	}
	if !slices.Contains(texts, "2051 / 12.0k credits") {
		t.Errorf("row labels = %v, want the secondary line", texts)
	}
	if i := slices.Index(texts, "Pace 3.4%/h · ≈51% at reset"); i > slices.Index(texts, "2051 / 12.0k credits") {
		t.Errorf("caption must sit before the secondary line: %v", texts)
	}
	if bars := projectionBars(t, row); len(bars) != 1 {
		t.Fatalf("projection bars = %d, want 1", len(bars))
	}

	// Without pace data nothing pace-related renders.
	plain := paceRowVM(at)
	plain.Pace = nil
	row2, _ := NewQuotaRow(plain)
	texts = labelTexts(rowLabels(t, row2))
	for _, s := range texts {
		if strings.HasPrefix(s, "Pace ") {
			t.Errorf("pace-less row label = %q", s)
		}
	}
	if bars := projectionBars(t, row2); len(bars) != 0 {
		t.Errorf("pace-less row bars = %d, want 0", len(bars))
	}
}

// The projection dies exactly at the reset instant, in place: the bar
// hides and the caption keeps only the speed, while the row's countdown
// keeps ticking. The flip is idempotent, and a not-yet-expired state
// restores the projection.
func TestNewQuotaRowPaceExpiryTick(t *testing.T) {
	at := time.Unix(1770000000, 0)
	reset := at.Add(time.Hour)
	row, tick := NewQuotaRow(paceRowVM(at))
	bars := projectionBars(t, row)

	tick(at)
	if !slices.Contains(labelTexts(rowLabels(t, row)), "Pace 3.4%/h · ≈51% at reset") {
		t.Fatal("fresh projection caption missing before the reset")
	}
	if !bars[0].Visible() {
		t.Fatal("projection bar hidden before the reset")
	}

	tick(reset)
	texts := labelTexts(rowLabels(t, row))
	if slices.Contains(texts, "≈51% at reset") || slices.Contains(texts, "Pace 3.4%/h · ≈51% at reset") {
		t.Errorf("caption after expiry = %v, want the projection part gone", texts)
	}
	if !slices.Contains(texts, "Pace 3.4%/h") {
		t.Errorf("caption after expiry = %v, want the speed kept", texts)
	}
	if bars[0].Visible() {
		t.Error("projection bar must hide at the reset instant")
	}

	// Idempotent: ticking again changes nothing.
	tick(reset.Add(time.Second))
	if !slices.Contains(labelTexts(rowLabels(t, row)), "Pace 3.4%/h") || bars[0].Visible() {
		t.Error("expiry flip is not idempotent")
	}

	// The countdown keeps ticking next to the expired projection.
	tick(reset.Add(30 * time.Minute))
	if !slices.Contains(labelTexts(rowLabels(t, row)), "5-hour · now") {
		t.Error("countdown must read \"now\" beside the expired projection")
	}

	// A not-yet-expired state (a fresh sample rebuilds the row, the flip
	// covers the same transition) restores bar and caption.
	tick(at)
	if !bars[0].Visible() || !slices.Contains(labelTexts(rowLabels(t, row)), "Pace 3.4%/h · ≈51% at reset") {
		t.Error("restore flip failed")
	}
}

// A projectable pace wires the expiry tick even when the row's quota
// snapshot carried no reset time: the monitor reads states before paces,
// so a sample landing between the two reads can pair a reset-less
// snapshot with a newer projectable estimate — and that projection must
// still retire at its reset instant instead of living until the next
// rebuild.
func TestNewQuotaRowTickForPaceWithoutReset(t *testing.T) {
	at := time.Unix(1770000000, 0)
	vm := paceRowVM(at)
	vm.ResetAt = nil
	vm.ResetText = ""
	row, tick := NewQuotaRow(vm)
	if tick == nil {
		t.Fatal("projectable pace lost its expiry tick")
	}
	bars := projectionBars(t, row)
	if len(bars) != 1 {
		t.Fatalf("projection bars = %d, want 1", len(bars))
	}

	tick(at.Add(2 * time.Hour))
	if bars[0].Visible() || slices.Contains(labelTexts(rowLabels(t, row)), "≈51% at reset") {
		t.Error("expired projection survived on a countdown-less row")
	}
	if !slices.Contains(labelTexts(rowLabels(t, row)), "Pace 3.4%/h") {
		t.Error("the speed part of the caption must stay")
	}
}
