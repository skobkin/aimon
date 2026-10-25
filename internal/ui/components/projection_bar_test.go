package components

import (
	"math"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// Segment math: the scale keeps the 100% divider inside the widget and
// base + overflow never exceed the full width, even at the saturation
// cap and for negative inputs.
func TestProjectionSegments(t *testing.T) {
	cases := []struct {
		projected             float64
		base, overflow, scale float64
	}{
		{0, 0, 0, 1},
		{0.31, 0.31, 0, 1},
		{1, 1, 0, 1},
		{1.5, 1.0 / 1.5, 0.5 / 1.5, 1.5},
		{2, 0.5, 0.5, 2},
		{3, 0.5, 0.5, 2}, // saturated: the rendered segment caps, not the text
		{-1, 0, 0, 1},
	}
	for _, c := range cases {
		base, overflow, scale := projectionSegments(c.projected)
		if !near(base, c.base) || !near(overflow, c.overflow) || !near(scale, c.scale) {
			t.Errorf("projectionSegments(%v) = (%v, %v, %v), want (%v, %v, %v)",
				c.projected, base, overflow, scale, c.base, c.overflow, c.scale)
		}
		if base+overflow > 1 {
			t.Errorf("projectionSegments(%v): segments exceed the widget (%v + %v)",
				c.projected, base, overflow)
		}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func projectionRects(t *testing.T, b *ProjectionBar) (*projectionBarRenderer, bool) {
	t.Helper()
	r, ok := test.TempWidgetRenderer(t, b).(*projectionBarRenderer)

	return r, ok
}

// Renderer geometry at a fixed 300px width: the base fill ends at the
// 100% divider, the overflow starts there and never passes the widget's
// right edge, and nothing past 100% renders when there is no overflow.
func TestProjectionBarRendererLayout(t *testing.T) {
	test.NewTempApp(t)
	const width = float32(300)

	t.Run("under 100%", func(t *testing.T) {
		b := NewProjectionBar(0.31)
		r, ok := projectionRects(t, b)
		if !ok {
			t.Fatal("renderer type assertion failed")
		}
		r.Layout(fyne.NewSize(width, 6))
		r.Refresh()
		if w := r.base.Size().Width; w != width*0.31 {
			t.Errorf("base width = %v, want %v", w, width*0.31)
		}
		if r.overflow.Visible() || r.divider.Visible() {
			t.Error("no overflow may render at or below 100%")
		}
		if got := r.base.FillColor; got != ThresholdColor(0.69) {
			t.Errorf("base color = %v, want the remaining-fraction threshold color", got)
		}
	})

	t.Run("150%", func(t *testing.T) {
		b := NewProjectionBar(1.5)
		r, ok := projectionRects(t, b)
		if !ok {
			t.Fatal("renderer type assertion failed")
		}
		r.Layout(fyne.NewSize(width, 6))
		r.Refresh()

		// Scale 1.5: the 100% point sits at 2/3 of the width.
		// Scale 1.5: the 100% point sits at 2/3 of the width.
		if x := r.divider.Position().X; x != width*2/3 {
			t.Errorf("divider x = %v, want %v", x, width*2/3)
		}
		if w := r.base.Size().Width; w != width/1.5 {
			t.Errorf("base width = %v, want %v", w, width/1.5)
		}
		if w := r.overflow.Size().Width; w != width/3 {
			t.Errorf("overflow width = %v, want %v", w, width/3)
		}
		if end := r.overflow.Position().X + r.overflow.Size().Width; end > width {
			t.Errorf("overflow ends at %v, beyond the widget width %v", end, width)
		}
		if got := r.overflow.FillColor; got != colorCritical {
			t.Errorf("overflow color = %v, want red", got)
		}
		if got := r.base.FillColor; got != colorCritical {
			t.Errorf("base color past 100%% = %v, want red (no remaining quota)", got)
		}
	})

	t.Run("300% saturates at the 2.0 scale cap", func(t *testing.T) {
		b := NewProjectionBar(3)
		r, ok := projectionRects(t, b)
		if !ok {
			t.Fatal("renderer type assertion failed")
		}
		r.Layout(fyne.NewSize(width, 6))
		r.Refresh()
		if x := r.divider.Position().X; x != width/2 {
			t.Errorf("divider x = %v, want %v", x, width/2)
		}
		if end := r.overflow.Position().X + r.overflow.Size().Width; end != width {
			t.Errorf("saturated overflow ends at %v, want exactly %v", end, width)
		}
	})
}

func nearF(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-6 }

// The very first paint must be correct without a Refresh: Fyne's renderer
// cache never refreshes a fresh renderer, so CreateRenderer already carries
// the final colors and visibility.
func TestProjectionBarRendererInitialPaint(t *testing.T) {
	test.NewTempApp(t)

	t.Run("past 100%", func(t *testing.T) {
		r, ok := projectionRects(t, NewProjectionBar(1.37))
		if !ok {
			t.Fatal("renderer type assertion failed")
		}
		if got := r.base.FillColor; got != colorCritical {
			t.Errorf("base color = %v, want red (no remaining quota)", got)
		}
		if !r.overflow.Visible() || !r.divider.Visible() {
			t.Error("overflow and divider must start visible above 100%")
		}
	})

	t.Run("under 100%", func(t *testing.T) {
		r, ok := projectionRects(t, NewProjectionBar(0.31))
		if !ok {
			t.Fatal("renderer type assertion failed")
		}
		if got := r.base.FillColor; got != ThresholdColor(0.69) {
			t.Errorf("base color = %v, want the remaining-fraction threshold color", got)
		}
		if r.overflow.Visible() || r.divider.Visible() {
			t.Error("overflow and divider must start hidden at or below 100%")
		}
	})
}

func TestProjectionBarNegativeClamp(t *testing.T) {
	if got := NewProjectionBar(-3).Projected(); got != 0 {
		t.Errorf("negative projection = %v, want 0", got)
	}
}

// Sub-0.1% updates leave the bar untouched — the same change-gating the
// ticker applies to labels.
func TestProjectionBarRefreshGate(t *testing.T) {
	b := NewProjectionBar(0.5)
	b.SetProjected(0.5005)
	if got := b.Projected(); got != 0.5 {
		t.Errorf("sub-threshold update stored = %v, want 0.5", got)
	}
	b.SetProjected(0.501)
	if got := b.Projected(); got != 0.501 {
		t.Errorf("threshold update = %v, want 0.501", got)
	}
	b.SetProjected(-2)
	if got := b.Projected(); got != 0 {
		t.Errorf("negative update = %v, want 0", got)
	}
}
