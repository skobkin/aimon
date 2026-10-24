package components

import (
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

// projectionBarHeight is the rendered bar height in theme-independent
// pixels — one step below QuotaBar, the projection being the subordinate
// line of a quota row.
const projectionBarHeight = 6

// ProjectionBar renders an end-of-window quota projection (S7): the part
// up to the 100% point uses the remaining-fraction ThresholdColor, and an
// overflow beyond it renders red, separated by a 1px divider at the 100%
// point. The widget width is constant — the segments inside rescale by
// scale = min(max(projected, 1), 2), so the overflow never draws beyond
// the widget (values over 200% saturate the rendered segment). The true,
// uncapped projected value always lives in the caption text, never in
// the bar. Like QuotaBar it is a narrowly scoped, presentation-only
// custom widget (docs/DECISION_LOG.md).
type ProjectionBar struct {
	widget.BaseWidget

	projected float64 // projected fraction at reset; unclamped above 1
}

// NewProjectionBar creates a bar for the projected end-of-window usage.
// Negative values clamp to 0; values above 1 are kept and saturate the
// rendered overflow at the 2.0 scale cap.
func NewProjectionBar(projected float64) *ProjectionBar {
	b := &ProjectionBar{}
	b.ExtendBaseWidget(b)
	b.projected = clampFloor(projected)

	return b
}

// SetProjected updates the projected fraction, refresh-gated like the
// ticker's setText: sub-0.1% changes leave the rendering untouched so a
// stream of tiny updates never churns the canvas.
func (b *ProjectionBar) SetProjected(projected float64) {
	projected = clampFloor(projected)
	if math.Abs(projected-b.projected) < 0.001 {
		return
	}
	b.projected = projected
	b.Refresh()
}

// Projected reports the currently shown projected fraction (unclamped).
func (b *ProjectionBar) Projected() float64 { return b.projected }

// CreateRenderer implements fyne.Widget.
func (b *ProjectionBar) CreateRenderer() fyne.WidgetRenderer {
	return &projectionBarRenderer{
		bar:      b,
		track:    canvas.NewRectangle(trackColor()),
		base:     canvas.NewRectangle(colorCritical),
		overflow: canvas.NewRectangle(colorCritical),
		divider:  canvas.NewRectangle(foregroundColor()),
	}
}

// projectionSegments maps a projected fraction onto the rendered segment
// widths (fractions of the widget width). The scale keeps the 100% point
// inside the widget — at the 2.0 saturation cap it sits at half the
// width — so the overflow segment (base to min(projected, scale)) never
// draws beyond the widget. base + overflow ≤ 1 always.
func projectionSegments(projected float64) (base, overflow, scale float64) {
	if projected < 0 {
		projected = 0
	}
	scale = math.Min(math.Max(projected, 1), 2)
	base = math.Min(projected, 1) / scale
	if projected > 1 {
		overflow = (math.Min(projected, scale) - 1) / scale
	}

	return base, overflow, scale
}

// clampFloor clamps a projected fraction to [0, ∞).
func clampFloor(v float64) float64 {
	if v < 0 {
		return 0
	}

	return v
}

type projectionBarRenderer struct {
	bar      *ProjectionBar
	track    *canvas.Rectangle
	base     *canvas.Rectangle
	overflow *canvas.Rectangle
	divider  *canvas.Rectangle
}

func (r *projectionBarRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.base, r.overflow, r.divider}
}

func (r *projectionBarRenderer) Destroy() {}

func (r *projectionBarRenderer) MinSize() fyne.Size {
	return fyne.NewSize(quotaBarMinWidth, projectionBarHeight)
}

// Layout sizes the track to the full area and places the segments of the
// projection, all projectionBarHeight tall and vertically centered.
func (r *projectionBarRenderer) Layout(size fyne.Size) {
	y := (size.Height - projectionBarHeight) / 2
	base, overflow, scale := projectionSegments(r.bar.projected)

	r.track.Move(fyne.NewPos(0, y))
	r.track.Resize(fyne.NewSize(size.Width, projectionBarHeight))

	hundredPct := size.Width * float32(1/scale)
	r.base.Move(fyne.NewPos(0, y))
	r.base.Resize(fyne.NewSize(size.Width*float32(base), projectionBarHeight))
	r.divider.Move(fyne.NewPos(hundredPct, y))
	r.divider.Resize(fyne.NewSize(1, projectionBarHeight))
	r.overflow.Move(fyne.NewPos(hundredPct, y))
	r.overflow.Resize(fyne.NewSize(size.Width*float32(overflow), projectionBarHeight))
}

func (r *projectionBarRenderer) Refresh() {
	r.track.FillColor = trackColor()
	r.base.FillColor = ThresholdColor(math.Max(0, 1-r.bar.projected))
	r.overflow.FillColor = colorCritical
	r.divider.FillColor = foregroundColor()

	// The divider and the overflow only mean anything past 100%; at or
	// below it the 100% point is the widget's right edge.
	if r.bar.projected > 1 {
		r.overflow.Show()
		r.divider.Show()
	} else {
		r.overflow.Hide()
		r.divider.Hide()
	}

	r.track.Refresh()
	r.base.Refresh()
	r.overflow.Refresh()
	r.divider.Refresh()
	canvas.Refresh(r.bar)
}
