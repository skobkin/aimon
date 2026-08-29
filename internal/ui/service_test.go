package ui

import (
	"math"
	"testing"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
)

// hindsightSnap is a fully populated deployment: every optional surface
// observed, backlog gauges exposed.
func hindsightSnap() domain.HindsightSnapshot {
	write := now.Add(-3 * time.Minute)
	doc := now.Add(-6 * time.Minute)
	return domain.HindsightSnapshot{
		CollectedAt: now.Add(-4 * time.Second),
		Service: domain.HindsightService{
			Status:        "healthy",
			Database:      "connected",
			Version:       "0.9.2",
			Uptime:        2*24*time.Hour + 7*time.Hour,
			DBAcquireMS:   f(0.4),
			DBPoolWaiting: f(0),
		},
		Memory: &domain.HindsightMemory{Banks: 27, Facts: 18400, LastWriteAt: &write, LastDocumentAt: &doc},
		Activity: domain.HindsightActivity{
			RetainPerMin:       f(2),
			RecallPerMin:       f(18),
			ReflectPerMin:      f(1),
			ConsolidatePerMin:  f(0.4),
			LLMCallsPerMin:     f(4),
			InputTokensPerMin:  f(12400),
			OutputTokensPerMin: f(1800),
			HTTP5xxPercent:     f(0),
		},
		Runtime: domain.HindsightRuntime{
			DBPoolSize:     f(10),
			DBPoolIdle:     f(8),
			DBPoolMax:      f(10),
			DBPoolWaiting:  f(7), // fallback; the /health value 0 above wins
			HTTPInProgress: f(1),
		},
		Queue: &domain.HindsightQueue{
			Pending:              f(3),
			Processing:           f(1),
			Failed:               f(0),
			ConsolidationBacklog: f(8),
			ConsolidationFailed:  f(0),
		},
	}
}

