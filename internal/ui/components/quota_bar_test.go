package components

import (
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/pace"
	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// walkTree visits every canvas object of a widget tree in document
// order, descending into widget renderers and containers — the one
// shared skeleton for the collectors below. Text leaves (Label, RichText)
// stay opaque: a Label wraps its own RichText internally, and that
// renderer detail is not part of the logical tree.
func walkTree(t *testing.T, obj fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	t.Helper()
	visit(obj)
	switch o := obj.(type) {
	case *widget.Label, *widget.RichText:
		return
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			walkTree(t, child, visit)
		}
	case *fyne.Container:
		for _, child := range o.Objects {
			walkTree(t, child, visit)
		}
	}
}

// treeTexts collects every text-bearing leaf's text in document order —
// labels and rich texts alike — so ordering assertions can compare
// positions across both kinds.
func treeTexts(t *testing.T, obj fyne.CanvasObject) []string {
	t.Helper()
	var out []string
	walkTree(t, obj, func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *widget.Label:
			out = append(out, v.Text)
		case *widget.RichText:
			out = append(out, RichTextPlain(v))
		}
	})

	return out
}

// richTexts walks a tree collecting RichText widgets — the quota row
// titles and pace captions.
func richTexts(t *testing.T, obj fyne.CanvasObject) []*widget.RichText {
	t.Helper()
	var out []*widget.RichText
	walkTree(t, obj, func(o fyne.CanvasObject) {
		if rt, ok := o.(*widget.RichText); ok {
			out = append(out, rt)
		}
	})

	return out
}

// paceCaptionRichText returns the row's pace caption rich text, failing
// when missing.
func paceCaptionRichText(t *testing.T, row fyne.CanvasObject) *widget.RichText {
	t.Helper()
	for _, rt := range richTexts(t, row) {
		if strings.HasPrefix(RichTextPlain(rt), "Pace ") {
			return rt
		}
	}
	t.Fatal("pace caption rich text missing")

	return nil
}

