package ui

import (
	"math"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

func f(v float64) *float64         { return &v }
func u(v uint64) *uint64           { return &v }
func ts(d time.Duration) time.Time { return time.Unix(1770000000, 0).Add(-d) }

// inst builds a minimal valid instance for view-model tests; the provider
// settings are placeholders the view models never read.
func inst(id, name string, kind domain.SourceKind, enabled bool) config.SourceInstance {
	i := config.SourceInstance{
		ID:           domain.InstanceID(id),
		Kind:         kind,
		Name:         name,
		Enabled:      enabled,
		PollInterval: 5 * time.Second,
	}
	switch kind {
	case domain.KindAMDGPUWeb:
		i.AMDGPUWeb = &config.AMDGPUWebSettings{BaseURL: "http://127.0.0.1:9990"}
	case domain.KindLlamaSwap:
		i.LlamaSwap = &config.LlamaSwapSettings{BaseURL: "http://127.0.0.1:9991"}
	case domain.KindMiniMax:
		i.MiniMax = &config.MiniMaxSettings{}
	case domain.KindZAI:
		i.ZAI = &config.ZAISettings{BaseURL: "https://api.z.ai"}
	case domain.KindOpenRouter:
		i.OpenRouter = &config.OpenRouterSettings{}
	}
	return i
}

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
		{0.004, "<1% used"}, // positive but rounds to zero
		{0.005, "1% used"},  // the exact half boundary rounds up, no "<1%"
		{1.0, "100% used"},
		{-0.5, "0% used"}, // clamped, not positive usage
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
		Kind: domain.KindMiniMax,
		Windows: []domain.QuotaWindow{
			{ID: "interval", Name: "5-hour", RemainingFraction: 0.69, UsedFraction: 0.31, Remaining: &rem, Limit: &lim, Unit: "requests"},
			{ID: "weekly", Name: "Weekly", RemainingFraction: 0.87, UsedFraction: 0.13},
		},
	}

	over := BuildQuotaVM(snap, now, false)
	if len(over.Windows) != 2 {
		t.Fatalf("windows = %d", len(over.Windows))
	}
	if over.Windows[0].Secondary != "" {
		t.Error("overview must not show raw counts")
	}

	detail := BuildQuotaVM(snap, now, true)
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
	instances := []config.SourceInstance{
		inst("gpu1", "Main GPU", domain.KindAMDGPUWeb, true),
		inst("swap1", "swap", domain.KindLlamaSwap, false),
		inst("mm1", "MiniMax Personal", domain.KindMiniMax, true),
	}
	states := map[domain.InstanceID]domain.Runtime{
		"gpu1":  {Enabled: true, LastSuccessAt: ts(5 * time.Second), Snapshot: gpuSnap()},
		"swap1": {Enabled: false},
		"mm1":   {Enabled: true, LastError: &source.Error{Category: source.CatConnection, Detail: "refused"}},
	}

	vm := BuildOverview(instances, states, now)
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
	if gpu.Instance != "gpu1" || gpu.Kind != OverviewMetrics || gpu.Title != "Main GPU" || gpu.Status != "5s ago" {
		t.Errorf("gpu card = %+v", gpu)
	}
	if !gpu.Metrics.HasData {
		t.Error("gpu metrics should have data")
	}

	mm := vm.RemoteCards[0]
	// Kind comes from the configuration, so a quota card presents as quota
	// even before its first snapshot; the body reports no data.
	if mm.Kind != OverviewQuota || mm.Status != "Unavailable" || mm.Quota.HasData {
		t.Errorf("minimax card = %+v", mm)
	}
}

