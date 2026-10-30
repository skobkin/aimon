// List operations on the configured source instances. Everything here is
// pure: functions derive a new candidate list from the current one and
// never touch widgets, so ordering, enable and delete semantics are
// unit-testable without a display.
package settings

import (
	"strings"

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

// duplicateInstance builds the starting point for duplicating an existing
// instance: a fresh instance ID — credential-free by construction, since
// keychain accounts are keyed per instance ID — with the source's provider
// settings, poll interval, pace window, and enabled flag, and a
// " (copy)"-suffixed name. Nothing is persisted; the editor owns that.
func duplicateInstance(inst config.SourceInstance) (config.SourceInstance, bool) {
	draft, ok := newDraft(inst.Kind)
	if !ok {
		return config.SourceInstance{}, false
	}
	draft.Name = inst.Name + " (copy)"
	draft.Enabled = inst.Enabled
	draft.PollInterval = inst.PollInterval
	draft.PaceWindow = inst.PaceWindow
	if s, ok := inst.Settings(); ok {
		switch v := s.(type) {
		case config.AMDGPUWebSettings:
			draft.AMDGPUWeb = &v
		case config.LlamaSwapSettings:
			draft.LlamaSwap = &v
		case config.MiniMaxSettings:
			draft.MiniMax = &v
		case config.ZAISettings:
			draft.ZAI = &v
		case config.OpenRouterSettings:
			draft.OpenRouter = &v
		case config.HindsightSettings:
			draft.Hindsight = &v
		case config.CodexSettings:
			draft.Codex = &v
		}
	}

	return draft, true
}

// duplicateNameHint returns the editor's soft warning for a name that
// case-insensitively collides with another configured instance. Duplicates
// stay allowed: the hint is presentation, not validation.
func duplicateNameHint(name string, self domain.InstanceID, instances []config.SourceInstance) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	for _, inst := range instances {
		if inst.ID != self && strings.EqualFold(strings.TrimSpace(inst.Name), trimmed) {
			return "Another source already uses this name."
		}
	}

	return ""
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
