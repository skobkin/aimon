package components

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// NewServiceDetailBody builds a Hindsight card's sectioned body. The view
// model emits only the sections and rows that were actually observed, so
// the body simply renders what it is given. The memory ages tick in place:
// the returned refreshers re-derive their text from each row's absolute
// instant once a second, without rebuilding the tree. now anchors the
// initial age text.
func NewServiceDetailBody(vm ui.ServiceVM, now time.Time) (fyne.CanvasObject, []func(now time.Time)) {
	if !vm.HasData {
		return noDataLabel(), nil
	}

	body := container.NewVBox()
	var ticks []func(now time.Time)

	section := func(title string, rows []ui.MetricRowVM) {
		if len(rows) == 0 {
			return
		}
		body.Add(NewSectionLabel(title))
		for _, row := range rows {
			body.Add(NewMetricRow(row))
		}
	}
	section("Service", vm.Service)
	section("Activity", vm.Activity)
	section("Queue", vm.Queue)
	section("Runtime", vm.Runtime)

	if len(vm.Memory) > 0 {
		body.Add(NewSectionLabel("Memory"))
		for _, row := range vm.Memory {
			obj, tick := newAgeRow(row)
			body.Add(obj)
			if tick != nil {
				ticks = append(ticks, tick)
			}
		}
	}

	return body, ticks
}

// newAgeRow builds one "Label … 3m ago" row. The tick re-derives the text
// from the row's absolute instant and repaints the same canvas.Text —
// object identity is preserved, and the refresh is skipped while the text
// is unchanged (the setLabelStatus discipline). A row without an instant
// shows the unavailable dash and never ticks.
func newAgeRow(vm ui.AgeRowVM) (fyne.CanvasObject, func(now time.Time)) {
	value := canvas.NewText(vm.Value, LevelColor(vm.Level))
	value.TextSize = bodyTextSize()
	obj := container.NewHBox(
		widget.NewLabel(vm.Label),
		layout.NewSpacer(),
		value,
	)
	if vm.At == nil {
		return obj, nil
	}

	return obj, func(now time.Time) {
		if s := ui.FormatAge(*vm.At, now); s != value.Text {
			value.Text = s
			value.Refresh()
		}
	}
}

// NewServiceOverviewBody builds the compact Hindsight overview body: a
// bold summary line ("Healthy · v0.9.2") and the headline pairs laid out
// two per row, mirroring the S5 wireframe.
func NewServiceOverviewBody(vm ui.ServiceOverviewVM) fyne.CanvasObject {
	if !vm.HasData {
		return noDataLabel()
	}

	body := container.NewVBox(
		widget.NewLabelWithStyle(vm.Summary, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	)
	for i := 0; i < len(vm.Rows); i += 2 {
		if i+1 < len(vm.Rows) {
			body.Add(container.NewGridWithColumns(2, NewMetricRow(vm.Rows[i]), NewMetricRow(vm.Rows[i+1])))

			continue
		}
		body.Add(NewMetricRow(vm.Rows[i]))
	}

	return body
}