func TestBuildOverviewNothingEnabled(t *testing.T) {
	vm := BuildOverview(nil, map[domain.InstanceID]domain.Runtime{}, now)
	if !vm.NothingEnabled {
		t.Error("empty states must set NothingEnabled")
	}

	vm = BuildOverview(
		[]config.SourceInstance{inst("z1", "Z.AI", domain.KindZAI, true)},
		map[domain.InstanceID]domain.Runtime{"z1": {Enabled: false}},
		now,
	)
	if !vm.NothingEnabled {
		t.Error("all-disabled must set NothingEnabled")
	}
}

// Spec §10: two same-kind instances both appear, in configured order, and
// each carries its own instance identity and name; interleaved categories
// still group into Local/Remote without reordering within a category.
func TestBuildOverviewTwoSameKindAndConfiguredOrder(t *testing.T) {
	instances := []config.SourceInstance{
		inst("z1", "Z.AI work", domain.KindZAI, true),
		inst("gpu1", "GPU server", domain.KindAMDGPUWeb, true),
		inst("mm1", "MiniMax Personal", domain.KindMiniMax, true),
		inst("mm2", "MiniMax Side", domain.KindMiniMax, true),
	}
	states := map[domain.InstanceID]domain.Runtime{
		"z1":   {Enabled: true, LastSuccessAt: now},
		"gpu1": {Enabled: true, LastSuccessAt: now},
		"mm1":  {Enabled: true, LastSuccessAt: now},
		"mm2":  {Enabled: true, LastSuccessAt: now},
	}

	vm := BuildOverview(instances, states, now)
	if len(vm.LocalCards) != 1 || vm.LocalCards[0].Title != "GPU server" {
		t.Fatalf("local cards = %+v, want only GPU server", vm.LocalCards)
	}
	if len(vm.RemoteCards) != 3 {
		t.Fatalf("remote cards = %d, want 3", len(vm.RemoteCards))
	}
	want := []struct{ id, title string }{
		{"z1", "Z.AI work"},
		{"mm1", "MiniMax Personal"},
		{"mm2", "MiniMax Side"},
	}
	for i, w := range want {
		c := vm.RemoteCards[i]
		if c.Instance != domain.InstanceID(w.id) || c.Title != w.title {
			t.Errorf("remote[%d] = (%s, %q), want (%s, %q)", i, c.Instance, c.Title, w.id, w.title)
		}
	}

	remote := BuildRemoteTab(instances, states, now)
	if len(remote) != 3 {
		t.Fatalf("remote tab = %d cards, want 3", len(remote))
	}
	if remote[1].Instance != "mm1" || remote[2].Instance != "mm2" {
		t.Errorf("same-kind instances missing from the remote tab: %+v", remote)
	}
}

func TestBuildLocalAndRemoteTabs(t *testing.T) {
	instances := []config.SourceInstance{
		inst("swap1", "swap", domain.KindLlamaSwap, true),
		inst("z1", "Z.AI", domain.KindZAI, true),
	}
	states := map[domain.InstanceID]domain.Runtime{
		"swap1": {Enabled: true, LastSuccessAt: now, Snapshot: domain.LlamaSwapSnapshot{RunningModels: []domain.RunningModel{{ID: "m1"}}}},
		"z1":    {Enabled: true, LastSuccessAt: now, Snapshot: domain.QuotaSnapshot{Windows: []domain.QuotaWindow{{ID: "tokens-5h", Name: "5-hour", RemainingFraction: 0.58, UsedFraction: 0.42}}}},
	}

	local := BuildLocalTab(instances, states, now)
	if len(local) != 1 || local[0].Kind != KindSwap || local[0].Title != "swap" {
		t.Fatalf("local tab = %+v", local)
	}
	remote := BuildRemoteTab(instances, states, now)
	if len(remote) != 1 || remote[0].Kind != KindQuota || remote[0].Title != "Z.AI" {
		t.Fatalf("remote tab = %+v", remote)
	}
	if w := remote[0].Quota.Windows[0]; w.UsedText != "42% used" {
		t.Errorf("used = %q", w.UsedText)
	}

	if got := BuildLocalTab(nil, map[domain.InstanceID]domain.Runtime{}, now); len(got) != 0 {
		t.Errorf("empty local tab = %+v", got)
	}
	if got := BuildRemoteTab(nil, map[domain.InstanceID]domain.Runtime{}, now); len(got) != 0 {
		t.Errorf("empty remote tab = %+v", got)
	}
}

func TestFormatResetCountdownBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		delta time.Duration
		want  string
	}{
		{"42s", 42 * time.Second, "42s"},
		{"59m", 59 * time.Minute, "59m"},
		{"2h14m", 2*time.Hour + 14*time.Minute, "2h14m"},
		{"4d7h", 4*24*time.Hour + 7*time.Hour, "4d7h"},
		{"zero", 0, "now"},
		{"past", -1 * time.Minute, "now"},
	}
	for _, c := range cases {
		resetAt := now.Add(c.delta)
		if got := FormatResetCountdown(&resetAt, now); got != c.want {
			t.Errorf("FormatResetCountdown(%s) = %q, want %q", c.name, got, c.want)
		}
	}
	// nil ResetAt → empty.
	if got := FormatResetCountdown(nil, now); got != "" {
		t.Errorf("nil reset = %q, want empty", got)
	}
}

func TestBuildQuotaVMResetText(t *testing.T) {
	reset := now.Add(2*time.Hour + 14*time.Minute)
	snap := domain.QuotaSnapshot{
		Kind: domain.KindMiniMax,
		Windows: []domain.QuotaWindow{
			{ID: "interval", Name: "5-hour", RemainingFraction: 0.83, UsedFraction: 0.17, ResetAt: &reset},
			{ID: "weekly", Name: "Weekly", RemainingFraction: 0.50, UsedFraction: 0.50},
		},
	}

	vm := BuildQuotaVM(snap, now, true)
	if vm.Windows[0].ResetText != "2h14m" {
		t.Errorf("interval reset text = %q, want \"2h14m\"", vm.Windows[0].ResetText)
	}
	if vm.Windows[1].ResetText != "" {
		t.Errorf("weekly reset text = %q, want empty", vm.Windows[1].ResetText)
	}
	// UsedText unchanged.
	if vm.Windows[0].UsedText != "17% used" {
		t.Errorf("used text = %q", vm.Windows[0].UsedText)
	}
}

// The per-second ticker re-renders countdowns from the absolute ResetAt,
// so both quota view models must carry the instant forward (nil stays nil —
// the component then renders no countdown at all).
func TestQuotaVMsCarryResetAt(t *testing.T) {
	reset := now.Add(2 * time.Hour)
	snap := domain.QuotaSnapshot{
		Windows: []domain.QuotaWindow{
			{ID: "interval", Name: "5-hour", UsedFraction: 0.42, RemainingFraction: 0.58, ResetAt: &reset},
			{ID: "weekly", Name: "Weekly", UsedFraction: 0.14, RemainingFraction: 0.86},
		},
	}

	detail := BuildQuotaVM(snap, now, true)
	if got := detail.Windows[0].ResetAt; got == nil || !got.Equal(reset) {
		t.Errorf("detail ResetAt = %v, want %v", got, reset)
	}
	if got := detail.Windows[1].ResetAt; got != nil {
		t.Errorf("window without reset carries ResetAt %v, want nil", got)
	}

	over := BuildQuotaOverview(snap, now)
	// Headline selection keeps the 5h window (priority 0) first.
	if got := over.Windows[0].ResetAt; got == nil || !got.Equal(reset) {
		t.Errorf("overview ResetAt = %v, want %v", got, reset)
	}
}

