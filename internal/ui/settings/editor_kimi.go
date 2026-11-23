// The Kimi Code editor field: a region select choosing one of the two fixed
// API hosts (global first), instead of a free-text URL.
package settings

import (
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
)

// kimiRegionChoice pairs the select's display label with the stored region.
type kimiRegionChoice struct {
	label string
	value config.KimiRegion
}

var kimiRegionChoices = []kimiRegionChoice{
	{label: "Global", value: config.KimiRegionGlobal},
	{label: "Mainland China", value: config.KimiRegionCN},
}

func kimiRegionLabel(value config.KimiRegion) string {
	for _, c := range kimiRegionChoices {
		if c.value == value {
			return c.label
		}
	}

	return kimiRegionChoices[0].label
}

func kimiRegionValue(label string) config.KimiRegion {
	for _, c := range kimiRegionChoices {
		if c.label == label {
			return c.value
		}
	}

	return config.KimiRegionGlobal
}

// buildKimiFields appends the region select.
func (e *editor) buildKimiFields(items *[]*widget.FormItem, s config.KimiSettings) {
	labels := make([]string, 0, len(kimiRegionChoices))
	for _, c := range kimiRegionChoices {
		labels = append(labels, c.label)
	}
	e.region = widget.NewSelect(labels, nil)
	e.region.SetSelected(kimiRegionLabel(s.EffectiveRegion()))
	*items = append(*items, widget.NewFormItem("Region", e.region))
}

// draftKimiSettings reads the region select.
func (e *editor) draftKimiSettings() config.KimiSettings {
	return config.KimiSettings{Region: kimiRegionValue(e.region.Selected)}
}