// errorSegmentTexts returns the texts of a rich text's error-colored
// segments — the red-accented parts of a label.
func errorSegmentTexts(rt *widget.RichText) []string {
	var out []string
	for _, seg := range rt.Segments {
		if ts, ok := seg.(*widget.TextSegment); ok && ts.Style.ColorName == theme.ColorNameError {
			out = append(out, ts.Text)
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
	before := RichTextPlain(richTexts(t, row)[0])

	tick(time.Unix(1770000000, 0).Add(1*time.Hour + 5*time.Minute))
	after := RichTextPlain(richTexts(t, row)[0])

	if before != "Credits-5h · 2h0m" {
		t.Errorf("title at t0 = %q", before)
	}
	if after != "Credits-5h · 55m" {
		t.Errorf("title one hour later = %q, want the aged countdown", after)
	}

	// Re-deriving an unchanged second must be a no-op text write.
	tick(time.Unix(1770000000, 0).Add(1*time.Hour + 5*time.Minute))
	if again := RichTextPlain(richTexts(t, row)[0]); again != after {
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
	rts := richTexts(t, row)
	if len(rts) != 1 || RichTextPlain(rts[0]) != "Credits-5h" {
		t.Errorf("row rich texts = %d, want one title reading the bare window name", len(rts))
	}
}

// projectionBars walks a tree collecting ProjectionBar widgets.
func projectionBars(t *testing.T, obj fyne.CanvasObject) []*ProjectionBar {
	t.Helper()
	var out []*ProjectionBar
	walkTree(t, obj, func(o fyne.CanvasObject) {
		if b, ok := o.(*ProjectionBar); ok {
			out = append(out, b)
		}
	})

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

// A projectable row: the caption reads "Pace <speed> · <projection>", the
// bar sits beneath it, the raw-counts line stays last. A row without pace
// data renders none of that.
func TestNewQuotaRowPaceLine(t *testing.T) {
	at := time.Unix(1770000000, 0)
	row, tick := NewQuotaRow(paceRowVM(at))
	if tick == nil {
		t.Fatal("projectable row lost its countdown tick")
	}

	texts := treeTexts(t, row)
	if !slices.Contains(texts, "Pace 3.4%/h · ≈51% at reset") {
		t.Errorf("row texts = %v, want the pace caption", texts)
	}
	for _, rt := range richTexts(t, row) {
		if strings.HasPrefix(RichTextPlain(rt), "Pace ") && rt.Wrapping != fyne.TextWrapWord {
			t.Errorf("pace caption wrapping = %v, want word wrap", rt.Wrapping)
		}
	}
	if !slices.Contains(texts, "2051 / 12.0k credits") {
		t.Errorf("row texts = %v, want the secondary line", texts)
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
	for _, s := range treeTexts(t, row2) {
		if strings.HasPrefix(s, "Pace ") {
			t.Errorf("pace-less row text = %q", s)
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
	if !slices.Contains(treeTexts(t, row), "Pace 3.4%/h · ≈51% at reset") {
		t.Fatal("fresh projection caption missing before the reset")
	}
	if !bars[0].Visible() {
		t.Fatal("projection bar hidden before the reset")
	}

	tick(reset)
	texts := treeTexts(t, row)
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
	if !slices.Contains(treeTexts(t, row), "Pace 3.4%/h") || bars[0].Visible() {
		t.Error("expiry flip is not idempotent")
	}

	// The countdown keeps ticking next to the expired projection.
	tick(reset.Add(30 * time.Minute))
	if got := RichTextPlain(richTexts(t, row)[0]); got != "5-hour · now" {
		t.Error("countdown must read \"now\" beside the expired projection")
	}

	// A not-yet-expired state (a fresh sample rebuilds the row, the flip
	// covers the same transition) restores bar and caption.
	tick(at)
	if !bars[0].Visible() || !slices.Contains(treeTexts(t, row), "Pace 3.4%/h · ≈51% at reset") {
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
	if bars[0].Visible() || slices.Contains(treeTexts(t, row), "≈51% at reset") {
		t.Error("expired projection survived on a countdown-less row")
	}
	if !slices.Contains(treeTexts(t, row), "Pace 3.4%/h") {
		t.Error("the speed part of the caption must stay")
	}
}

// The burn-out estimate rides the row title as its own error-colored
// segment: "5-hour · ≈35m / 1h0m". It ages and clamps like the countdown
// itself and drops at the reset together with the projection.
func TestNewQuotaRowCountdownExhaust(t *testing.T) {
	at := time.Unix(1770000000, 0)
	reset := at.Add(time.Hour)
	exhaust := at.Add(35 * time.Minute)
	vm := paceRowVM(at)
	vm.ExhaustText = "≈35m"
	vm.Pace.ExhaustAt = &exhaust
	row, tick := NewQuotaRow(vm)

	title := richTexts(t, row)[0]
	if got := RichTextPlain(title); got != "5-hour · ≈35m / 1h0m" {
		t.Fatalf("title = %q, want the combined countdown", got)
	}
	if got := errorSegmentTexts(title); !slices.Equal(got, []string{"≈35m"}) {
		t.Errorf("error segments = %v, want the estimate alone", got)
	}
	if got := treeTexts(t, row); !slices.Contains(got, "Pace 3.4%/h · ≈51% at reset") {
		t.Errorf("caption untouched by the estimate = %v", got)
	}

	// The estimate clamps at zero and keeps aging with the countdown.
	tick(at.Add(50 * time.Minute))
	if got := RichTextPlain(richTexts(t, row)[0]); got != "5-hour · ≈0s / 10m" {
		t.Errorf("aged title = %q, want the clamped estimate", got)
	}

	// Idempotent: ticking again changes nothing.
	tick(at.Add(50 * time.Minute))
	if got := RichTextPlain(richTexts(t, row)[0]); got != "5-hour · ≈0s / 10m" {
		t.Errorf("second tick changed the title to %q", got)
	}

	// At the reset the estimate drops with the projection.
	tick(reset)
	if got := RichTextPlain(richTexts(t, row)[0]); got != "5-hour · now" {
		t.Errorf("title at reset = %q, want the plain \"now\" countdown", got)
	}
}

// The caption's projection segment turns error-colored exactly past 100%,
// mirroring the ProjectionBar's overflow segment.
func TestNewQuotaRowPaceCaptionColor(t *testing.T) {
	at := time.Unix(1770000000, 0)

	over := paceRowVM(at)
	over.Pace.Projected = 1.46
	over.Pace.ProjectedText = "≈146% at reset"
	row, _ := NewQuotaRow(over)
	if got := errorSegmentTexts(paceCaptionRichText(t, row)); !slices.Equal(got, []string{"≈146% at reset"}) {
		t.Errorf("over-100%% caption error segments = %v, want the projection alone", got)
	}

	safe := paceRowVM(at) // 51% projection
	row2, _ := NewQuotaRow(safe)
	if got := errorSegmentTexts(paceCaptionRichText(t, row2)); len(got) != 0 {
		t.Errorf("sub-100%% caption error segments = %v, want none", got)
	}
}
