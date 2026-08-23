// Package components holds the reusable Fyne building blocks. Everything
// here is standard Fyne widgets and containers except QuotaBar, which is
// the single approved custom widget (AGENTS.md: standard widgets only).
package components

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/skobkin/llm-monitor/internal/ui"
)

// quotaBarHeight is the rendered bar height in theme-independent pixels.
const quotaBarHeight = 8

// quotaBarMinWidth keeps the bar readable inside cards.
const quotaBarMinWidth = 120

// Quota threshold colors (DECISIONS D9). Fixed constants, not
// theme-derived, so the meaning is stable across light and dark themes.
var (
	colorCritical = color.RGBA{R: 0xF4, G: 0x43, B: 0x36, A: 0xFF} // red
	colorLow      = color.RGBA{R: 0xFF, G: 0x98, B: 0x00, A: 0xFF} // orange
	colorWarning  = color.RGBA{R: 0xFF, G: 0xC1, B: 0x07, A: 0xFF} // amber
	colorHealthy  = color.RGBA{R: 0x4C, G: 0xAF, B: 0x50, A: 0xFF} // green
)

// ThresholdColor maps a remaining fraction onto the threshold color:
// <=0.10 red, <=0.20 orange, <=0.50 amber, else green.
func ThresholdColor(remaining float64) color.Color {
	switch {
	case remaining <= 0.10:
		return colorCritical
	case remaining <= 0.20:
		return colorLow
	case remaining <= 0.50:
		return colorWarning
	default:
		return colorHealthy
	}
}

// QuotaBar is a thin horizontal progress bar showing used quota: the fill
// is the used fraction, the color still reflects how much quota is left
// (ThresholdColor of the remaining fraction). The numeric label always
// lives outside the bar (a separate label), so the bar itself never
// renders text. This is the only custom widget in S1.
type QuotaBar struct {
	widget.BaseWidget

	used      float64
	remaining float64
}

// NewQuotaBar creates a bar filled to the used fraction and colored by
// the remaining fraction.
func NewQuotaBar(used, remaining float64) *QuotaBar {
	b := &QuotaBar{}
	b.ExtendBaseWidget(b)
	b.used = clamp01(used)
	b.remaining = clamp01(remaining)
	return b
}

// SetFractions updates the fractions shown, clamped to [0, 1].
func (b *QuotaBar) SetFractions(used, remaining float64) {
	b.used = clamp01(used)
	b.remaining = clamp01(remaining)
	b.Refresh()
}

// Used reports the currently shown used fraction.
func (b *QuotaBar) Used() float64 { return b.used }

// Remaining reports the currently shown remaining fraction.
func (b *QuotaBar) Remaining() float64 { return b.remaining }

// CreateRenderer implements fyne.Widget.
func (b *QuotaBar) CreateRenderer() fyne.WidgetRenderer {
	track := canvas.NewRectangle(trackColor())
	fill := canvas.NewRectangle(ThresholdColor(b.remaining))
	return &quotaBarRenderer{bar: b, track: track, fill: fill}
}

// trackColor picks the neutral track color from the theme. Outside a
// running app (headless tests) the theme lookup panics, so a fixed
// neutral gray stands in.
func trackColor() color.Color {
	if fyne.CurrentApp() == nil {
		return color.RGBA{R: 0x88, G: 0x88, B: 0x88, A: 0x40}
	}
	return theme.Color(theme.ColorNameInputBackground)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

type quotaBarRenderer struct {
	bar   *QuotaBar
	track *canvas.Rectangle
	fill  *canvas.Rectangle
}

func (r *quotaBarRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.fill}
}

func (r *quotaBarRenderer) Destroy() {}

func (r *quotaBarRenderer) MinSize() fyne.Size {
	return fyne.NewSize(quotaBarMinWidth, quotaBarHeight)
}

// Layout sizes the track to the full area and the fill to the used
// fraction, both quotaBarHeight tall and vertically centered.
func (r *quotaBarRenderer) Layout(size fyne.Size) {
	y := (size.Height - quotaBarHeight) / 2
	r.track.Move(fyne.NewPos(0, y))
	r.track.Resize(fyne.NewSize(size.Width, quotaBarHeight))
	r.fill.Move(fyne.NewPos(0, y))
	r.fill.Resize(fyne.NewSize(size.Width*float32(r.bar.used), quotaBarHeight))
}

func (r *quotaBarRenderer) Refresh() {
	r.track.FillColor = trackColor()
	r.fill.FillColor = ThresholdColor(r.bar.remaining)
	r.track.Refresh()
	r.fill.Refresh()
	canvas.Refresh(r.bar)
}

// NewQuotaRow builds one compact quota window block: the window name and
// the "NN% used" label share the first line, the QuotaBar sits directly
// beneath, and the optional raw-count secondary line follows. The numeric
// label stays outside the bar.
func NewQuotaRow(vm ui.QuotaWindowVM) fyne.CanvasObject {
	row := container.NewVBox(
		container.NewHBox(
			widget.NewLabelWithStyle(vm.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			layout.NewSpacer(),
			widget.NewLabel(vm.UsedText),
		),
		NewQuotaBar(vm.Used, vm.Remaining),
	)
	if vm.Secondary != "" {
		row.Add(widget.NewLabel(vm.Secondary))
	}
	return row
}
