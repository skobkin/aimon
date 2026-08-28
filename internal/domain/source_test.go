package domain

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestSourceKindCategory(t *testing.T) {
	cases := []struct {
		id   SourceKind
		want Category
	}{
		{KindAMDGPUWeb, CategoryLocal},
		{KindLlamaSwap, CategoryLocal},
		{KindHindsight, CategoryLocal},
		{KindMiniMax, CategoryRemote},
		{KindZAI, CategoryRemote},
		{KindOpenRouter, CategoryRemote},
	}
	for _, tc := range cases {
		if got := tc.id.Category(); got != tc.want {
			t.Errorf("%s.Category() = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestDeriveState(t *testing.T) {
	snap := GPUSnapshot{CollectedAt: time.Now()}
	err := errors.New("boom")

	cases := []struct {
		name string
		rt   Runtime
		want State
	}{
		{"disabled", Runtime{}, StateDisabled},
		{"enabled fresh no attempt", Runtime{Enabled: true}, StateLoading},
		{"refreshing first time", Runtime{Enabled: true, Refreshing: true}, StateLoading},
		{"first attempt failed", Runtime{Enabled: true, LastError: err}, StateUnavailable},
		{"healthy", Runtime{Enabled: true, Snapshot: snap}, StateHealthy},
		{"healthy while refreshing", Runtime{Enabled: true, Snapshot: snap, Refreshing: true}, StateHealthy},
		{"stale keeps snapshot", Runtime{Enabled: true, Snapshot: snap, LastError: err}, StateStale},
		{"disabled ignores everything", Runtime{Enabled: false, Snapshot: snap, LastError: err}, StateDisabled},
	}
	for _, tc := range cases {
		if got := tc.rt.DeriveState(); got != tc.want {
			t.Errorf("%s: DeriveState() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// fakeSnapshot verifies the Snapshot interface is satisfied structurally by
// distinct implementations.
type fakeSnapshot struct {
	source SourceKind
	at     time.Time
}

func (f fakeSnapshot) SnapshotKind() SourceKind { return f.source }
func (f fakeSnapshot) Timestamp() time.Time     { return f.at }

func TestSnapshotConformance(t *testing.T) {
	at := time.Now()
	snaps := map[string]Snapshot{
		"gpu":        GPUSnapshot{CollectedAt: at},
		"llamaswap":  LlamaSwapSnapshot{CollectedAt: at},
		"minimax":    QuotaSnapshot{Kind: KindMiniMax, CollectedAt: at},
		"zai":        QuotaSnapshot{Kind: KindZAI, CollectedAt: at},
		"openrouter": OpenRouterSnapshot{CollectedAt: at},
		"hindsight":  HindsightSnapshot{CollectedAt: at},
		"fake":       fakeSnapshot{source: KindZAI, at: at},
	}
	for name, s := range snaps {
		if s.Timestamp() != at {
			t.Errorf("%s: Timestamp() = %v, want %v", name, s.Timestamp(), at)
		}
	}
	if (GPUSnapshot{}).SnapshotKind() != KindAMDGPUWeb {
		t.Error("GPUSnapshot source mismatch")
	}
	if (LlamaSwapSnapshot{}).SnapshotKind() != KindLlamaSwap {
		t.Error("LlamaSwapSnapshot source mismatch")
	}
	if (QuotaSnapshot{Kind: KindZAI}).SnapshotKind() != KindZAI {
		t.Error("QuotaSnapshot source mismatch")
	}
	if (OpenRouterSnapshot{}).SnapshotKind() != KindOpenRouter {
		t.Error("OpenRouterSnapshot source mismatch")
	}
	if (HindsightSnapshot{}).SnapshotKind() != KindHindsight {
		t.Error("HindsightSnapshot source mismatch")
	}
}

func TestFraction(t *testing.T) {
	ok := []struct {
		in, want float64
	}{
		{0, 0},
		{1, 1},
		{0.5, 0.5},
		{0.42, 0.42},
	}
	for _, tc := range ok {
		got, err := Fraction(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("Fraction(%v) = (%v, %v), want (%v, nil)", tc.in, got, err, tc.want)
		}
	}

	bad := []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.1, -1, 1.5, 2}
	for _, v := range bad {
		if got, err := Fraction(v); err == nil {
			t.Errorf("Fraction(%v) = (%v, nil), want an error", v, got)
		}
	}
}

func TestClamp01(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{0, 0},
		{0.3, 0.3},
		{1, 1},
		{1.7, 1},
		{-0.2, 0},
		{math.NaN(), 0},
	}
	for _, tc := range cases {
		if got := Clamp01(tc.in); got != tc.want {
			t.Errorf("Clamp01(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
