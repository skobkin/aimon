package components

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

// NewRuntimeOverviewBody builds the compact llama-swap overview body.
func NewRuntimeOverviewBody(vm ui.RuntimeOverviewVM) fyne.CanvasObject {
	if !vm.HasData {
		return widget.NewLabelWithStyle("No data yet", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	}

	objs := []fyne.CanvasObject{
		widget.NewLabelWithStyle(vm.Summary, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	}
	for _, name := range vm.Names {
		// A running model's name is provider free text ("hf.co/org/repo:Q4")
		// and can be long: wrap it instead of widening the overview column.
		objs = append(objs, NewWrappedLabel(name, fyne.TextAlignLeading, fyne.TextStyle{}))
	}
	if vm.MoreCount > 0 {
		objs = append(objs, widget.NewLabelWithStyle(
			"+"+ui.FormatCount(float64(vm.MoreCount))+" more",
			fyne.TextAlignLeading, fyne.TextStyle{Italic: true}))
	}

	return container.NewVBox(objs...)
}
