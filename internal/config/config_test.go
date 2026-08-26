package config

import (
	"testing"
	"time"
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
	if LocalIntervals[0] != 5*time.Second || RemoteIntervals[0] != 30*time.Second {
		t.Error("preset lists changed unexpectedly")
	}
}
