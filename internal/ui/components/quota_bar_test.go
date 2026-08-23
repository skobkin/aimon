package components

import (
	"image/color"
	"testing"

	"git.skobk.in/skobkin/aimonitor/internal/ui"
)

func TestThresholdColorBoundaries(t *testing.T) {
	cases := []struct {
		remaining float64
		want      color.Color
	}{
		{0.00, colorCritical},
		{0.10, colorCritical},
		{0.11, colorLow},
		{0.20, colorLow},
		{0.21, colorWarning},
		{0.50, colorWarning},
		{0.51, colorHealthy},
		{1.00, colorHealthy},
	}
	for _, c := range cases {
		if got := ThresholdColor(c.remaining); got != c.want {
			t.Errorf("ThresholdColor(%v) = %v, want %v", c.remaining, got, c.want)
		}
	}
}

func TestThresholdColorExactValues(t *testing.T) {
	// The exact palette from DECISIONS D9.
	if got := ThresholdColor(0.05); got != (color.RGBA{R: 0xF4, G: 0x43, B: 0x36, A: 0xFF}) {
		t.Errorf("critical color = %v", got)
	}
	if got := ThresholdColor(0.15); got != (color.RGBA{R: 0xFF, G: 0x98, B: 0x00, A: 0xFF}) {
		t.Errorf("low color = %v", got)
	}
	if got := ThresholdColor(0.30); got != (color.RGBA{R: 0xFF, G: 0xC1, B: 0x07, A: 0xFF}) {
		t.Errorf("warning color = %v", got)
	}
	if got := ThresholdColor(0.80); got != (color.RGBA{R: 0x4C, G: 0xAF, B: 0x50, A: 0xFF}) {
		t.Errorf("healthy color = %v", got)
	}
}

func TestLevelColor(t *testing.T) {
	cases := []struct {
		level ui.MetricLevel
		want  color.Color
	}{
		{ui.MetricGood, colorHealthy},
		{ui.MetricWarning, colorWarning},
		{ui.MetricLow, colorLow},
		{ui.MetricCritical, colorCritical},
	}
	for _, c := range cases {
		if got := LevelColor(c.level); got != c.want {
			t.Errorf("LevelColor(%v) = %v, want %v", c.level, got, c.want)
		}
	}
	// Neutral falls back to the theme foreground; headless (no app) it
	// is the fixed near-black stand-in.
	if got := LevelColor(ui.MetricNeutral); got == nil {
		t.Error("LevelColor(neutral) = nil")
	}
}

func TestNewQuotaBarClamps(t *testing.T) {
	b := NewQuotaBar(1.5, -3)
	if got := b.Used(); got != 1 {
		t.Errorf("used over range = %v, want 1", got)
	}
	if got := b.Remaining(); got != 0 {
		t.Errorf("remaining under range = %v, want 0", got)
	}
	if got := NewQuotaBar(0.42, 0.58).Used(); got != 0.42 {
		t.Errorf("used in range = %v, want 0.42", got)
	}
}

func TestSetFractionsClamps(t *testing.T) {
	b := NewQuotaBar(0.5, 0.5)
	b.SetFractions(2, 2)
	if got := b.Used(); got != 1 {
		t.Errorf("set used over = %v, want 1", got)
	}
	if got := b.Remaining(); got != 1 {
		t.Errorf("set remaining over = %v, want 1", got)
	}
	b.SetFractions(-1, -1)
	if got := b.Used(); got != 0 {
		t.Errorf("set used under = %v, want 0", got)
	}
	if got := b.Remaining(); got != 0 {
		t.Errorf("set remaining under = %v, want 0", got)
	}
}
