package config

import (
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

func TestKimiRegionBaseURL(t *testing.T) {
	cases := map[KimiRegion]string{"": KimiGlobalURL, KimiRegionGlobal: KimiGlobalURL, KimiRegionCN: KimiCNURL}
	for region, want := range cases {
		if got := (KimiSettings{Region: region}).BaseURL(); got != want {
			t.Errorf("region %q: BaseURL = %q, want %q", region, got, want)
		}
	}
}

func TestKimiValidate(t *testing.T) {
	inst := SourceInstance{ID: "k", Kind: domain.KindKimi, Name: "K", Enabled: true, PollInterval: time.Minute, Kimi: &KimiSettings{}}
	if errs := inst.Validate(); len(errs) != 0 {
		t.Errorf("empty region must be valid (default): %v", errs)
	}
	inst.Kimi = &KimiSettings{Region: "mars"}
	if errs := inst.Validate(); len(errs) == 0 {
		t.Error("unknown region accepted")
	}
	inst.Kimi = nil
	if errs := inst.Validate(); len(errs) == 0 {
		t.Error("missing settings accepted")
	}
}

func TestClaudeEffectiveCredentialsFile(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	if got := (ClaudeSettings{}).EffectiveCredentialsFile(); got != "/home/u/.claude/.credentials.json" {
		t.Errorf("default = %q", got)
	}
	if got := (ClaudeSettings{CredentialsFile: "/etc/c.json"}).EffectiveCredentialsFile(); got != "/etc/c.json" {
		t.Errorf("explicit = %q", got)
	}
	if !SameSettings(ClaudeSettings{}, ClaudeSettings{CredentialsFile: "~/.claude/.credentials.json"}) {
		t.Error("empty and explicit default must compare equal")
	}
}
