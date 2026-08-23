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

func TestFormatRemainingRounding(t *testing.T) {
	cases := []struct {
		frac float64
		want string
	}{
		{0.28, "28% remaining"},
		{0.586, "59% remaining"}, // rounds to nearest
		{0.0, "0% remaining"},
		{1.0, "100% remaining"},
		{-0.5, "0% remaining"}, // clamped
		{1.5, "100% remaining"},
	}
	for _, c := range cases {
		if got := FormatRemaining(c.frac); got != c.want {
			t.Errorf("FormatRemaining(%v) = %q, want %q", c.frac, got, c.want)
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
		{"GPU util", "74%"},
		{"VRAM", "13.8 / 16.0 GiB"},
		{"Temp", "63 °C"},
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

	// A null sensor becomes an em dash, never a fake zero.
	empty := domain.GPUSnapshot{}
	vm = BuildGPUVM(empty, true)
	for _, r := range vm.Detail {
		if r.Value != "—" {
			t.Errorf("null sensor %s = %q, want em dash", r.Label, r.Value)
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
			{ID: "interval", Name: "5-hour", RemainingFraction: 0.69, Remaining: &rem, Limit: &lim, Unit: "requests"},
			{ID: "weekly", Name: "Weekly", RemainingFraction: 0.87},
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
	if detail.Windows[0].Secondary != "690 / 1000 requests" {
		t.Errorf("secondary = %q", detail.Windows[0].Secondary)
	}
	if detail.Windows[1].Secondary != "" {
		t.Errorf("percent-only window secondary = %q, want empty", detail.Windows[1].Secondary)
	}
	if detail.Windows[0].RemainingText != "69% remaining" {
		t.Errorf("remaining text = %q", detail.Windows[0].RemainingText)
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
		domain.SourceZAI:       {Enabled: true, LastSuccessAt: now, Snapshot: domain.QuotaSnapshot{Windows: []domain.QuotaWindow{{ID: "tokens-5h", Name: "Token usage · 5 hour", RemainingFraction: 0.58}}}},
	}

	local := BuildLocalTab(states, now)
	if len(local) != 1 || local[0].Kind != KindSwap {
		t.Fatalf("local tab = %+v", local)
	}
	remote := BuildRemoteTab(states, now)
	if len(remote) != 1 || remote[0].Kind != KindQuota {
		t.Fatalf("remote tab = %+v", remote)
	}
	if w := remote[0].Quota.Windows[0]; w.RemainingText != "58% remaining" {
		t.Errorf("remaining = %q", w.RemainingText)
	}

	if got := BuildLocalTab(map[domain.SourceID]domain.Runtime{}, now); len(got) != 0 {
		t.Errorf("empty local tab = %+v", got)
	}
	if got := BuildRemoteTab(map[domain.SourceID]domain.Runtime{}, now); len(got) != 0 {
		t.Errorf("empty remote tab = %+v", got)
	}
}
