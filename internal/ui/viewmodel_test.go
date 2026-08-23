package ui

import (
	"testing"
	"time"

	"github.com/skobkin/llm-monitor/internal/domain"
	"github.com/skobkin/llm-monitor/internal/source"
)

func f(v float64) *float64         { return &v }
func u(v uint64) *uint64           { return &v }
func ts(d time.Duration) time.Time { return time.Unix(1770000000, 0).Add(-d) }

var now = time.Unix(1770000000, 0)

func gpuSnap() domain.GPUSnapshot {
	return domain.GPUSnapshot{
		CollectedAt:       now.Add(-5 * time.Second),
		DeviceID:          "amdgpu0",
		Name:              "Radeon RX 580",
		GPUUtilizationPct: f(74.5),
		TemperatureC:      f(63),
		PowerWatts:        f(172),
		SCLKMHz:           f(2420),
		MCLKMHz:           f(1249),
		VRAMUsedBytes:     u(14800000000),
		VRAMTotalBytes:    u(17179869184),
		GTTUsedBytes:      u(2254857830),
		GTTTotalBytes:     u(68719476736),
	}
}

func TestFormatAgeBoundaries(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want string
	}{
		{0, "0s ago"},
		{59 * time.Second, "59s ago"},
		{60 * time.Second, "1m ago"},
		{59 * time.Minute, "59m ago"},
		{60 * time.Minute, "1h ago"},
		{23 * time.Hour, "23h ago"},
		{24 * time.Hour, "1d ago"},
	}
	for _, c := range cases {
		if got := FormatAge(now.Add(-c.age), now); got != c.want {
			t.Errorf("FormatAge(%v) = %q, want %q", c.age, got, c.want)
		}
	}
	if got := FormatAge(time.Time{}, now); got != "—" {
		t.Errorf("zero time = %q, want em dash", got)
	}
}

func TestFormatUsedRounding(t *testing.T) {
	cases := []struct {
		frac float64
		want string
	}{
		{0.28, "28% used"},
		{0.586, "59% used"}, // rounds to nearest
		{0.0, "0% used"},
		{1.0, "100% used"},
		{-0.5, "0% used"}, // clamped
		{1.5, "100% used"},
	}
	for _, c := range cases {
		if got := FormatUsed(c.frac); got != c.want {
			t.Errorf("FormatUsed(%v) = %q, want %q", c.frac, got, c.want)
		}
	}
}

func TestFormatUnits(t *testing.T) {
	if got := FormatBytesGiB(14800000000); got != "13.8" {
		t.Errorf("GiB = %q", got)
	}
	if got := FormatBytesGiB(17179869184); got != "16.0" {
		t.Errorf("GiB total = %q", got)
	}
	if got := FormatTemp(f(63)); got != "63 °C" {
		t.Errorf("temp = %q", got)
	}
	if got := FormatTemp(f(63.4)); got != "63 °C" {
		t.Errorf("temp fraction = %q", got)
	}
	if got := FormatMHz(f(2420)); got != "2420 MHz" {
		t.Errorf("mhz = %q", got)
	}
	if got := FormatWatts(f(172)); got != "172 W" {
		t.Errorf("watts = %q", got)
	}
	if got := FormatPct(f(74.5)); got != "74%" { // %.0f rounds half to even
		t.Logf("FormatPct(74.5) = %q", got)
	}
}

