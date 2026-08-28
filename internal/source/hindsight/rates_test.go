package hindsight

import (
	"math"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
)

func f(v float64) *float64 { return &v }

func TestCounterDelta(t *testing.T) {
	inf := math.Inf(1)
	nan := math.NaN()
	cases := []struct {
		name  string
		prev  *float64
		curr  *float64
		delta float64
		ok    bool
	}{
		{"advance", f(10), f(13), 3, true},
		{"flat", f(10), f(10), 0, true},
		{"reset", f(10), f(2), 0, false},
		{"missing prev", nil, f(13), 0, false},
		{"missing curr", f(10), nil, 0, false},
		{"non-finite prev", &inf, f(1), 0, false},
		{"non-finite curr", f(1), &nan, 0, false},
	}
	for _, tc := range cases {
		got, ok := counterDelta(tc.prev, tc.curr)
		if ok != tc.ok || (ok && got != tc.delta) {
			t.Errorf("%s: counterDelta = (%v, %v), want (%v, %v)", tc.name, got, ok, tc.delta, tc.ok)
		}
	}
}

func TestPerMinute(t *testing.T) {
	if got := perMinute(30, 30*time.Second); got == nil || *got != 60 {
		t.Errorf("perMinute(30, 30s) = %v, want 60", got)
	}
	if got := perMinute(3, time.Minute); got == nil || *got != 3 {
		t.Errorf("perMinute(3, 1m) = %v, want 3", got)
	}
	if got := perMinute(1, 0); got != nil {
		t.Errorf("perMinute(1, 0) = %v, want nil", got)
	}
	if got := perMinute(1, -time.Second); got != nil {
		t.Errorf("perMinute(1, -1s) = %v, want nil", got)
	}
}

func TestPercentOf(t *testing.T) {
	cases := []struct {
		name string
		part float64
		all  float64
		want *float64
	}{
		{"plain", 1, 4, f(25)},
		{"zero part", 0, 10, f(0)},
		{"no traffic", 0, 0, nil},
		{"negative total", 1, -5, nil},
		{"noise below", -1e-12, 10, f(0)},
		{"materially negative", -1, 10, nil},
		{"noise overshoot", 100.0000000001, 100, f(100)},
		{"impossible", 150, 100, nil},
	}
	for _, tc := range cases {
		got := percentOf(tc.part, tc.all)
		if tc.want == nil {
			if got != nil {
				t.Errorf("%s: percentOf = %v, want nil", tc.name, *got)
			}
			continue
		}
		if got == nil || *got != *tc.want {
			t.Errorf("%s: percentOf = %v, want %v", tc.name, got, *tc.want)
		}
	}
}

func TestDeriveActivityFirstSample(t *testing.T) {
	act := deriveActivity(nil, &metricsCounters{}, time.Minute)
	if act != (domain.HindsightActivity{}) {
		t.Errorf("first sample rates = %+v, want all unavailable", act)
	}
}

// TestDeriveActivityPerFieldIndependence: a family absent on one side only
// blanks its own rate, never the others; a family present on both sides
// with no movement is a real zero.
func TestDeriveActivityPerFieldIndependence(t *testing.T) {
	prev := &metricsCounters{RetainTotal: f(10), RecallTotal: f(4), ReflectTotal: f(0)}
	curr := &metricsCounters{RetainTotal: f(12), RecallTotal: nil, ReflectTotal: f(0)}

	act := deriveActivity(prev, curr, time.Minute)
	if act.RetainPerMin == nil || *act.RetainPerMin != 2 {
		t.Errorf("RetainPerMin = %v, want 2", act.RetainPerMin)
	}
	if act.RecallPerMin != nil {
		t.Errorf("RecallPerMin = %v, want nil (missing on one side)", *act.RecallPerMin)
	}
	if act.ReflectPerMin == nil || *act.ReflectPerMin != 0 {
		t.Errorf("ReflectPerMin = %v, want 0 (no movement is a real zero)", act.ReflectPerMin)
	}
	if act.LLMCallsPerMin != nil {
		t.Errorf("LLMCallsPerMin = %v, want nil (absent on both sides)", *act.LLMCallsPerMin)
	}
}

func TestDeriveActivityResetIntervalUnavailable(t *testing.T) {
	prev := &metricsCounters{RetainTotal: f(10)}
	curr := &metricsCounters{RetainTotal: f(1)}
	if act := deriveActivity(prev, curr, time.Minute); act.RetainPerMin != nil {
		t.Errorf("reset rate = %v, want nil", *act.RetainPerMin)
	}
}

func TestDomainQueueNilStaysNil(t *testing.T) {
	if got := domainQueue(nil); got != nil {
		t.Errorf("domainQueue(nil) = %+v, want nil", got)
	}
	q := domainQueue(&queueGauges{Pending: f(2)})
	if q == nil || q.Pending == nil || *q.Pending != 2 {
		t.Errorf("domainQueue = %+v, want pending 2", q)
	}
}
