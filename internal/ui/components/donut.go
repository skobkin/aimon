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
	// donutSize is the widget size in DIP (compact for two side-by-side).
	donutSize = 80
)

// QuotaDonut is a ring gauge showing used quota as a colored arc over a
// neutral track ring. Center text shows the percentage and short label.
// Uses native Fyne canvas.Arc (Fyne 2.7+), no image rasterization.
type QuotaDonut struct {
	widget.BaseWidget

	pctText   string
	labelText string
	used      float64
	remaining float64
}

// NewQuotaDonut creates a donut gauge. Used drives the arc angle;
// remaining drives the threshold color.
func NewQuotaDonut(pctText, labelText string, used, remaining float64) *QuotaDonut {
	d := &QuotaDonut{
		pctText:   pctText,
		labelText: labelText,
		used:      clamp01(used),
		remaining: clamp01(remaining),
	}
	d.ExtendBaseWidget(d)
	return d
}

// SetData updates the donut fractions and text.
func (d *QuotaDonut) SetData(pctText, labelText string, used, remaining float64) {
	d.pctText = pctText
	d.labelText = labelText
	d.used = clamp01(used)
	d.remaining = clamp01(remaining)
	d.Refresh()
}

// CreateRenderer implements fyne.Widget.
func (d *QuotaDonut) CreateRenderer() fyne.WidgetRenderer {
	track := canvas.NewDoughnutArc(0, 360, trackColor())
	usedAngle := float32(d.used * 360)
	arc := canvas.NewDoughnutArc(0, usedAngle, ThresholdColor(d.remaining))

	pct := canvas.NewText(d.pctText, color.White)
	pct.TextStyle = fyne.TextStyle{Bold: true}
	pct.Alignment = fyne.TextAlignCenter
	if fyne.CurrentApp() != nil {
		pct.TextSize = theme.Size(theme.SizeNameText)
	}

	lbl := canvas.NewText(d.labelText, foregroundColor())
	lbl.Alignment = fyne.TextAlignCenter
	if fyne.CurrentApp() != nil {
		lbl.TextSize = theme.Size(theme.SizeNameCaptionText)
	}

	return &donutRenderer{
		donut: d,
		track: track,
		arc:   arc,
		pct:   pct,
		lbl:   lbl,
	}
}

type donutRenderer struct {
	donut *QuotaDonut
	track *canvas.Arc
	arc   *canvas.Arc
	pct   *canvas.Text
	lbl   *canvas.Text
}

func (r *donutRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.track, r.arc, r.pct, r.lbl}
}

func (r *donutRenderer) Destroy() {}

func (r *donutRenderer) MinSize() fyne.Size {
	return fyne.NewSize(donutSize, donutSize)
}

func (r *donutRenderer) Layout(size fyne.Size) {
	r.track.Move(fyne.NewPos(0, 0))
	r.track.Resize(size)
	r.arc.Move(fyne.NewPos(0, 0))
	r.arc.Resize(size)

	// Center percentage in upper half.
	pctSize := r.pct.MinSize()
	r.pct.Move(fyne.NewPos(
		(size.Width-pctSize.Width)/2,
		(size.Height/2-pctSize.Height)/2,
	))
	r.pct.Resize(pctSize)

	// Center label in lower half.
	lblSize := r.lbl.MinSize()
	r.lbl.Move(fyne.NewPos(
		(size.Width-lblSize.Width)/2,
		size.Height/2+(size.Height/2-lblSize.Height)/2,
	))
	r.lbl.Resize(lblSize)
}

func (r *donutRenderer) Refresh() {
	r.track.FillColor = trackColor()
	r.arc.FillColor = ThresholdColor(r.donut.remaining)
	r.arc.EndAngle = float32(r.donut.used * 360)

	r.pct.Text = r.donut.pctText
	r.lbl.Text = r.donut.labelText

	r.track.Refresh()
	r.arc.Refresh()
	r.pct.Refresh()
	r.lbl.Refresh()
	canvas.Refresh(r.donut)
}

// NewQuotaOverviewBody builds the compact quota overview body with up to
// two side-by-side donut gauges.
func NewQuotaOverviewBody(vm ui.QuotaOverviewVM) fyne.CanvasObject {
	if !vm.HasData {
		return widget.NewLabelWithStyle("No data yet", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	}

	columns := make([]fyne.CanvasObject, 0, len(vm.Windows))
	for _, w := range vm.Windows {
		donut := NewQuotaDonut(w.PctText, w.Label, w.Used, w.Remaining)
		reset := widget.NewLabel(w.ResetText)
		reset.Alignment = fyne.TextAlignCenter
		columns = append(columns, container.NewVBox(donut, reset))
	}
	return container.NewHBox(columns...)
}
