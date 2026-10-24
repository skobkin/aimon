package components

import (
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

// The overview body's aggregate refresher must age every countdown line of
// every window in one call, so the ticker replaces whole-body rebuilds.
func TestQuotaOverviewBodyTickAgesCountdowns(t *testing.T) {
	at := time.Unix(1770000000, 0)
	reset5h := at.Add(2 * time.Hour)
	resetWeek := at.Add(24 * time.Hour)
	body, tick := NewQuotaOverviewBody(ui.QuotaOverviewVM{
		HasData: true,
		Windows: []ui.OverviewDonutVM{
			{Label: "5h", PctText: "42%", Used: 0.42, Remaining: 0.58, ResetText: "2h0m", ResetAt: &reset5h},
			{Label: "week", PctText: "14%", Used: 0.14, Remaining: 0.86, ResetText: "1d0h", ResetAt: &resetWeek},
		},
	})
	if tick == nil {
		t.Fatal("body with countdowns returned a nil tick refresher")
	}

	tick(at.Add(3 * time.Hour))
	texts := donutTexts(t, body)
	// The 5h window's reset fell 1h in the past; the weekly one has 21h left.
	if !slices.Contains(texts, "now") {
		t.Errorf("5h countdown = %v, want %q after its reset passed", texts, "now")
	}
	if !slices.Contains(texts, "21h0m") {
		t.Errorf("week countdown = %v, want 21h0m", texts)
	}
}

// No reset instants: the body renders but hands back a nil refresher.
func TestQuotaOverviewBodyWithoutCountdownsHasNoTick(t *testing.T) {
	body, tick := NewQuotaOverviewBody(ui.QuotaOverviewVM{
		HasData: true,
		Windows: []ui.OverviewDonutVM{{Label: "5h", PctText: "42%", Used: 0.42, Remaining: 0.58}},
	})
	if tick != nil {
		t.Error("body without countdowns returned a non-nil tick refresher")
	}
	if body == nil {
		t.Error("body = nil")
	}
}

// donutTexts collects label texts from an overview body tree. Concrete
// cases must precede fyne.Widget: *widget.Label implements the Widget
// interface and would otherwise match first.
func donutTexts(t *testing.T, obj fyne.CanvasObject) []string {
	t.Helper()
	var out []string
	switch o := obj.(type) {
	case *widget.Label:
		out = append(out, o.Text)
	case fyne.Widget:
		for _, child := range test.TempWidgetRenderer(t, o).Objects() {
			out = append(out, donutTexts(t, child)...)
		}
	case *fyne.Container:
		for _, child := range o.Objects {
			out = append(out, donutTexts(t, child)...)
		}
	}

	return out
}

func TestDonutTextBlockCentersBothLinesInHole(t *testing.T) {
	// 80 DIP donut with CutoutRatio 0.5: the hole is the inner half,
	// spanning y from 20 to 60 in the center column.
	size := fyne.NewSize(donutSize, donutSize)
	pct := fyne.NewSize(30, 19)
	lbl := fyne.NewSize(16, 15)

	pctPos, lblPos := donutTextBlock(size, pct, lbl)

	// The 19+2+15 = 36-high block is centered: top = (80-36)/2 = 22.
	if pctPos.Y != 22 {
		t.Errorf("pct y = %v, want 22", pctPos.Y)
	}
	if lblPos.Y != 43 { // 22 + 19 + gap 2
		t.Errorf("lbl y = %v, want 43", lblPos.Y)
	}
	// Both lines horizontally centered.
	if pctPos.X != 25 { // (80-30)/2
		t.Errorf("pct x = %v, want 25", pctPos.X)
	}
	if lblPos.X != 32 { // (80-16)/2
		t.Errorf("lbl x = %v, want 32", lblPos.X)
	}
	// Neither line reaches the ring.
	if pctPos.Y < 20 {
		t.Errorf("pct y = %v, enters the ring (hole starts at 20)", pctPos.Y)
	}
	if lblPos.Y+15 > 60 {
		t.Errorf("lbl bottom = %v, enters the ring (hole ends at 60)", lblPos.Y+15)
	}
}

func TestDonutTextBlockStaysCenteredWhenTight(t *testing.T) {
	// An oversized block (26+2+18 = 46 > 40 DIP hole) overflows the hole
	// symmetrically rather than snapping onto the ring.
	size := fyne.NewSize(donutSize, donutSize)
	pct := fyne.NewSize(40, 26)
	lbl := fyne.NewSize(20, 18)

	pctPos, lblPos := donutTextBlock(size, pct, lbl)

	want := float32(donutSize-46) / 2
	if pctPos.Y != want {
		t.Errorf("pct y = %v, want %v (block centered, not halves)", pctPos.Y, want)
	}
	if lblPos.Y != want+26+2 {
		t.Errorf("lbl y = %v, want %v", lblPos.Y, want+26+2)
	}
}

// One compact projection line under a projectable donut: the centered
// caption, no speed text, exactly one bar for one projectable window. The
// expiry tick hides the caption and the bar together.
func TestQuotaOverviewBodyPaceLine(t *testing.T) {
	at := time.Unix(1770000000, 0)
	reset := at.Add(time.Hour)
	weekReset := at.Add(24 * time.Hour)
	body, tick := NewQuotaOverviewBody(ui.QuotaOverviewVM{
		HasData: true,
		Windows: []ui.OverviewDonutVM{
			{Label: "5h", PctText: "31%", Used: 0.31, Remaining: 0.69,
				ResetText: "1h0m", ResetAt: &reset,
				Pace: &ui.PaceVM{
					State: pace.StateActive, SpeedText: "3.4%/h",
					CanProject: true, ProjectedText: "≈51% at reset", Projected: 0.51,
					ResetAt: &reset,
				}},
			{Label: "week", PctText: "14%", Used: 0.14, Remaining: 0.86,
				ResetText: "1d0h", ResetAt: &weekReset},
		},
	})

	texts := donutTexts(t, body)
	if !slices.Contains(texts, "≈51% at reset") {
		t.Errorf("overview texts = %v, want the projection caption", texts)
	}
	if slices.Contains(texts, "3.4%/h") {
		t.Errorf("overview texts = %v, want no speed text in Overview", texts)
	}
	bars := projectionBars(t, body)
	if len(bars) != 1 {
		t.Fatalf("projection bars = %d, want 1 (only the projectable window)", len(bars))
	}

	// Expiry: the caption and the bar hide together; the countdown keeps
	// its column.
	tick(at.Add(time.Hour))
	for _, l := range rowLabels(t, body) {
		if l.Text == "≈51% at reset" && !l.Hidden {
			t.Error("projection caption still visible after expiry")
		}
	}
	if bars[0].Visible() {
		t.Error("projection bar still visible after expiry")
	}
	if !slices.Contains(donutTexts(t, body), "23h0m") {
		t.Error("the other column's countdown stopped ticking")
	}
}

// A body without any projectable window renders exactly as before S7.
func TestQuotaOverviewBodyWithoutPace(t *testing.T) {
	at := time.Unix(1770000000, 0)
	reset := at.Add(time.Hour)
	body, tick := NewQuotaOverviewBody(ui.QuotaOverviewVM{
		HasData: true,
		Windows: []ui.OverviewDonutVM{
			{Label: "5h", PctText: "31%", Used: 0.31, Remaining: 0.69, ResetText: "1h0m", ResetAt: &reset},
		},
	})
	if tick == nil {
		t.Fatal("countdown tick lost")
	}
	if bars := projectionBars(t, body); len(bars) != 0 {
		t.Errorf("projection bars = %d, want 0 without pace", len(bars))
	}
	for _, l := range rowLabels(t, body) {
		if strings.HasPrefix(l.Text, "≈") {
			t.Errorf("unexpected projection caption %q", l.Text)
		}
	}
}
