package config

import (
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

func TestValidateURL(t *testing.T) {
	cases := []struct {
		raw   string
		valid bool
	}{
		{"", false},
		{"http://127.0.0.1:8080", true},
		{"https://api.z.ai", true},
		{"ftp://127.0.0.1:8080", false},
		{"not a url", false},
		{"http://", false}, // scheme ok, host empty
	}
	for _, c := range cases {
		errs := validateURL("test", c.raw)
		if (len(errs) == 0) != c.valid {
			t.Errorf("validateURL(%q) errs = %v, valid = %v", c.raw, errs, c.valid)
		}
	}
	if errs := validateURL("test", ""); len(errs) != 1 || errs[0].Message != "must not be empty" {
		t.Errorf("empty-URL error = %v", errs)
	}
}

func TestValidateInterval(t *testing.T) {
	if errs := validateInterval("test", 5*time.Second, LocalIntervals); len(errs) != 0 {
		t.Errorf("preset interval rejected: %v", errs)
	}
	errs := validateInterval("test", 7*time.Second, LocalIntervals)
	if len(errs) != 1 || errs[0].Field != "Poll interval" {
		t.Fatalf("non-preset interval errors = %v", errs)
	}
	if want := "must be one of 5s, 10s, 15s, 30s, 1m"; errs[0].Message != want {
		t.Errorf("message = %q, want %q", errs[0].Message, want)
	}
	if errs := validateInterval("test", 30*time.Second, RemoteIntervals); len(errs) != 0 {
		t.Errorf("remote preset rejected: %v", errs)
	}
	if errs := validateInterval("test", 5*time.Second, RemoteIntervals); len(errs) != 1 {
		t.Errorf("local preset must not validate as remote: %v", errs)
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

func TestIntervalDefaults(t *testing.T) {
	if DefaultLocalInterval != 5*time.Second {
		t.Errorf("local default = %v", DefaultLocalInterval)
	}
	if DefaultRemoteInterval != 60*time.Second {
		t.Errorf("remote default = %v", DefaultRemoteInterval)
	}
	if DefaultHindsightInterval != 10*time.Second {
		t.Errorf("hindsight default = %v, want 10s", DefaultHindsightInterval)
	}
	if LocalIntervals[0] != 5*time.Second || RemoteIntervals[0] != 30*time.Second {
		t.Error("preset lists changed unexpectedly")
	}
}

func TestKindIntervals(t *testing.T) {
	cases := []struct {
		kind domain.SourceKind
		want []time.Duration
	}{
		{domain.KindAMDGPUWeb, LocalIntervals},
		{domain.KindLlamaSwap, LocalIntervals},
		{domain.KindMiniMax, RemoteIntervals},
		{domain.KindZAI, RemoteIntervals},
		{domain.KindOpenRouter, RemoteIntervals},
		// Hindsight is Local but keeps its own shifted-up ladder.
		{domain.KindHindsight, HindsightIntervals},
	}
	for _, c := range cases {
		got := KindIntervals(c.kind)
		if len(got) != len(c.want) {
			t.Fatalf("%s intervals = %v, want %v", c.kind, got, c.want)
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Fatalf("%s intervals = %v, want %v", c.kind, got, c.want)
			}
		}
	}

	if HindsightIntervals[0] != DefaultHindsightInterval {
		t.Error("the hindsight default must be the first preset of its ladder")
	}
	// Hindsight must not simply share the local list.
	if &HindsightIntervals[0] == &LocalIntervals[0] {
		t.Error("hindsight must have its own preset list, not the local one")
	}
}

// snapInterval is the load-time migration for a stored interval a preset
// list no longer offers: nearest preset wins, and an exact tie goes to the
// longer interval.
func TestSnapInterval(t *testing.T) {
	cases := []struct {
		name    string
		presets []time.Duration
		in      time.Duration
		want    time.Duration
	}{
		{"offered value unchanged", HindsightIntervals, time.Minute, time.Minute},
		{"old hindsight default moves up", HindsightIntervals, 5 * time.Second, 10 * time.Second},
		{"nearest wins", HindsightIntervals, 40 * time.Second, 30 * time.Second},
		{"tie resolves to the longer preset", HindsightIntervals, 12*time.Second + 500*time.Millisecond, 15 * time.Second},
		{"below the ladder snaps to the shortest", HindsightIntervals, 250 * time.Millisecond, 10 * time.Second},
		{"zero snaps to the shortest", HindsightIntervals, 0, 10 * time.Second},
		{"negative snaps to the shortest", HindsightIntervals, -time.Minute, 10 * time.Second},
		{"absurd value snaps to the longest", HindsightIntervals, 24 * time.Hour, 5 * time.Minute},
		{"local ladder keeps its own boundary", LocalIntervals, 2 * time.Hour, time.Minute},
		{"local tie resolves to the longer preset", LocalIntervals, 7500 * time.Millisecond, 10 * time.Second},
		{"remote ladder", RemoteIntervals, 45 * time.Second, time.Minute},
		{"single-preset list is trivially stable", []time.Duration{time.Minute}, time.Second, time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := snapInterval(c.presets, c.in); got != c.want {
				t.Errorf("snapInterval(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeInterval(t *testing.T) {
	// A value already on its kind's list passes through untouched.
	if got := normalizeInterval(domain.KindHindsight, 15*time.Second); got != 15*time.Second {
		t.Errorf("offered hindsight interval = %v, want it unchanged", got)
	}
	// The old local default is off-list for Hindsight now.
	if got := normalizeInterval(domain.KindHindsight, 5*time.Second); got != DefaultHindsightInterval {
		t.Errorf("stored 5s hindsight interval = %v, want %v", got, DefaultHindsightInterval)
	}
	// Other kinds keep their own list: a local 5s instance is untouched.
	if got := normalizeInterval(domain.KindAMDGPUWeb, 5*time.Second); got != 5*time.Second {
		t.Errorf("stored 5s local interval = %v, want it unchanged", got)
	}
	// An off-list local value normalizes the same way.
	if got := normalizeInterval(domain.KindAMDGPUWeb, 7*time.Second); got != 5*time.Second {
		t.Errorf("stored 7s local interval = %v, want 5s", got)
	}
}
