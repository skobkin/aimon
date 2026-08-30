// List operations on the configured source instances. Everything here is
// pure: functions derive a new candidate list from the current one and
// never touch widgets, so ordering, enable and delete semantics are
// unit-testable without a display.
package settings

import (
	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
)

// newDraft builds the starting point for the Add editor: a fresh instance
// ID generated here, when the editor opens — Test Connection and Save use
// this same ID, and Cancel simply discards the unused one (the ID is never
// regenerated because a test or validation was repeated) — plus the kind's
// default name, interval, and provider settings.
func newDraft(kind domain.SourceKind) (config.SourceInstance, bool) {
	def, ok := registry.Get(kind)
	if !ok {
		return config.SourceInstance{}, false
	}
	id, err := config.GenerateInstanceID()
	if err != nil {
		return config.SourceInstance{}, false
	}
	inst := config.SourceInstance{
		ID:           id,
		Kind:         kind,
		Name:         def.DefaultName,
		Enabled:      true,
		PollInterval: def.DefaultInterval,
	}
	switch s := def.DefaultSettings().(type) {
	case config.AMDGPUWebSettings:
		inst.AMDGPUWeb = &s
	case config.LlamaSwapSettings:
		inst.LlamaSwap = &s
	case config.MiniMaxSettings:
		inst.MiniMax = &s
	case config.ZAISettings:
		inst.ZAI = &s
	case config.OpenRouterSettings:
		inst.OpenRouter = &s
	case config.HindsightSettings:
		inst.Hindsight = &s
	case config.CodexSettings:
		inst.Codex = &s
	}

	return inst, true
}

func indexByID(list []config.SourceInstance, id domain.InstanceID) int {
	for i := range list {
		if list[i].ID == id {
			return i
		}
	}

	return -1
}

// neighborInCategory returns the index of the nearest instance of the same
// category before (dir < 0) or after (dir > 0) position i, or -1. Ordering
// never crosses the Local/Remote boundary.
func neighborInCategory(list []config.SourceInstance, i int, dir int) int {
	cat := list[i].Kind.Category()
	for j := i + dir; j >= 0 && j < len(list); j += dir {
		if list[j].Kind.Category() == cat {
			return j
		}
	}

	return -1
}

// CanMoveUp reports whether the instance has a same-category predecessor.
func CanMoveUp(list []config.SourceInstance, id domain.InstanceID) bool {
	i := indexByID(list, id)

	return i >= 0 && neighborInCategory(list, i, -1) >= 0
}

// CanMoveDown reports whether the instance has a same-category successor.
func CanMoveDown(list []config.SourceInstance, id domain.InstanceID) bool {
	i := indexByID(list, id)

	return i >= 0 && neighborInCategory(list, i, 1) >= 0
}

// MoveUp swaps the instance with its nearest same-category predecessor.
// At the category edge (or for an unknown ID) the list is returned
// unchanged.
func MoveUp(list []config.SourceInstance, id domain.InstanceID) []config.SourceInstance {
	i := indexByID(list, id)
	if i < 0 {
		return list
	}
	j := neighborInCategory(list, i, -1)
	if j < 0 {
		return list
	}
	out := append([]config.SourceInstance(nil), list...)
	out[i], out[j] = out[j], out[i]

	return out
}

// MoveDown swaps the instance with its nearest same-category successor.
// At the category edge (or for an unknown ID) the list is returned
// unchanged.
func MoveDown(list []config.SourceInstance, id domain.InstanceID) []config.SourceInstance {
	i := indexByID(list, id)
	if i < 0 {
		return list
	}
	j := neighborInCategory(list, i, 1)
	if j < 0 {
		return list
	}
	out := append([]config.SourceInstance(nil), list...)
	out[i], out[j] = out[j], out[i]

	return out
}

// SetEnabled returns a copy with the instance's Enabled flag replaced.
// Instances are keyed by ID: rename and reorder never affect it.
func SetEnabled(list []config.SourceInstance, id domain.InstanceID, enabled bool) []config.SourceInstance {
	out := append([]config.SourceInstance(nil), list...)
	for i := range out {
		if out[i].ID == id {
			out[i].Enabled = enabled

			return out
		}
	}

	return list
}

// RemoveInstance returns a copy without the instance; unknown IDs leave
// the list unchanged.
func RemoveInstance(list []config.SourceInstance, id domain.InstanceID) []config.SourceInstance {
	i := indexByID(list, id)
	if i < 0 {
		return list
	}
	out := make([]config.SourceInstance, 0, len(list)-1)
	out = append(out, list[:i]...)
	out = append(out, list[i+1:]...)

	return out
}

// ReplaceInstance substitutes the edited instance in place (identity by
// ID); when the ID is not present the instance is appended, which lands it
// last within its category section.
func ReplaceInstance(list []config.SourceInstance, inst config.SourceInstance) []config.SourceInstance {
	i := indexByID(list, inst.ID)
	if i < 0 {
		out := make([]config.SourceInstance, 0, len(list)+1)
		out = append(out, list...)

		return append(out, inst)
	}
	out := append([]config.SourceInstance(nil), list...)
	out[i] = inst

	return out
}
