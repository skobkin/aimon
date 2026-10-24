// Package components holds the reusable Fyne building blocks. Everything
// here is standard Fyne widgets and containers except QuotaBar,
// MetricBar, ProjectionBar and SensitiveValue — narrowly scoped,
// presentation-only custom widgets (AGENTS.md: standard widgets only;
// SensitiveValue and ProjectionBar recorded in docs/DECISION_LOG.md).
package components

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
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

// LevelColor maps a metric level onto its display color. Neutral uses
// the theme foreground so unleveled rows render like plain text.
func LevelColor(level ui.MetricLevel) color.Color {
	switch level {
	case ui.MetricGood:
		return colorHealthy
	case ui.MetricWarning:
		return colorWarning
	case ui.MetricLow:
		return colorLow
	case ui.MetricCritical:
		return colorCritical
	default:
		return foregroundColor()
	}
}

// foregroundColor picks the theme's foreground color. Outside a running
// app (headless tests) the theme lookup panics, so a fixed near-black
// stands in.
func foregroundColor() color.Color {
	if fyne.CurrentApp() == nil {
		return color.RGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xFF}
	}

	return theme.Color(theme.ColorNameForeground)
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
//
// The second return re-renders the row's reset countdown for a new time
// (a no-op text write unless the rendered second/minute ticked over); it
// is nil when the row has no countdown. Rows are never rebuilt for time:
// the monitor's ticker calls this instead.
func NewQuotaRow(vm ui.QuotaWindowVM) (fyne.CanvasObject, func(now time.Time)) {
	title := widget.NewLabelWithStyle(quotaRowTitle(vm), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	row := container.NewVBox(
		container.NewHBox(
			title,
			layout.NewSpacer(),
			widget.NewLabel(vm.UsedText),
		),
		NewQuotaBar(vm.Used, vm.Remaining),
	)

	// The pace caption sits between the bar and the optional raw-counts
	// line, with the projection bar directly beneath it (S7_UI_UX).
	paceCaption, paceBar := addPaceLine(row, vm.Pace)

	if vm.Secondary != "" {
		// The raw "used / limit" line is provider free text: wrap it instead
		// of letting it widen the card.
		row.Add(NewWrappedLabel(vm.Secondary, fyne.TextAlignLeading, fyne.TextStyle{}))
	}

	var tick func(now time.Time)
	if vm.ResetAt != nil {
		reset := vm.ResetAt
		tick = func(now time.Time) {
			setText(title, quotaRowTitle(ui.QuotaWindowVM{
				Name:      vm.Name,
				ResetText: ui.FormatResetCountdown(reset, now),
			}))
			// The projection dies at the reset instant while the speed
			// stays meaningful: drop the projection part in place until a
			// fresh sample rebuilds the row with a new one.
			tickPaceExpiry(paceCaption, paceBar, vm.Pace, now)
		}
	}

	return row, tick
}

// addPaceLine appends the pace caption (and, when a fresh projection
// exists, the ProjectionBar) to a row box. It returns the caption and the
// bar for the row's expiry tick; both are nil without pace data.
func addPaceLine(row *fyne.Container, pace *ui.PaceVM) (*widget.Label, *ProjectionBar) {
	if pace == nil {
		return nil, nil
	}
	caption := widget.NewLabel(pace.Caption())
	row.Add(caption)

	var bar *ProjectionBar
	if pace.CanProject {
		bar = NewProjectionBar(pace.Projected)
		row.Add(bar)
	}

	return caption, bar
}

// tickPaceExpiry applies the projection-expiry flip of one pace line: once
// now has reached the reset, the bar and the projection half of the
// caption hide in place; the speed text stays. A no-op without pace data
// or a projection, and a no-op while the state is unchanged.
func tickPaceExpiry(caption *widget.Label, bar *ProjectionBar, pace *ui.PaceVM, now time.Time) {
	if pace == nil || !pace.CanProject {
		return
	}
	expired := pace.ProjectionExpired(now)
	want := pace.Caption()
	if expired {
		want = pace.SpeedCaption()
	}
	if caption.Text == want {
		// The caption already matches the wanted state.
		return
	}
	if expired {
		if bar != nil {
			bar.Hide()
		}
	} else if bar != nil {
		bar.Show()
	}
	setText(caption, want)
}

// quotaRowTitle renders the row's first line: the window name, with the
// countdown appended when one is known ("Credits-5h · 2h14m").
func quotaRowTitle(vm ui.QuotaWindowVM) string {
	if vm.ResetText == "" {
		return vm.Name
	}

	return vm.Name + " · " + vm.ResetText
}

// setText writes a label's text, skipping the refresh when unchanged so a
// per-second tick never repaints a line that reads the same.
func setText(l *widget.Label, s string) {
	if l.Text != s {
		l.SetText(s)
	}
}
