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
		objs = append(objs, widget.NewLabel(name))
	}
	if vm.MoreCount > 0 {
		objs = append(objs, widget.NewLabelWithStyle(
			"+"+ui.FormatCount(float64(vm.MoreCount))+" more",
			fyne.TextAlignLeading, fyne.TextStyle{Italic: true}))
	}
	return container.NewVBox(objs...)
}
