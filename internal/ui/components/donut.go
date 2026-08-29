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

const (
	// donutSize is the widget size in DIP (compact for two side-by-side).
	donutSize = 80
	// donutTextGap is the deliberate gap between the percentage and the
	// short label inside the hole.
	donutTextGap = 2
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

	// Both center lines form one block centered in the hole.
	pctSize := r.pct.MinSize()
	lblSize := r.lbl.MinSize()
	pctPos, lblPos := donutTextBlock(size, pctSize, lblSize)
	r.pct.Move(pctPos)
	r.pct.Resize(pctSize)
	r.lbl.Move(lblPos)
	r.lbl.Resize(lblSize)
}

// donutTextBlock positions the percentage and the short label as one block,
// vertically centered in the donut and horizontally centered line by line.
// Centering the block (not each line in its own half) keeps both lines
// clustered inside the hole — the doughnut's CutoutRatio 0.5 leaves the
// inner half of the widget clear — instead of spread onto the ring.
func donutTextBlock(size, pctSize, lblSize fyne.Size) (pctPos, lblPos fyne.Position) {
	block := pctSize.Height + float32(donutTextGap) + lblSize.Height
	top := (size.Height - block) / 2
	pctPos = fyne.NewPos((size.Width-pctSize.Width)/2, top)
	lblPos = fyne.NewPos((size.Width-lblSize.Width)/2, top+pctSize.Height+float32(donutTextGap))

	return pctPos, lblPos
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
// two quota donut gauges. The available width is split into equal columns
// (one per window, full width for a single one); each donut is centered in
// its column at its natural size — a stretched cell would squash the ring
// into an ellipse — with its reset countdown centered directly beneath it.
// Windows without a reliable reset time show no countdown line at all.
//
// The second return re-renders the countdown lines for a new time; it is
// nil when no window shows a countdown. Bodies are never rebuilt for time:
// the monitor's ticker calls this instead.
func NewQuotaOverviewBody(vm ui.QuotaOverviewVM) (fyne.CanvasObject, func(now time.Time)) {
	if !vm.HasData {
		return widget.NewLabelWithStyle("No data yet", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}), nil
	}

	cells := make([]fyne.CanvasObject, 0, len(vm.Windows))
	var ticks []func(now time.Time)
	for _, w := range vm.Windows {
		col := container.NewVBox(NewQuotaDonut(w.PctText, w.Label, w.Used, w.Remaining))
		if w.ResetAt != nil {
			reset := widget.NewLabel(w.ResetText)
			reset.Alignment = fyne.TextAlignCenter
			col.Add(reset)
			at := w.ResetAt
			ticks = append(ticks, func(now time.Time) {
				setText(reset, ui.FormatResetCountdown(at, now))
			})
		}
		cells = append(cells, container.New(layout.NewCenterLayout(), col))
	}
	body := container.NewGridWithColumns(len(cells), cells...)
	if len(ticks) == 0 {
		return body, nil
	}

	return body, func(now time.Time) {
		for _, tick := range ticks {
			tick(now)
		}
	}
}
