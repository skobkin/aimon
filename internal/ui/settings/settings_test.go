package settings

import (
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

func TestURLValidator(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"", false}, // empty falls back to the default on sanitize
		{"http://127.0.0.1:8080", false},
		{"https://api.z.ai", false},
		{"ftp://host", true},
		{"not a url", true},
		{"http://", true}, // no host
	}
	for _, c := range cases {
		err := urlValidator(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("urlValidator(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
		}
	}
}

func TestIntervalOptions(t *testing.T) {
	opts := intervalOptions(config.LocalIntervals)
	want := []string{"5s", "10s", "15s", "30s", "1m"} // 60s labels as 1m
	if len(opts) != len(want) {
		t.Fatalf("options = %v", opts)
	}
	for i := range want {
		if opts[i] != want[i] {
			t.Errorf("option[%d] = %q, want %q", i, opts[i], want[i])
		}
	}
	remote := intervalOptions(config.RemoteIntervals)
	if remote[0] != "30s" || remote[1] != "1m" || remote[4] != "10m" {
		t.Errorf("remote options = %v", remote)
	}
}

func TestGPUOption(t *testing.T) {
	if got := gpuOption(domain.GPUInfo{ID: "amdgpu0", Name: "Radeon RX 580"}); got != "Radeon RX 580 (amdgpu0)" {
		t.Errorf("named = %q", got)
	}
	if got := gpuOption(domain.GPUInfo{ID: "amdgpu1"}); got != "amdgpu1" {
		t.Errorf("unnamed = %q", got)
	}
}

func TestOptionForID(t *testing.T) {
	gpus := []domain.GPUInfo{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	if opt, ok := optionForID(gpus, "b"); !ok || opt != "B (b)" {
		t.Errorf("optionForID = %q, %v", opt, ok)
	}
	if _, ok := optionForID(gpus, "zz"); ok {
		t.Error("unknown ID must not match")
	}
}

func TestFormatDurationsRoundTrip(t *testing.T) {
	for _, d := range append(append([]time.Duration{}, config.LocalIntervals...), config.RemoteIntervals...) {
		if got := config.IntervalLabel(d); got == "" {
			t.Errorf("IntervalLabel(%v) empty", d)
		}
	}
}
