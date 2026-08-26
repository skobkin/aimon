package ui

import (
	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

// Console pages of the remote providers (their own usage dashboards, not
// the API endpoints the adapters poll).
const (
	MiniMaxConsoleURL = "https://platform.minimax.io/console/usage"
	ZAIUsageURL       = "https://z.ai/manage-apikey/coding-plan/personal/usage"
)

// SourceLink is a source's web UI entry point. Label names the
// destination without an action verb; call sites add their own phrasing
// ("Open llama-swap UI" card button, "llama-swap UI" tray menu item).
type SourceLink struct {
	Label string
	URL   string
}

// SourceWebLink returns the web UI link shown on one source's card
// (navigation only — S1 never calls control APIs). ok is false for
// sources without a web UI.
func SourceWebLink(id domain.SourceKind, cfg config.Config) (SourceLink, bool) {
	switch id {
	case domain.KindAMDGPUWeb:
		return SourceLink{Label: "AMDGPUTOP web UI", URL: cfg.AMDGPUWeb.BaseURL}, true
	case domain.KindLlamaSwap:
		return SourceLink{Label: "llama-swap UI", URL: cfg.LlamaSwap.BaseURL + "/ui"}, true
	case domain.KindMiniMax:
		return SourceLink{Label: "MiniMax usage", URL: MiniMaxConsoleURL}, true
	case domain.KindZAI:
		return SourceLink{Label: "Z.ai usage", URL: ZAIUsageURL}, true
	}
	return SourceLink{}, false
}

// SourceLinks lists the ENABLED sources' web UIs in the UI's own order
// (local sources, then remote) — the tray's Open submenu. Disabled
// sources are omitted.
func SourceLinks(cfg config.Config) []SourceLink {
	order := []domain.SourceKind{
		domain.KindAMDGPUWeb,
		domain.KindLlamaSwap,
		domain.KindMiniMax,
		domain.KindZAI,
	}
	links := make([]SourceLink, 0, len(order))
	for _, id := range order {
		if !sourceEnabled(id, cfg) {
			continue
		}
		if l, ok := SourceWebLink(id, cfg); ok {
			links = append(links, l)
		}
	}
	return links
}

// sourceEnabled reports whether the source is switched on in the config.
func sourceEnabled(id domain.SourceKind, cfg config.Config) bool {
	switch id {
	case domain.KindAMDGPUWeb:
		return cfg.AMDGPUWeb.Enabled
	case domain.KindLlamaSwap:
		return cfg.LlamaSwap.Enabled
	case domain.KindMiniMax:
		return cfg.MiniMax.Enabled
	case domain.KindZAI:
		return cfg.ZAI.Enabled
	}
	return false
}
