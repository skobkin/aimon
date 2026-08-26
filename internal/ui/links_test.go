package ui

import (
	"testing"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

func TestSourceWebLinkPerSource(t *testing.T) {
	cfg := config.Config{}
	cfg.AMDGPUWeb.BaseURL = "http://127.0.0.1:19999"
	cfg.LlamaSwap.BaseURL = "http://127.0.0.1:9999"
	cases := []struct {
		id    domain.SourceKind
		label string
		url   string
	}{
		{domain.KindAMDGPUWeb, "AMDGPUTOP web UI", "http://127.0.0.1:19999"},
		{domain.KindLlamaSwap, "llama-swap UI", "http://127.0.0.1:9999/ui"},
		{domain.KindMiniMax, "MiniMax usage", MiniMaxConsoleURL},
		{domain.KindZAI, "Z.ai usage", ZAIUsageURL},
	}
	for _, c := range cases {
		l, ok := SourceWebLink(c.id, cfg)
		if !ok || l.Label != c.label || l.URL != c.url {
			t.Errorf("SourceWebLink(%s) = %+v ok=%v; want %q %q", c.id, l, ok, c.label, c.url)
		}
	}
	if _, ok := SourceWebLink(domain.SourceKind("nope"), cfg); ok {
		t.Error("unknown source must have no link")
	}
}

func TestSourceLinksEnabledOnlyInUIOrder(t *testing.T) {
	cfg := config.Config{}
	cfg.AMDGPUWeb.Enabled = true
	cfg.ZAI.Enabled = true
	// Configured but disabled: must be omitted.
	cfg.LlamaSwap.BaseURL = "http://127.0.0.1:9999"

	links := SourceLinks(cfg)
	if len(links) != 2 {
		t.Fatalf("links = %+v, want 2 (llama-swap disabled)", links)
	}
	if links[0].Label != "AMDGPUTOP web UI" || links[1].Label != "Z.ai usage" {
		t.Errorf("order = %q, %q; want local first then remote", links[0].Label, links[1].Label)
	}
	if got := SourceLinks(config.Config{}); len(got) != 0 {
		t.Errorf("no enabled sources = %+v, want none", got)
	}
}
