// Package components holds the reusable Fyne building blocks. Everything
// here is standard Fyne widgets and containers except QuotaBar, MetricBar
// and SensitiveValue — narrowly scoped, presentation-only custom widgets.
package components

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

const (
	// metricBarHeight is the rendered bar height in DIP.
	metricBarHeight = 20
	// metricBarMinWidth keeps the bar readable inside cards.
	metricBarMinWidth = 120
	// metricBarPillPadX is the horizontal padding inside the pill.
	metricBarPillPadX = 6
	// metricBarPillPadY is the vertical padding inside the pill.
	metricBarPillPadY = 2
	// metricBarPillInset is the minimum gap kept between the pill and the
	// bar's top and bottom when the padded text would otherwise touch them.
	metricBarPillInset = 1
)

// pillBg is a semi-transparent neutral background for the label pill,
// translucent enough to read as a tint over both the fill and the track.
var pillBg = color.RGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x66}

// pillText is white text inside the pill.
var pillText = color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}

// MetricBar is a compact labeled usage bar: a colored fill over a neutral
// track with a text pill centered on the bar. Fraction 0 renders an empty
// track with its text.
type MetricBar struct {
	widget.BaseWidget

	text     string
	fraction float64
	level    ui.MetricLevel
}

// NewMetricBar creates a labeled usage bar.
func NewMetricBar(text string, fraction float64, level ui.MetricLevel) *MetricBar {
	b := &MetricBar{text: text, fraction: clamp01(fraction), level: level}
	b.ExtendBaseWidget(b)
	return b
}

// SetData updates the bar contents.
func (b *MetricBar) SetData(text string, fraction float64, level ui.MetricLevel) {
	b.text = text
	b.fraction = clamp01(fraction)
	b.level = level
	b.Refresh()
}

// CreateRenderer implements fyne.Widget.
func (b *MetricBar) CreateRenderer() fyne.WidgetRenderer {
	track := canvas.NewRectangle(trackColor())
	fill := canvas.NewRectangle(LevelColor(b.level))
	pillBgRect := canvas.NewRectangle(pillBg)
	pillLabel := canvas.NewText(b.text, pillText)
	pillLabel.TextSize = b.pillTextSize()
	pillLabel.TextStyle = fyne.TextStyle{Bold: true}

	return &metricBarRenderer{
		bar:       b,
		track:     track,
		fill:      fill,
		pillBg:    pillBgRect,
		pillLabel: pillLabel,
	}
}

func (b *MetricBar) pillTextSize() float32 {
	if fyne.CurrentApp() == nil {
		return 11
	}
	return theme.Size(theme.SizeNameCaptionText)
}

type metricBarRenderer struct {
	bar       *MetricBar
	track     *canvas.Rectangle
	fill      *canvas.Rectangle
	pillBg    *canvas.Rectangle
	pillLabel *canvas.Text
}

func (r *metricBarRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.fill, r.pillBg, r.pillLabel}
}

func (r *metricBarRenderer) Destroy() {}

func (r *metricBarRenderer) MinSize() fyne.Size {
	return fyne.NewSize(metricBarMinWidth, metricBarHeight)
}

func (r *metricBarRenderer) Layout(size fyne.Size) {
	// Track and fill cover the complete bar bounds.
	r.track.Move(fyne.NewPos(0, 0))
	r.track.Resize(size)

	fillW := size.Width * float32(r.bar.fraction)
	r.fill.Move(fyne.NewPos(0, 0))
	r.fill.Resize(fyne.NewSize(fillW, size.Height))

	// Pill and text share one centered geometry.
	r.pillLabel.Text = r.bar.text
	textSize := r.pillLabel.MinSize()
	pillPos, pillSize, textPos := pillGeometry(size, textSize)
	r.pillBg.Move(pillPos)
	r.pillBg.Resize(pillSize)
	r.pillLabel.Move(textPos)
	r.pillLabel.Resize(textSize)
}

// pillGeometry centers the label pill and its text inside the complete bar
// bounds. The pill keeps at least metricBarPillInset to the bar's top and
// bottom even when the padded text would touch the edges, and its width is
// capped at the bar width; the text stays centered in the bar regardless, so
// a clamped pill may sit slightly inside its text.
func pillGeometry(bar, text fyne.Size) (pillPos fyne.Position, pillSize fyne.Size, textPos fyne.Position) {
	pillW := text.Width + 2*float32(metricBarPillPadX)
	if pillW > bar.Width {
		pillW = bar.Width
	}
	pillH := text.Height + 2*float32(metricBarPillPadY)
	if maxH := bar.Height - 2*float32(metricBarPillInset); pillH > maxH {
		pillH = maxH
	}
	if pillH < 0 {
		pillH = 0
	}

	pillPos = fyne.NewPos((bar.Width-pillW)/2, (bar.Height-pillH)/2)
	pillSize = fyne.NewSize(pillW, pillH)
	textPos = fyne.NewPos((bar.Width-text.Width)/2, (bar.Height-text.Height)/2)
	return pillPos, pillSize, textPos
}

func (r *metricBarRenderer) Refresh() {
	r.track.FillColor = trackColor()
	r.fill.FillColor = LevelColor(r.bar.level)
	r.pillLabel.Text = r.bar.text
	r.pillLabel.TextSize = r.bar.pillTextSize()
	r.track.Refresh()
	r.fill.Refresh()
	r.pillBg.Refresh()
	r.pillLabel.Refresh()
	canvas.Refresh(r.bar)
}

// NewMetricsOverviewBody builds the compact GPU overview body.
func NewMetricsOverviewBody(vm ui.MetricsOverviewVM) fyne.CanvasObject {
	if !vm.HasData {
		return widget.NewLabelWithStyle("No data yet", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	}

	objs := make([]fyne.CanvasObject, 0, 3)
	if vm.GPU.Text != "" {
		objs = append(objs, NewMetricBar(vm.GPU.Text, vm.GPU.Fraction, vm.GPU.Level))
	}
	if vm.VRAM.Text != "" {
		objs = append(objs, NewMetricBar(vm.VRAM.Text, vm.VRAM.Fraction, vm.VRAM.Level))
	}
	if vm.Temp.Value != "" {
		objs = append(objs, NewMetricRow(vm.Temp))
	}
	return container.NewVBox(objs...)
}
