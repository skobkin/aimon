package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.AMDGPUWeb.Enabled || cfg.LlamaSwap.Enabled || cfg.MiniMax.Enabled || cfg.ZAI.Enabled {
		t.Error("defaults must start with every source disabled")
	}
	if cfg.AMDGPUWeb.BaseURL != DefaultAMDGPUWebURL {
		t.Errorf("amdgputop-web default URL = %q", cfg.AMDGPUWeb.BaseURL)
	}
	if cfg.LlamaSwap.BaseURL != DefaultLlamaSwapURL {
		t.Errorf("llama-swap default URL = %q", cfg.LlamaSwap.BaseURL)
	}
	if cfg.ZAI.BaseURL != DefaultZAIURL {
		t.Errorf("Z.AI default URL = %q", cfg.ZAI.BaseURL)
	}
	if cfg.AMDGPUWeb.PollInterval != 5*time.Second {
		t.Errorf("local default interval = %v", cfg.AMDGPUWeb.PollInterval)
	}
	if cfg.MiniMax.PollInterval != 60*time.Second {
		t.Errorf("remote default interval = %v", cfg.MiniMax.PollInterval)
	}
}

func TestValidate(t *testing.T) {
	valid := Default()
	if errs := valid.Validate(); len(errs) != 0 {
		t.Fatalf("default config should validate, got %v", errs)
	}

	bad := Default()
	bad.AMDGPUWeb.BaseURL = "ftp://127.0.0.1:8080"
	bad.LlamaSwap.BaseURL = "not a url"
	bad.ZAI.BaseURL = "http://" // scheme ok, host empty
	bad.AMDGPUWeb.PollInterval = 7 * time.Second
	bad.MiniMax.PollInterval = 5 * time.Second // below remote minimum preset

	errs := bad.Validate()
	if len(errs) != 5 {
		t.Fatalf("expected 5 validation errors, got %d: %v", len(errs), errs)
	}

	var sawScheme, sawHost, sawEmptyHost, sawLocalInterval, sawRemoteInterval bool
	for _, e := range errs {
		switch {
		case e.Source == "amdgputop-web" && e.Field == "URL" && strings.Contains(e.Message, "scheme"):
			sawScheme = true
		case e.Source == "llama-swap" && e.Field == "URL":
			sawHost = true
		case e.Source == "Z.AI / GLM" && e.Field == "URL":
			sawEmptyHost = true
		case e.Source == "amdgputop-web" && e.Field == "Poll interval":
			sawLocalInterval = true
		case e.Source == "MiniMax" && e.Field == "Poll interval":
			sawRemoteInterval = true
		}
	}
	if !(sawScheme && sawHost && sawEmptyHost && sawLocalInterval && sawRemoteInterval) {
		t.Errorf("missing expected errors: scheme=%v host=%v emptyHost=%v localInterval=%v remoteInterval=%v",
			sawScheme, sawHost, sawEmptyHost, sawLocalInterval, sawRemoteInterval)
	}
}

func TestValidateEmptyURL(t *testing.T) {
	bad := Default()
	bad.LlamaSwap.BaseURL = ""
	errs := bad.Validate()
	if len(errs) != 1 || errs[0].Message != "must not be empty" {
		t.Fatalf("expected empty-URL error, got %v", errs)
	}
}

func TestSanitize(t *testing.T) {
	def := Default()

	dirty := Config{
		AMDGPUWeb: AMDGPUWeb{
			BaseURL:      "  http://127.0.0.1:9999/  ",
			PollInterval: 15 * time.Second,
		},
		LlamaSwap: LlamaSwap{
			BaseURL:      "ftp://example.com",
			PollInterval: 5 * time.Second,
		},
		MiniMax: MiniMax{PollInterval: 2 * time.Minute},
		ZAI:     ZAI{BaseURL: "https://api.z.ai/", PollInterval: 1 * time.Minute},
	}

	got := dirty.Sanitize()

	if got.AMDGPUWeb.BaseURL != "http://127.0.0.1:9999" {
		t.Errorf("trim/slash-strip failed: %q", got.AMDGPUWeb.BaseURL)
	}
	if got.AMDGPUWeb.PollInterval != 15*time.Second {
		t.Error("valid interval must survive sanitize")
	}
	if got.LlamaSwap.BaseURL != def.LlamaSwap.BaseURL {
		t.Errorf("invalid URL must fall back to default, got %q", got.LlamaSwap.BaseURL)
	}
	if got.ZAI.BaseURL != "https://api.z.ai" {
		t.Errorf("trailing slash must be stripped: %q", got.ZAI.BaseURL)
	}

	worse := Default()
	worse.AMDGPUWeb.PollInterval = 0
	worse.MiniMax.PollInterval = -time.Second
	s := worse.Sanitize()
	if s.AMDGPUWeb.PollInterval != def.AMDGPUWeb.PollInterval {
		t.Errorf("invalid interval must fall back to default, got %v", s.AMDGPUWeb.PollInterval)
	}
	if s.MiniMax.PollInterval != def.MiniMax.PollInterval {
		t.Errorf("invalid remote interval must fall back to default, got %v", s.MiniMax.PollInterval)
	}

	// Empty URL falls back to default.
	empty := Default()
	empty.ZAI.BaseURL = ""
	if got := empty.Sanitize().ZAI.BaseURL; got != DefaultZAIURL {
		t.Errorf("empty URL must fall back to default, got %q", got)
	}
}

func TestIntervalLabel(t *testing.T) {
	cases := map[time.Duration]string{
		5 * time.Second:  "5s",
		10 * time.Second: "10s",
		15 * time.Second: "15s",
		30 * time.Second: "30s",
		60 * time.Second: "1m",
		2 * time.Minute:  "2m",
		5 * time.Minute:  "5m",
		10 * time.Minute: "10m",
		90 * time.Second: "1m30s", // non-preset falls back to Duration.String()
	}
	for d, want := range cases {
		if got := IntervalLabel(d); got != want {
			t.Errorf("IntervalLabel(%v) = %q, want %q", d, got, want)
		}
	}
}
