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
	for _, l := range rowLabels(t, body) {
		if l.Text == "≈51% at reset" && l.Wrapping != fyne.TextWrapWord {
			// An unclamped projection can reach four digits; the caption
			// must wrap instead of widening the donut column.
			t.Errorf("projection caption wrapping = %v, want word wrap", l.Wrapping)
		}
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

// layoutOverview lays a quota overview body out the way a paint would:
// Container.Resize runs the layout chain synchronously, no window needed.
// test.NewTempApp must run first — Container.Refresh dereferences the app
// driver. Two passes: a wrapping caption recomputes its row bounds only
// once laid out at its real width, so the first pass grows its minimum and
// the second re-lays the grid at the grown minimum.
func layoutOverview(t *testing.T, body fyne.CanvasObject) {
	t.Helper()
	test.NewTempApp(t)
	body.Resize(body.MinSize())
	body.Refresh()
	body.Resize(body.MinSize())
	body.Refresh()
}

// overviewColumns returns each grid cell's VBox column in window order. A
// cell is the HBox (spacer, column, spacer) built by NewQuotaOverviewBody;
// the middle child is the column, whose first object is the donut. The
// structure guards keep these tests honest when the cell grammar changes.
func overviewColumns(t *testing.T, body fyne.CanvasObject) []*fyne.Container {
	t.Helper()
	grid, ok := body.(*fyne.Container)
	if !ok {
		t.Fatalf("overview body = %T, want the grid container", body)
	}
	var cols []*fyne.Container
	for i, cell := range grid.Objects {
		c, ok := cell.(*fyne.Container)
		if !ok || len(c.Objects) != 3 {
			t.Fatalf("overview cell %d = %T with %d objects, want an HBox cell (spacer, column, spacer)", i, cell, len(c.Objects))
		}
		col, ok := c.Objects[1].(*fyne.Container)
		if !ok {
			t.Fatalf("overview cell %d middle object = %T, want the column container", i, c.Objects[1])
		}
		if _, ok := col.Objects[0].(*QuotaDonut); !ok {
			t.Fatalf("overview column %d first object = %T, want the donut", i, col.Objects[0])
		}
		cols = append(cols, col)
	}

	return cols
}

// requireDonutsTopAligned asserts every column's ring starts at the cell's
// top edge: Fyne positions are parent-relative, so a level ring means the
// column at y=0 within its cell and the donut at y=0 within the column.
// Vertically centered columns (the old CenterLayout bug) fail the first
// check.
func requireDonutsTopAligned(t *testing.T, cols []*fyne.Container) {
	t.Helper()
	for i, col := range cols {
		if y := col.Position().Y; y != 0 {
			t.Errorf("column %d y = %v, want 0 (top-aligned)", i, y)
		}
		if y := col.Objects[0].Position().Y; y != 0 {
			t.Errorf("column %d donut y = %v, want 0 (top of column)", i, y)
		}
	}
}

// projectionCaption returns the column's "≈…" caption label, or nil.
func projectionCaption(col *fyne.Container) *widget.Label {
	for _, o := range col.Objects {
		if l, ok := o.(*widget.Label); ok && strings.HasPrefix(l.Text, "≈") {
			return l
		}
	}

	return nil
}

// A window without a reset time or projection — a just-reset 5h quota —
// has the shortest column of all while its neighbor keeps full height (the
// smoke-tested Z.AI scene). The rings must stay level: columns are
// top-aligned, not vertically centered (DECISION_LOG 2026-10-29).
func TestQuotaOverviewBodyTopAlignsRaggedColumns(t *testing.T) {
	at := time.Unix(1770000000, 0)
	weekReset := at.Add(4 * 24 * time.Hour)
	body, _ := NewQuotaOverviewBody(ui.QuotaOverviewVM{
		HasData: true,
		Windows: []ui.OverviewDonutVM{
			// The 5h window has just reset: no reset instant, no pace.
			{Label: "5h", PctText: "0%", Used: 0, Remaining: 1},
			{Label: "week", PctText: "45%", Used: 0.45, Remaining: 0.55,
				ResetText: "4d0h", ResetAt: &weekReset,
				Pace: &ui.PaceVM{
					State: pace.StateActive, SpeedText: "8.1%/h",
					CanProject: true, ProjectedText: "≈212% at reset", Projected: 2.12,
					ResetAt: &weekReset,
				}},
		},
	})

	layoutOverview(t, body)
	cols := overviewColumns(t, body)
	if len(cols) != 2 {
		t.Fatalf("columns = %d, want 2", len(cols))
	}
	if cols[0].MinSize().Height >= cols[1].MinSize().Height {
		t.Fatalf("ragged fixture collapsed: left column min height %v, right %v",
			cols[0].MinSize().Height, cols[1].MinSize().Height)
	}
	requireDonutsTopAligned(t, cols)
}

// A four-digit projection ("≈1873% at reset") wraps its caption onto two
// lines and grows its column; the neighbor's one-line column must keep the
// rings level (the second smoke-tested scene, a hard-burning week window).
func TestQuotaOverviewBodyTopAlignsWrappedProjectionCaption(t *testing.T) {
	at := time.Unix(1770000000, 0)
	reset5h := at.Add(1*time.Hour + 49*time.Minute)
	resetWeek := at.Add(6*24*time.Hour + 10*time.Hour)
	body, _ := NewQuotaOverviewBody(ui.QuotaOverviewVM{
		HasData: true,
		Windows: []ui.OverviewDonutVM{
			{Label: "5h", PctText: "78%", Used: 0.78, Remaining: 0.22,
				ResetText: "1h49m", ResetAt: &reset5h,
				Pace: &ui.PaceVM{
					State: pace.StateActive, SpeedText: "22.9%/h",
					CanProject: true, ProjectedText: "≈122% at reset", Projected: 1.22,
					ResetAt: &reset5h,
				}},
			{Label: "week", PctText: "28%", Used: 0.28, Remaining: 0.72,
				ResetText: "6d10h", ResetAt: &resetWeek,
				Pace: &ui.PaceVM{
					State: pace.StateActive, SpeedText: "18.7%/h",
					CanProject: true, ProjectedText: "≈1873% at reset", Projected: 18.73,
					ResetAt: &resetWeek,
				}},
		},
	})

	layoutOverview(t, body)
	cols := overviewColumns(t, body)
	if len(cols) != 2 {
		t.Fatalf("columns = %d, want 2", len(cols))
	}

	// Precondition: the long caption really wraps onto two lines at the
	// column's natural width — otherwise this fixture no longer exercises
	// the wrapped-caption raggedness.
	short, long := projectionCaption(cols[0]), projectionCaption(cols[1])
	if short == nil || long == nil {
		t.Fatalf("captions missing: %v / %v", short, long)
	}
	if long.MinSize().Height <= short.MinSize().Height {
		t.Fatalf("long caption min height %v not above short %v; the fixture stopped wrapping",
			long.MinSize().Height, short.MinSize().Height)
	}
	requireDonutsTopAligned(t, cols)
}

// The expiry tick hides one column's bar and caption without a rebuild;
// once the grid re-shrinks around the shorter column the rings must still
// sit level at the top — hiding lines must never re-float a ring.
func TestQuotaOverviewBodyKeepsDonutsLevelAcrossPaceExpiry(t *testing.T) {
	at := time.Unix(1770000000, 0)
	reset5h := at.Add(time.Hour)
	resetWeek := at.Add(24 * time.Hour)
	body, tick := NewQuotaOverviewBody(ui.QuotaOverviewVM{
		HasData: true,
		Windows: []ui.OverviewDonutVM{
			{Label: "5h", PctText: "31%", Used: 0.31, Remaining: 0.69,
				ResetText: "1h0m", ResetAt: &reset5h,
				Pace: &ui.PaceVM{
					State: pace.StateActive, SpeedText: "3.4%/h",
					CanProject: true, ProjectedText: "≈51% at reset", Projected: 0.51,
					ResetAt: &reset5h,
				}},
			{Label: "week", PctText: "14%", Used: 0.14, Remaining: 0.86,
				ResetText: "1d0h", ResetAt: &resetWeek,
				Pace: &ui.PaceVM{
					State: pace.StateActive, SpeedText: "1.2%/h",
					CanProject: true, ProjectedText: "≈43% at reset", Projected: 0.43,
					ResetAt: &resetWeek,
				}},
		},
	})

	layoutOverview(t, body)
	cols := overviewColumns(t, body)
	if len(cols) != 2 {
		t.Fatalf("columns = %d, want 2", len(cols))
	}
	requireDonutsTopAligned(t, cols)

	// The 5h projection expires; the tick hides its bar and caption.
	tick(at.Add(time.Hour))
	layoutOverview(t, body)
	requireDonutsTopAligned(t, cols)
}