func TestBuildMetricsOverview(t *testing.T) {
	snap := gpuSnap()
	vm := BuildMetricsOverview(snap)
	if !vm.HasData {
		t.Error("should have data")
	}
	if vm.GPU.Text != "GPU 74%" {
		t.Errorf("GPU text = %q", vm.GPU.Text)
	}
	if vm.GPU.Fraction != 0.745 {
		t.Errorf("GPU fraction = %v", vm.GPU.Fraction)
	}
	if vm.GPU.Level != MetricWarning {
		t.Errorf("GPU level = %v", vm.GPU.Level)
	}
	if vm.VRAM.Text != "VRAM 13.8 / 16.0 GiB" {
		t.Errorf("VRAM text = %q", vm.VRAM.Text)
	}
	if vm.Temp.Value != "63 °C" {
		t.Errorf("Temp value = %q", vm.Temp.Value)
	}

	// Sparse: only GPU util.
	sparse := domain.GPUSnapshot{GPUUtilizationPct: f(12)}
	vm = BuildMetricsOverview(sparse)
	if vm.GPU.Text == "" {
		t.Error("GPU text should be set")
	}
	if vm.VRAM.Text != "" {
		t.Error("VRAM text should be empty")
	}
	if vm.Temp.Value != "" {
		t.Error("Temp value should be empty")
	}

	// Empty: no data.
	vm = BuildMetricsOverview(domain.GPUSnapshot{})
	if vm.HasData {
		t.Error("empty snap should not have data")
	}
}

func TestBuildRuntimeOverview(t *testing.T) {
	// 0 models.
	vm := BuildRuntimeOverview(domain.LlamaSwapSnapshot{})
	if vm.Summary != "No models running" {
		t.Errorf("summary = %q", vm.Summary)
	}
	if !vm.HasData {
		t.Error("should have data even with 0 models")
	}

	// 1 model.
	vm = BuildRuntimeOverview(domain.LlamaSwapSnapshot{
		RunningModels: []domain.RunningModel{{ID: "m1", Name: "Qwen 3 14B"}},
	})
	if vm.Summary != "1 model running" {
		t.Errorf("summary = %q", vm.Summary)
	}
	if len(vm.Names) != 1 || vm.Names[0] != "Qwen 3 14B" {
		t.Errorf("names = %+v", vm.Names)
	}
	if vm.MoreCount != 0 {
		t.Errorf("more = %d", vm.MoreCount)
	}

	// 3 models: show 2 names, +1 more.
	vm = BuildRuntimeOverview(domain.LlamaSwapSnapshot{
		RunningModels: []domain.RunningModel{
			{ID: "m1", Name: "Qwen 3 14B"},
			{ID: "m2", Name: "Gemma 3 4B"},
			{ID: "m3"}, // no Name, falls back to ID
		},
	})
	if vm.Summary != "3 models running" {
		t.Errorf("summary = %q", vm.Summary)
	}
	if len(vm.Names) != 2 || vm.Names[1] != "Gemma 3 4B" {
		t.Errorf("names = %+v", vm.Names)
	}
	if vm.MoreCount != 1 {
		t.Errorf("more = %d", vm.MoreCount)
	}
}

func TestBuildQuotaOverview(t *testing.T) {
	reset := now.Add(2 * time.Hour)
	snap := domain.QuotaSnapshot{
		Windows: []domain.QuotaWindow{
			{ID: "interval", Name: "5-hour", UsedFraction: 0.42, RemainingFraction: 0.58, ResetAt: &reset},
			{ID: "weekly", Name: "Weekly", UsedFraction: 0.14, RemainingFraction: 0.86},
		},
	}

	vm := BuildQuotaOverview(snap, now)
	if !vm.HasData {
		t.Error("should have data")
	}
	if len(vm.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(vm.Windows))
	}
	// 5h should be first (priority 0).
	if vm.Windows[0].Label != "5h" {
		t.Errorf("label[0] = %q", vm.Windows[0].Label)
	}
	if vm.Windows[0].PctText != "42%" {
		t.Errorf("pct[0] = %q", vm.Windows[0].PctText)
	}
	if vm.Windows[0].ResetText != "2h0m" {
		t.Errorf("reset[0] = %q", vm.Windows[0].ResetText)
	}
	if vm.Windows[1].Label != "week" {
		t.Errorf("label[1] = %q", vm.Windows[1].Label)
	}

	// No windows.
	vm = BuildQuotaOverview(domain.QuotaSnapshot{}, now)
	if vm.HasData {
		t.Error("no windows should not have data")
	}
}

