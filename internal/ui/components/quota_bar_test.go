package components

import (
	"image/color"
	"testing"
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

func TestNewQuotaBarClamps(t *testing.T) {
	if got := NewQuotaBar(1.5).Remaining(); got != 1 {
		t.Errorf("over range = %v, want 1", got)
	}
	if got := NewQuotaBar(-3).Remaining(); got != 0 {
		t.Errorf("under range = %v, want 0", got)
	}
	if got := NewQuotaBar(0.42).Remaining(); got != 0.42 {
		t.Errorf("in range = %v, want 0.42", got)
	}
}

func TestSetRemainingClamps(t *testing.T) {
	b := NewQuotaBar(0.5)
	b.SetRemaining(2)
	if got := b.Remaining(); got != 1 {
		t.Errorf("set over = %v, want 1", got)
	}
	b.SetRemaining(-1)
	if got := b.Remaining(); got != 0 {
		t.Errorf("set under = %v, want 0", got)
	}
}