func TestFormatCount(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{312, "312"},
		{9999, "9999"},
		{10000, "10.0k"},
		{12400, "12.4k"},
		{1200000, "1.2M"},
	}
	for _, c := range cases {
		if got := FormatCount(c.v); got != c.want {
			t.Errorf("FormatCount(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestFreshnessTextPerState(t *testing.T) {
	err := &source.Error{Category: source.CatConnection, Detail: "refused"}

	cases := []struct {
		name string
		rt   domain.Runtime
		want string
	}{
		{"disabled", domain.Runtime{}, ""},
		{"loading", domain.Runtime{Enabled: true}, "Loading…"},
		{"unavailable", domain.Runtime{Enabled: true, LastError: err}, "Unavailable"},
		{"stale", domain.Runtime{Enabled: true, LastError: err, LastSuccessAt: ts(time.Minute), Snapshot: gpuSnap()}, "1m ago · stale"},
		{"healthy", domain.Runtime{Enabled: true, LastSuccessAt: ts(5 * time.Second), Snapshot: gpuSnap()}, "5s ago"},
	}
	for _, c := range cases {
		if got := FreshnessText(c.rt, now); got != c.want {
			t.Errorf("%s freshness = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFooterTextOnlyOnFailureStates(t *testing.T) {
	err := &source.Error{Category: source.CatTimeout, Detail: "10s"}
	withData := domain.Runtime{Enabled: true, LastError: err, LastSuccessAt: now, Snapshot: gpuSnap()}

	if got := FooterText(withData); got != "Last refresh failed: Request timed out" {
		t.Errorf("stale footer = %q", got)
	}
	noData := domain.Runtime{Enabled: true, LastError: err}
	if got := FooterText(noData); got != "Last refresh failed: Request timed out" {
		t.Errorf("unavailable footer = %q", got)
	}
	healthy := domain.Runtime{Enabled: true, LastSuccessAt: now, Snapshot: gpuSnap()}
	if got := FooterText(healthy); got != "" {
		t.Errorf("healthy footer = %q, want empty", got)
	}
	if got := FooterText(domain.Runtime{Enabled: true}); got != "" {
		t.Errorf("loading footer = %q, want empty", got)
	}
}

func TestBuildGPUVMOverviewOmitsMissing(t *testing.T) {
	snap := gpuSnap()
	vm := BuildGPUVM(snap, false)
	if len(vm.Overview) != 3 {
		t.Fatalf("overview rows = %d, want 3", len(vm.Overview))
	}
	want := []MetricRowVM{
		{Label: "GPU util", Value: "74%", Level: MetricWarning},
		{Label: "VRAM", Value: "13.8 / 16.0 GiB", Level: MetricLow},
		{Label: "Temp", Value: "63 °C", Level: MetricWarning},
	}
	for i, w := range want {
		if vm.Overview[i] != w {
			t.Errorf("overview[%d] = %+v, want %+v", i, vm.Overview[i], w)
		}
	}

	// Missing sensors disappear from the overview.
	sparse := domain.GPUSnapshot{GPUUtilizationPct: f(12)}
	vm = BuildGPUVM(sparse, false)
	if len(vm.Overview) != 1 || vm.Overview[0].Label != "GPU util" {
		t.Errorf("sparse overview = %+v", vm.Overview)
	}
}

func TestBuildGPUVMDetailShowsEveryRowWithDash(t *testing.T) {
	vm := BuildGPUVM(gpuSnap(), true)
	if len(vm.Detail) != 7 {
		t.Fatalf("detail rows = %d, want 7", len(vm.Detail))
	}
	labels := map[string]string{}
	for _, r := range vm.Detail {
		labels[r.Label] = r.Value
	}
	for _, want := range []struct{ label, value string }{
		{"GPU util", "74%"},
		{"VRAM", "13.8 / 16.0 GiB"},
		{"GTT", "2.1 / 64.0 GiB"},
		{"Temp", "63 °C"},
		{"Power", "172 W"},
		{"SCLK", "2420 MHz"},
		{"MCLK", "1249 MHz"},
	} {
		if labels[want.label] != want.value {
			t.Errorf("detail %s = %q, want %q", want.label, labels[want.label], want.value)
		}
	}

	// A null sensor becomes an em dash, never a fake zero, and carries
	// no load level.
	empty := domain.GPUSnapshot{}
	vm = BuildGPUVM(empty, true)
	for _, r := range vm.Detail {
		if r.Value != "—" {
			t.Errorf("null sensor %s = %q, want em dash", r.Label, r.Value)
		}
		if r.Level != MetricNeutral {
			t.Errorf("null sensor %s level = %v, want neutral", r.Label, r.Level)
		}
	}
}

// Load-level boundaries mirror the quota-bar thresholds onto usage;
// temperature uses absolute °C bands (user-agreed 2026-08-23).
func TestBuildGPUVMMetricLevels(t *testing.T) {
	cases := []struct {
		name string
		snap domain.GPUSnapshot
		want MetricLevel
	}{
		{"util 50", domain.GPUSnapshot{GPUUtilizationPct: f(50)}, MetricGood},
		{"util 51", domain.GPUSnapshot{GPUUtilizationPct: f(51)}, MetricWarning},
		{"util 80", domain.GPUSnapshot{GPUUtilizationPct: f(80)}, MetricWarning},
		{"util 81", domain.GPUSnapshot{GPUUtilizationPct: f(81)}, MetricLow},
		{"util 90", domain.GPUSnapshot{GPUUtilizationPct: f(90)}, MetricLow},
		{"util 91", domain.GPUSnapshot{GPUUtilizationPct: f(91)}, MetricCritical},
		{"vram half", domain.GPUSnapshot{VRAMUsedBytes: u(8), VRAMTotalBytes: u(16)}, MetricGood},
		{"vram 81%", domain.GPUSnapshot{VRAMUsedBytes: u(81), VRAMTotalBytes: u(100)}, MetricLow},
		{"vram 91%", domain.GPUSnapshot{VRAMUsedBytes: u(91), VRAMTotalBytes: u(100)}, MetricCritical},
		{"vram no total", domain.GPUSnapshot{VRAMUsedBytes: u(91)}, MetricNeutral},
		{"temp 59", domain.GPUSnapshot{TemperatureC: f(59)}, MetricGood},
		{"temp 60", domain.GPUSnapshot{TemperatureC: f(60)}, MetricWarning},
		{"temp 70", domain.GPUSnapshot{TemperatureC: f(70)}, MetricLow},
		{"temp 80", domain.GPUSnapshot{TemperatureC: f(80)}, MetricCritical},
	}
	for _, c := range cases {
		vm := BuildGPUVM(c.snap, true)
		var got MetricLevel
		var found bool
		for _, r := range vm.Detail {
			if r.Level != MetricNeutral {
				got, found = r.Level, true
			}
		}
		if !found && c.want != MetricNeutral {
			t.Errorf("%s: no leveled row in %+v", c.name, vm.Detail)
			continue
		}
		if found && got != c.want {
			t.Errorf("%s level = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildSwapVMCapsOverviewAndPrefersNames(t *testing.T) {
	snap := domain.LlamaSwapSnapshot{
		Healthy: true,
		RunningModels: []domain.RunningModel{
			{ID: "qwen3:14b", Name: "Qwen 3 14B"},
			{ID: "gemma3:4b"},
			{ID: "llama3:8b", Name: "Llama 3 8B"},
			{ID: "mistral:7b", Name: "Mistral 7B"},
		},
	}

	over := BuildSwapVM(snap, false)
	if len(over.ModelLines) != 3 {
		t.Fatalf("overview lines = %d, want 3", len(over.ModelLines))
	}
	if over.MoreCount != 1 {
		t.Errorf("more = %d, want 1", over.MoreCount)
	}
	if over.ModelLines[0] != "Qwen 3 14B" || over.ModelLines[1] != "gemma3:4b" {
		t.Errorf("lines = %+v", over.ModelLines)
	}

	detail := BuildSwapVM(snap, true)
	if len(detail.ModelLines) != 4 || detail.MoreCount != 0 {
		t.Errorf("detail = %d lines, more %d", len(detail.ModelLines), detail.MoreCount)
	}
}

func TestBuildQuotaVM(t *testing.T) {
	rem, lim := 690.0, 1000.0
	snap := domain.QuotaSnapshot{
		Source: domain.SourceMiniMax,
		Windows: []domain.QuotaWindow{
			{ID: "interval", Name: "5-hour", RemainingFraction: 0.69, UsedFraction: 0.31, Remaining: &rem, Limit: &lim, Unit: "requests"},
			{ID: "weekly", Name: "Weekly", RemainingFraction: 0.87, UsedFraction: 0.13},
		},
	}

	over := BuildQuotaVM(snap, false)
	if len(over.Windows) != 2 {
		t.Fatalf("windows = %d", len(over.Windows))
	}
	if over.Windows[0].Secondary != "" {
		t.Error("overview must not show raw counts")
	}

	detail := BuildQuotaVM(snap, true)
	if detail.Windows[0].Secondary != "310 / 1000 requests" {
		t.Errorf("secondary = %q", detail.Windows[0].Secondary)
	}
	if detail.Windows[1].Secondary != "" {
		t.Errorf("percent-only window secondary = %q, want empty", detail.Windows[1].Secondary)
	}
	if detail.Windows[0].UsedText != "31% used" {
		t.Errorf("used text = %q", detail.Windows[0].UsedText)
	}
	if detail.Windows[0].Used != 0.31 || detail.Windows[0].Remaining != 0.69 {
		t.Errorf("fractions = used %v remaining %v, want 0.31/0.69",
			detail.Windows[0].Used, detail.Windows[0].Remaining)
	}
}

func TestBuildOverviewOmitsDisabled(t *testing.T) {
	states := map[domain.SourceID]domain.Runtime{
		domain.SourceAMDGPUWeb: {Enabled: true, LastSuccessAt: ts(5 * time.Second), Snapshot: gpuSnap()},
		domain.SourceLlamaSwap: {Enabled: false},
		domain.SourceMiniMax:   {Enabled: true, LastError: &source.Error{Category: source.CatConnection, Detail: "refused"}},
	}

	vm := BuildOverview(states, now)
	if len(vm.LocalCards) != 1 {
		t.Fatalf("local cards = %d, want 1 (llama-swap disabled)", len(vm.LocalCards))
	}
	if len(vm.RemoteCards) != 1 {
		t.Fatalf("remote cards = %d, want 1 (minimax enabled, failing)", len(vm.RemoteCards))
	}
	if vm.NothingEnabled {
		t.Error("nothing-enabled must be false")
	}

	gpu := vm.LocalCards[0]
	if gpu.Kind != KindGPU || gpu.Title != "amdgputop-web" || gpu.Status != "5s ago" {
		t.Errorf("gpu card = %+v", gpu)
	}
	mm := vm.RemoteCards[0]
	if mm.Kind != KindNone || mm.Status != "Unavailable" {
		t.Errorf("minimax card = %+v", mm)
	}
	if mm.Footer != "Last refresh failed: Connection failed" {
		t.Errorf("minimax footer = %q", mm.Footer)
	}
}

func TestBuildOverviewNothingEnabled(t *testing.T) {
	vm := BuildOverview(map[domain.SourceID]domain.Runtime{}, now)
	if !vm.NothingEnabled {
		t.Error("empty states must set NothingEnabled")
	}

	vm = BuildOverview(map[domain.SourceID]domain.Runtime{
		domain.SourceZAI: {Enabled: false},
	}, now)
	if !vm.NothingEnabled {
		t.Error("all-disabled must set NothingEnabled")
	}
}

func TestBuildLocalAndRemoteTabs(t *testing.T) {
	states := map[domain.SourceID]domain.Runtime{
		domain.SourceLlamaSwap: {Enabled: true, LastSuccessAt: now, Snapshot: domain.LlamaSwapSnapshot{RunningModels: []domain.RunningModel{{ID: "m1"}}}},
		domain.SourceZAI:       {Enabled: true, LastSuccessAt: now, Snapshot: domain.QuotaSnapshot{Windows: []domain.QuotaWindow{{ID: "tokens-5h", Name: "Token usage · 5 hour", RemainingFraction: 0.58, UsedFraction: 0.42}}}},
	}

	local := BuildLocalTab(states, now)
	if len(local) != 1 || local[0].Kind != KindSwap {
		t.Fatalf("local tab = %+v", local)
	}
	remote := BuildRemoteTab(states, now)
	if len(remote) != 1 || remote[0].Kind != KindQuota {
		t.Fatalf("remote tab = %+v", remote)
	}
	if w := remote[0].Quota.Windows[0]; w.UsedText != "42% used" {
		t.Errorf("used = %q", w.UsedText)
	}

	if got := BuildLocalTab(map[domain.SourceID]domain.Runtime{}, now); len(got) != 0 {
		t.Errorf("empty local tab = %+v", got)
	}
	if got := BuildRemoteTab(map[domain.SourceID]domain.Runtime{}, now); len(got) != 0 {
		t.Errorf("empty remote tab = %+v", got)
	}
}