// Normalized Z.AI dual-credit regression (observed live): the 5-hour
// window has just reset (0% used, no ResetAt) while the weekly window is
// part-used with a reset days out. Both presentations must carry the
// windows as the domain adapter normalized them — counts and resets stay
// with their own window.
func TestZAINormalizedWindowsRender(t *testing.T) {
	weeklyReset := now.Add(72*time.Hour + 9*time.Hour)
	snap := domain.QuotaSnapshot{
		Kind: domain.KindZAI,
		Windows: []domain.QuotaWindow{
			{ID: "credits-5h", Name: "5-hour", UsedFraction: 0, RemainingFraction: 1},
			{ID: "credits-weekly", Name: "Weekly", UsedFraction: 0.66, RemainingFraction: 0.34, ResetAt: &weeklyReset},
		},
	}

	// Remote detail rows.
	detail := BuildQuotaVM(snap, now, true)
	if len(detail.Windows) != 2 {
		t.Fatalf("detail windows = %d, want 2", len(detail.Windows))
	}
	if detail.Windows[0].Name != "5-hour" || detail.Windows[0].UsedText != "0% used" {
		t.Errorf("detail 5h = %q/%q", detail.Windows[0].Name, detail.Windows[0].UsedText)
	}
	if detail.Windows[0].ResetText != "" {
		t.Errorf("detail 5h reset = %q, want empty (no reliable reset)", detail.Windows[0].ResetText)
	}
	if detail.Windows[1].Name != "Weekly" || detail.Windows[1].UsedText != "66% used" {
		t.Errorf("detail weekly = %q/%q", detail.Windows[1].Name, detail.Windows[1].UsedText)
	}
	if detail.Windows[1].ResetText != "3d9h" {
		t.Errorf("detail weekly reset = %q, want \"3d9h\"", detail.Windows[1].ResetText)
	}

	// Overview donuts.
	ov := BuildQuotaOverview(snap, now)
	if len(ov.Windows) != 2 {
		t.Fatalf("overview windows = %d, want 2", len(ov.Windows))
	}
	if ov.Windows[0].Label != "5h" || ov.Windows[0].PctText != "0%" {
		t.Errorf("overview 5h = %q/%q", ov.Windows[0].Label, ov.Windows[0].PctText)
	}
	if ov.Windows[0].ResetText != "" {
		t.Errorf("overview 5h reset = %q, want empty", ov.Windows[0].ResetText)
	}
	if ov.Windows[1].Label != "week" || ov.Windows[1].PctText != "66%" {
		t.Errorf("overview weekly = %q/%q", ov.Windows[1].Label, ov.Windows[1].PctText)
	}
	if ov.Windows[1].ResetText != "3d9h" {
		t.Errorf("overview weekly reset = %q, want \"3d9h\"", ov.Windows[1].ResetText)
	}
}

