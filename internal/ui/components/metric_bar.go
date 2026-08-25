// Package components holds the reusable Fyne building blocks. Everything
// here is standard Fyne widgets and containers except QuotaBar and MetricBar,
// the two approved custom widgets (AGENTS.md: standard widgets only).
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
)

// pillBg is a semi-transparent dark background for the label pill.
var pillBg = color.RGBA{R: 0x00, G: 0x00, B: 0x00, A: 0x99}

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
	// Track fills the full bar area.
	r.track.Move(fyne.NewPos(0, 0))
	r.track.Resize(fyne.NewSize(size.Width, metricBarHeight))

	// Fill covers the used fraction.
	fillW := size.Width * float32(r.bar.fraction)
	r.fill.Move(fyne.NewPos(0, 0))
	r.fill.Resize(fyne.NewSize(fillW, metricBarHeight))

	// Pill: measure text, add padding, center in bar.
	r.pillLabel.Text = r.bar.text
	textSize := r.pillLabel.MinSize()
	pillW := textSize.Width + 2*float32(metricBarPillPadX)
	pillH := textSize.Height + 2*float32(metricBarPillPadY)

	// Cap pill to bar width.
	if pillW > size.Width {
		pillW = size.Width
	}

	pillX := (size.Width - pillW) / 2
	pillY := (metricBarHeight - pillH) / 2
	r.pillBg.Move(fyne.NewPos(pillX, pillY))
	r.pillBg.Resize(fyne.NewSize(pillW, pillH))

	// Text centered inside the pill.
	textX := pillX + (pillW-textSize.Width)/2
	textY := pillY + (pillH-textSize.Height)/2
	r.pillLabel.Move(fyne.NewPos(textX, textY))
	r.pillLabel.Resize(textSize)
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