func findRow(t *testing.T, rows []MetricRowVM, label string) MetricRowVM {
	t.Helper()
	for _, r := range rows {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no %q row in %+v", label, rows)
	return MetricRowVM{}
}

func findAgeRow(t *testing.T, rows []AgeRowVM, label string) AgeRowVM {
	t.Helper()
	for _, r := range rows {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no %q row in %+v", label, rows)
	return AgeRowVM{}
}

// Every registered kind must map onto a card body and an overview body;
// a new kind added to the registry without presentation silently renders
// as "no data" otherwise.
func TestCardAndOverviewKindsCoverEveryRegisteredKind(t *testing.T) {
	for _, def := range registry.All() {
		if cardKind(def.Kind) == KindNone {
			t.Errorf("%s has no card kind mapping", def.Kind)
		}
		if overviewKind(def.Kind) == OverviewKind("") {
			t.Errorf("%s has no overview kind mapping", def.Kind)
		}
	}
	if cardKind(domain.SourceKind("nope")) != KindNone {
		t.Error("unknown kinds must stay KindNone")
	}
	if cardKind(domain.KindHindsight) != KindService || overviewKind(domain.KindHindsight) != OverviewService {
		t.Error("hindsight must use the service presentation")
	}
}

func TestBuildServiceVMFull(t *testing.T) {
	vm := BuildServiceVM(hindsightSnap(), now)
	if !vm.HasData {
		t.Fatal("a landed snapshot has data")
	}

	cases := []struct {
		label string
		value string
		level MetricLevel
	}{
		{"Status", "Healthy", MetricGood},
		{"Version", "0.9.2", MetricNeutral},
		{"Uptime", "2d 7h", MetricNeutral},
		{"Database", "connected", MetricGood},
		{"DB acquire", "0.4 ms", MetricNeutral},
		{"DB pool active", "2 / 10", MetricNeutral},
		{"DB waiting", "0", MetricNeutral}, // /health value 0 preferred over gauge 7
		{"HTTP in progress", "1", MetricNeutral},
		{"HTTP 5xx", "0.0%", MetricNeutral},
		{"Pending", "3", MetricNeutral},
		{"Processing", "1", MetricNeutral},
		{"Failed", "0", MetricNeutral},
		{"Consolidation backlog", "8", MetricNeutral},
		{"Consolidation failed", "0", MetricNeutral},
	}
	for _, c := range cases {
		row := findRow(t, vm.allMetricRows(), c.label)
		if row.Value != c.value || row.Level != c.level {
			t.Errorf("%s = (%q, %v), want (%q, %v)", c.label, row.Value, row.Level, c.value, c.level)
		}
	}

	rates := map[string]string{
		"Retain": "2/m", "Recall": "18/m", "Reflect": "1/m", "Consolidation": "0.4/m",
		"LLM calls": "4/m", "Input tokens": "12.4k/m", "Output tokens": "1800/m",
	}
	for label, want := range rates {
		if got := findRow(t, vm.Activity, label).Value; got != want {
			t.Errorf("Activity %s = %q, want %q", label, got, want)
		}
	}

	if got := findAgeRow(t, vm.Memory, "Banks").Value; got != "27" {
		t.Errorf("Banks = %q, want 27", got)
	}
	if got := findAgeRow(t, vm.Memory, "Facts").Value; got != "18.4k" {
		t.Errorf("Facts = %q, want 18.4k", got)
	}
	lastWrite := findAgeRow(t, vm.Memory, "Last write")
	if lastWrite.Value != "3m ago" || lastWrite.At == nil || !lastWrite.At.Equal(now.Add(-3*time.Minute)) {
		t.Errorf("Last write = (%q, %v), want 3m ago at %v", lastWrite.Value, lastWrite.At, now.Add(-3*time.Minute))
	}
	if got := findAgeRow(t, vm.Memory, "Last document"); got.Value != "6m ago" || got.At == nil {
		t.Errorf("Last document = (%q, %v), want 6m ago", got.Value, got.At)
	}
}

// allMetricRows concatenates the metric sections so label lookups do not
// care which section a row lives in.
func (vm ServiceVM) allMetricRows() []MetricRowVM {
	return append(append(append(append([]MetricRowVM{}, vm.Service...), vm.Activity...), vm.Queue...), vm.Runtime...)
}

// Unobserved surfaces disappear instead of rendering placeholder rows, and
// the DB Waiting fallback engages only when /health carried no value.
func TestBuildServiceVMConditional(t *testing.T) {
	snap := domain.HindsightSnapshot{
		CollectedAt: now,
		Service:     domain.HindsightService{Status: "healthy"},
		// No version/uptime/database/diagnostics, no gauges, no backlog,
		// first sample: every rate unavailable.
	}
	vm := BuildServiceVM(snap, now)

	if len(vm.Service) != 1 || vm.Service[0].Label != "Status" {
		t.Errorf("Service rows = %+v, want only Status", vm.Service)
	}
	if vm.Queue != nil {
		t.Errorf("Queue = %+v, want nil without backlog families", vm.Queue)
	}
	if vm.Runtime != nil {
		t.Errorf("Runtime = %+v, want nil without gauges", vm.Runtime)
	}
	if vm.Memory != nil {
		t.Errorf("Memory rows = %+v, want none before the slow data lands", vm.Memory)
	}
	for _, row := range vm.Activity {
		if row.Value != "—" {
			t.Errorf("first-sample %s = %q, want —", row.Label, row.Value)
		}
	}

	// A landed aggregate of a valid empty deployment renders as real zeros,
	// never as absence.
	snap.Memory = &domain.HindsightMemory{}
	vm = BuildServiceVM(snap, now)
	if len(vm.Memory) != 4 {
		t.Fatalf("Memory rows = %+v, want the four aggregate rows", vm.Memory)
	}
	for _, label := range []string{"Banks", "Facts"} {
		if row := findAgeRow(t, vm.Memory, label); row.Value != "0" {
			t.Errorf("%s = %q, want 0", label, row.Value)
		}
	}
	for _, label := range []string{"Last write", "Last document"} {
		if row := findAgeRow(t, vm.Memory, label); row.Value != "—" || row.At != nil {
			t.Errorf("%s = (%q, %v), want the unavailable dash", label, row.Value, row.At)
		}
	}

	// Gauge fallback: with no /health diagnostic the Prometheus waiting
	// gauge is shown and a positive value warns.
	snap.Runtime.DBPoolWaiting = f(2)
	vm = BuildServiceVM(snap, now)
	row := findRow(t, vm.Runtime, "DB waiting")
	if row.Value != "2" || row.Level != MetricWarning {
		t.Errorf("DB waiting = (%q, %v), want (2, warning)", row.Value, row.Level)
	}

	// Failed backlog > 0 reads as critical.
	snap.Queue = &domain.HindsightQueue{Failed: f(2)}
	vm = BuildServiceVM(snap, now)
	if row := findRow(t, vm.Queue, "Failed"); row.Level != MetricCritical {
		t.Errorf("Failed level = %v, want critical", row.Level)
	}

	// A non-healthy status stays uncolored rather than inventing a
	// threshold.
	snap.Service.Status = "degraded"
	vm = BuildServiceVM(snap, now)
	if row := findRow(t, vm.Service, "Status"); row.Value != "Degraded" || row.Level != MetricNeutral {
		t.Errorf("Status = (%q, %v), want (Degraded, neutral)", row.Value, row.Level)
	}
}

func TestBuildServiceOverviewVM(t *testing.T) {
	vm := BuildServiceOverviewVM(hindsightSnap())
	if !vm.HasData {
		t.Fatal("a landed snapshot has data")
	}
	if vm.Summary != "Healthy · v0.9.2" {
		t.Errorf("Summary = %q", vm.Summary)
	}
	want := []struct{ label, value string }{
		{"Banks", "27"}, {"Facts", "18.4k"},
		{"Recall", "18/m"}, {"LLM calls", "4/m"},
		{"Pending", "3"}, {"Failed", "0"},
	}
	if len(vm.Rows) != len(want) {
		t.Fatalf("Rows = %+v, want %d pairs", vm.Rows, len(want))
	}
	for i, w := range want {
		if vm.Rows[i].Label != w.label || vm.Rows[i].Value != w.value {
			t.Errorf("row[%d] = (%q, %q), want (%q, %q)", i, vm.Rows[i].Label, vm.Rows[i].Value, w.label, w.value)
		}
	}
}

// Overview priority: a partially available family never suppresses a
// useful fallback and never renders an empty cell.
func TestBuildServiceOverviewVMPriority(t *testing.T) {
	snap := hindsightSnap()

	// Consolidation-only backlog: the consolidation pair is used.
	snap.Queue = &domain.HindsightQueue{ConsolidationBacklog: f(8)}
	vm := BuildServiceOverviewVM(snap)
	if vm.Rows[4].Label != "Consol. backlog" || vm.Rows[4].Value != "8" {
		t.Errorf("row[4] = %+v, want consolidation backlog 8", vm.Rows[4])
	}
	if vm.Rows[5].Label != "Consol. failed" || vm.Rows[5].Value != "—" {
		t.Errorf("row[5] = %+v, want the unavailable dash", vm.Rows[5])
	}

	// No backlog at all: DB pressure, preferring the /health waiting value.
	snap.Queue = nil
	vm = BuildServiceOverviewVM(snap)
	if vm.Rows[4].Label != "DB active" || vm.Rows[4].Value != "2 / 10" {
		t.Errorf("row[4] = %+v, want DB active 2 / 10", vm.Rows[4])
	}
	if vm.Rows[5].Label != "Waiting" || vm.Rows[5].Value != "0" {
		t.Errorf("row[5] = %+v, want the /health waiting value 0", vm.Rows[5])
	}

	// Nothing measurable: the third row disappears entirely.
	snap.Runtime = domain.HindsightRuntime{}
	snap.Service.DBPoolWaiting = nil
	vm = BuildServiceOverviewVM(snap)
	if len(vm.Rows) != 4 {
		t.Errorf("Rows = %+v, want only the memory and rate pairs", vm.Rows)
	}

	// First sample: rates read as unavailable, never 0/m; memory that has
	// not landed yet produces no fabricated zeros.
	empty := domain.HindsightSnapshot{CollectedAt: now, Service: domain.HindsightService{Status: "loading"}}
	vm = BuildServiceOverviewVM(empty)
	if vm.Summary != "Loading" {
		t.Errorf("Summary = %q, want Loading", vm.Summary)
	}
	if len(vm.Rows) != 2 {
		t.Fatalf("Rows = %+v, want just the rate pair", vm.Rows)
	}
	if vm.Rows[0].Value != "—" || vm.Rows[1].Value != "—" {
		t.Errorf("first-sample rates = %+v, want —", vm.Rows)
	}

	// A landed aggregate of a valid empty deployment shows the real zeros:
	// availability is the landed pointer, not a non-zero count.
	empty.Memory = &domain.HindsightMemory{}
	vm = BuildServiceOverviewVM(empty)
	if len(vm.Rows) != 4 {
		t.Fatalf("Rows = %+v, want the memory pair in front of the rates", vm.Rows)
	}
	if vm.Rows[0].Label != "Banks" || vm.Rows[0].Value != "0" {
		t.Errorf("row[0] = %+v, want Banks 0", vm.Rows[0])
	}
	if vm.Rows[1].Label != "Facts" || vm.Rows[1].Value != "0" {
		t.Errorf("row[1] = %+v, want Facts 0", vm.Rows[1])
	}
}

func TestBuildTabsWithHindsightInstance(t *testing.T) {
	instance := inst("hs-1", "Hindsight", domain.KindHindsight, true)
	states := map[domain.InstanceID]domain.Runtime{
		instance.ID: {Enabled: true, Snapshot: hindsightSnap(), LastSuccessAt: now.Add(-4 * time.Second)},
	}

	local := BuildLocalTab([]config.SourceInstance{instance}, states, now)
	if len(local) != 1 {
		t.Fatalf("local tab = %d cards, want 1", len(local))
	}
	if local[0].Kind != KindService || !local[0].Service.HasData {
		t.Errorf("local card = kind %q hasData %v", local[0].Kind, local[0].Service.HasData)
	}

	overview := BuildOverview([]config.SourceInstance{instance}, states, now)
	if len(overview.LocalCards) != 1 {
		t.Fatalf("overview = %d local cards, want 1", len(overview.LocalCards))
	}
	card := overview.LocalCards[0]
	if card.Kind != OverviewService || card.Service.Summary != "Healthy · v0.9.2" {
		t.Errorf("overview card = kind %q summary %q", card.Kind, card.Service.Summary)
	}
}

func TestServiceFormatting(t *testing.T) {
	rates := []struct {
		in   *float64
		want string
	}{
		{nil, "—"},
		{f(0), "0/m"},
		{f(0.05), "<0.1/m"},
		{f(0.4), "0.4/m"},
		{f(9.96), "10.0/m"},
		{f(18), "18/m"},
		{f(1800), "1800/m"},
		{f(12400), "12.4k/m"},
	}
	for _, c := range rates {
		if got := FormatRatePerMin(c.in); got != c.want {
			t.Errorf("FormatRatePerMin(%v) = %q, want %q", c.in, got, c.want)
		}
	}

	if got := FormatPct1(nil); got != "—" {
		t.Errorf("FormatPct1(nil) = %q, want —", got)
	}
	if got := FormatPct1(f(1.0 / 11.0 * 100)); got != "9.1%" {
		t.Errorf("FormatPct1 = %q, want 9.1%%", got)
	}

	uptimes := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{42 * time.Second, "42s"},
		{3 * time.Minute, "3m"},
		{2*time.Hour + 5*time.Minute, "2h 5m"},
		{2*24*time.Hour + 7*time.Hour, "2d 7h"},
	}
	for _, c := range uptimes {
		if got := FormatUptime(c.in); got != c.want {
			t.Errorf("FormatUptime(%v) = %q, want %q", c.in, got, c.want)
		}
	}

	if got := FormatAgePtr(nil, now); got != "—" {
		t.Errorf("FormatAgePtr(nil) = %q, want —", got)
	}
	past := now.Add(-120 * time.Second)
	if got := FormatAgePtr(&past, now); got != "2m ago" {
		t.Errorf("FormatAgePtr = %q, want 2m ago", got)
	}
}

func TestDBPoolActive(t *testing.T) {
	active, poolMax, ok := dbPoolActive(f(10), f(8), f(10))
	if !ok || active != 2 || poolMax != 10 {
		t.Errorf("dbPoolActive(10, 8, 10) = (%v, %v, %v), want (2, 10, true)", active, poolMax, ok)
	}

	// Idle above size floors at zero; a missing max returns NaN so the
	// caller renders the active count alone.
	active, poolMax, ok = dbPoolActive(f(10), f(12), nil)
	if !ok || active != 0 || !math.IsNaN(poolMax) {
		t.Errorf("dbPoolActive(10, 12, nil) = (%v, %v, %v), want (0, NaN, true)", active, poolMax, ok)
	}

	if _, _, ok := dbPoolActive(nil, f(1), nil); ok {
		t.Error("half-present pool pair must be unusable")
	}
}