func TestShortLabelMapping(t *testing.T) {
	cases := []struct {
		id, name, want string
	}{
		{"interval", "5-hour", "5h"},
		{"tokens-5h", "5-hour", "5h"},
		{"credits-5h", "5-hour", "5h"},
		{"weekly", "Weekly", "week"},
		{"credits-weekly", "Weekly", "week"},
		{"mcp-monthly", "Monthly", "month"},
		{"unknown-id", "Custom Plan", "Custom Plan"},
	}
	for _, c := range cases {
		w := domain.QuotaWindow{ID: c.id, Name: c.name}
		if got := shortLabel(w); got != c.want {
			t.Errorf("shortLabel(%s) = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestSelectHeadlineWindows(t *testing.T) {
	windows := []domain.QuotaWindow{
		{ID: "mcp-monthly", Name: "Monthly"},
		{ID: "weekly", Name: "Weekly"},
		{ID: "interval", Name: "5-hour"},
	}

	sel := selectHeadlineWindows(windows)
	if len(sel) != 2 {
		t.Fatalf("selected = %d, want 2", len(sel))
	}
	if sel[0].ID != "interval" {
		t.Errorf("first = %q, want interval", sel[0].ID)
	}
	if sel[1].ID != "weekly" {
		t.Errorf("second = %q, want weekly", sel[1].ID)
	}

	// Only one window.
	sel = selectHeadlineWindows([]domain.QuotaWindow{
		{ID: "mcp-monthly", Name: "Monthly"},
	})
	if len(sel) != 1 || sel[0].ID != "mcp-monthly" {
		t.Errorf("single = %+v", sel)
	}
}

func TestOverviewPctTextNoUsed(t *testing.T) {
	if got := formatPctOnly(0.14); got != "14%" {
		t.Errorf("formatPctOnly(0.14) = %q, want \"14%%\"", got)
	}
	if got := formatPctOnly(0.0); got != "0%" {
		t.Errorf("formatPctOnly(0) = %q, want \"0%%\"", got)
	}
	if got := formatPctOnly(0.004); got != "<1%" {
		t.Errorf("formatPctOnly(0.004) = %q, want \"<1%%\"", got)
	}
	if got := formatPctOnly(1.0); got != "100%" {
		t.Errorf("formatPctOnly(1) = %q, want \"100%%\"", got)
	}
}

// orKey builds a full OpenRouter key-usage block (no limit).
func orKey() *domain.OpenRouterKeyUsage {
	return &domain.OpenRouterKeyUsage{
		UsageTotal:   41.08,
		UsageDaily:   0.42,
		UsageWeekly:  3.81,
		UsageMonthly: 12.47,
	}
}

func orLimit(cadence string, amount, remaining float64) *domain.OpenRouterLimit {
	return &domain.OpenRouterLimit{Cadence: cadence, Amount: amount, Remaining: remaining}
}

func orSnap(key *domain.OpenRouterKeyUsage, acct *domain.OpenRouterAccountCredits) domain.OpenRouterSnapshot {
	return domain.OpenRouterSnapshot{CollectedAt: now.Add(-5 * time.Second), Key: key, Account: acct}
}

func TestBuildSpendVMAPIOnlyDetail(t *testing.T) {
	vm := BuildSpendVM(orSnap(orKey(), nil), true)

	if !vm.HasData || len(vm.Account) != 0 {
		t.Fatalf("api-only vm = %+v", vm)
	}
	want := []struct{ label, text string }{
		{"Today", "$0.42"},
		{"Week", "$3.81"},
		{"Month", "$12.47"},
		{"Total", "$41.08"},
	}
	if len(vm.Usage) != len(want) {
		t.Fatalf("usage rows = %+v", vm.Usage)
	}
	for i, w := range want {
		if vm.Usage[i].Label != w.label || vm.Usage[i].Text != w.text {
			t.Errorf("usage[%d] = %q %q, want %q %q", i, vm.Usage[i].Label, vm.Usage[i].Text, w.label, w.text)
		}
		if vm.Usage[i].Sensitive {
			t.Errorf("usage row %q must not be sensitive", vm.Usage[i].Label)
		}
	}
	if vm.Limit != nil {
		t.Errorf("limit = %+v, want nil without a limit", vm.Limit)
	}
}

func TestBuildSpendVMOverviewOmitsTotal(t *testing.T) {
	vm := BuildSpendOverview(orSnap(orKey(), nil))
	if len(vm.Usage) != 3 || vm.Usage[0].Label != "Today" || vm.Usage[2].Label != "Month" {
		t.Fatalf("overview usage rows = %+v", vm.Usage)
	}
	if vm.Balance != nil {
		t.Errorf("balance = %+v, want nil with no account data", vm.Balance)
	}
}

func TestBuildSpendVMAccountRowsAreSensitive(t *testing.T) {
	acct := &domain.OpenRouterAccountCredits{TotalCredits: 105.89, TotalUsage: 32.47, Balance: 73.42}
	vm := BuildSpendVM(orSnap(nil, acct), true)

	if !vm.HasData || len(vm.Account) != 3 {
		t.Fatalf("account vm = %+v", vm)
	}
	want := []struct{ label, text string }{
		{"Balance", "$73.42"},
		{"Purchased credits", "$105.89"},
		{"Account usage", "$32.47"},
	}
	for i, w := range want {
		row := vm.Account[i]
		if row.Label != w.label || row.Text != w.text {
			t.Errorf("account[%d] = %q %q, want %q %q", i, row.Label, row.Text, w.label, w.text)
		}
		if !row.Sensitive {
			t.Errorf("account row %q must be marked sensitive", row.Label)
		}
	}

	ov := BuildSpendOverview(orSnap(nil, acct))
	if ov.Balance == nil || ov.Balance.Text != "$73.42" || !ov.Balance.Sensitive {
		t.Errorf("overview balance = %+v", ov.Balance)
	}
	if len(ov.Usage) != 0 {
		t.Errorf("management-only overview must not show usage rows: %+v", ov.Usage)
	}
}

func TestBuildSpendVMLimitLabelsAndFractions(t *testing.T) {
	cases := []struct {
		cadence     string
		label       string
		used        float64 // of $25
		remaining   float64
		wantPct     string
		wantRawText string
	}{
		{"monthly", "Monthly limit", 12.47, 12.53, "50% used", "$12.47 / $25.00"},
		{"weekly", "Weekly limit", 1.25, 23.75, "5% used", "$1.25 / $25.00"},
		{"daily", "Daily limit", 0, 25, "0% used", "$0.00 / $25.00"},
		{"", "Key limit", 20, 5, "80% used", "$20.00 / $25.00"},
		{"hourly", "Key limit", 24, 1, "96% used", "$24.00 / $25.00"},
		// Cadences arrive lowercased from the adapter; anything else is
		// unknown and stays neutral.
		{"MONTHLY", "Key limit", 5, 20, "20% used", "$5.00 / $25.00"},
	}
	for _, c := range cases {
		vm := BuildSpendVM(orSnap(&domain.OpenRouterKeyUsage{
			UsageDaily: 1, UsageWeekly: 1, UsageMonthly: 1, UsageTotal: 1,
			Limit: orLimit(c.cadence, 25, c.remaining),
		}, nil), true)
		l := vm.Limit
		if l == nil {
			t.Fatalf("%s: limit dropped", c.cadence)
		}
		if l.Label != c.label {
			t.Errorf("cadence %q label = %q, want %q", c.cadence, l.Label, c.label)
		}
		if l.UsedText != c.wantPct {
			t.Errorf("cadence %q used text = %q, want %q", c.cadence, l.UsedText, c.wantPct)
		}
		if l.RawText != c.wantRawText {
			t.Errorf("cadence %q raw text = %q, want %q", c.cadence, l.RawText, c.wantRawText)
		}
	}

	// Overview keeps the bar but no raw amount line.
	ov := BuildSpendOverview(orSnap(&domain.OpenRouterKeyUsage{
		UsageDaily: 1, UsageWeekly: 1, UsageMonthly: 1,
		Limit: orLimit("monthly", 25, 12.53),
	}, nil))
	if ov.Limit == nil || ov.Limit.RawText != "" || ov.Limit.Label != "Monthly limit" {
		t.Errorf("overview limit = %+v", ov.Limit)
	}
}

func TestBuildSpendVMOmitsUnusableLimit(t *testing.T) {
	vm := BuildSpendVM(orSnap(&domain.OpenRouterKeyUsage{
		UsageDaily: 1, UsageWeekly: 1, UsageMonthly: 1,
		Limit: orLimit("monthly", -3, 0),
	}, nil), true)
	if vm.Limit != nil || !vm.HasData || len(vm.Usage) != 4 {
		t.Errorf("non-positive limit must be omitted but keep usage: %+v", vm)
	}

	vm = BuildSpendVM(orSnap(nil, nil), true)
	if vm.HasData || vm.Limit != nil || len(vm.Usage) != 0 || len(vm.Account) != 0 {
		t.Errorf("empty snapshot = %+v, want no data", vm)
	}
}

func TestBuildSpendVMTinyLimitUsageReadsUnderOnePercent(t *testing.T) {
	used := FormatUsed(domain.Clamp01(0.02 / 25))
	if used != "<1% used" {
		t.Fatalf("tiny usage text = %q, want \"<1%% used\"", used)
	}
	vm := BuildSpendVM(orSnap(&domain.OpenRouterKeyUsage{
		UsageDaily: 0.02, UsageWeekly: 0.02, UsageMonthly: 0.02,
		Limit: orLimit("daily", 25, 24.98),
	}, nil), true)
	if vm.Limit.UsedText != "<1% used" {
		t.Errorf("limit used text = %q, want \"<1%% used\"", vm.Limit.UsedText)
	}
}

func TestCardKindSpend(t *testing.T) {
	instances := []config.SourceInstance{
		inst("or1", "OpenRouter", domain.KindOpenRouter, true),
		inst("mm1", "MiniMax Personal", domain.KindMiniMax, true),
	}
	states := map[domain.InstanceID]domain.Runtime{
		"or1": {Enabled: true, LastSuccessAt: now, Snapshot: orSnap(orKey(), &domain.OpenRouterAccountCredits{Balance: 73.42})},
		"mm1": {Enabled: true},
	}

	tab := BuildRemoteTab(instances, states, now)
	if len(tab) != 2 {
		t.Fatalf("remote tab = %d cards", len(tab))
	}
	card := tab[0]
	if card.Kind != KindSpend || card.Title != "OpenRouter" {
		t.Fatalf("card kind = %q title = %q", card.Kind, card.Title)
	}
	if len(card.Spend.Usage) != 4 || len(card.Spend.Account) != 3 {
		t.Errorf("detail body rows = %+v / %+v", card.Spend.Usage, card.Spend.Account)
	}

	ov := BuildOverview(instances, states, now)
	spendCard := ov.RemoteCards[0]
	if spendCard.Kind != OverviewSpend || !spendCard.Spend.HasData {
		t.Fatalf("overview card = %+v", spendCard)
	}
	if len(spendCard.Spend.Usage) != 3 || spendCard.Spend.Balance == nil {
		t.Errorf("overview spend body = %+v", spendCard.Spend)
	}

	// Quota kinds stay quota.
	mm := ov.RemoteCards[1]
	if mm.Kind != OverviewQuota || mm.Spend.HasData {
		t.Errorf("minimax card = %+v", mm)
	}
}

func TestFormatMoney(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{0, "$0.00"},
		{0.04, "$0.04"},
		{12.47, "$12.47"},
		{41.084, "$41.08"},
		{1000, "$1000.00"},
		{0.0042, "$0.0042"},
		{0.03, "$0.03"},
		{0.0099, "$0.0099"},
		{0.009, "$0.009"},
		{-12.47, "-$12.47"},
		{-0.0042, "-$0.0042"},
		{-0.000001, "$0.00"}, // rounds away below the smallest rendered scale; no "-$0.00"
		{math.NaN(), "—"},
		{math.Inf(1), "—"},
	}
	for _, c := range cases {
		if got := FormatMoney(c.v); got != c.want {
			t.Errorf("FormatMoney(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}
