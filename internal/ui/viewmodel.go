// Package ui builds the presentation layer. View models are pure data
// derived from domain runtimes — no Fyne objects — so all formatting and
// state mapping is unit-testable without a display. The Fyne-facing code
// consumes these VMs.
package ui

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/pace"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// CardKind discriminates which body a card renders.
type CardKind string

// Card body kinds; KindNone means the source has no data yet.
const (
	KindNone    CardKind = ""
	KindGPU     CardKind = "gpu"
	KindSwap    CardKind = "swap"
	KindQuota   CardKind = "quota"
	KindSpend   CardKind = "spend"
	KindService CardKind = "service"
)

// MetricLevel classifies a metric value by load for coloring. The zero
// value is MetricNeutral: no coloring (unleveled rows and placeholders).
// The names pair with the threshold color constants.
type MetricLevel int

// Metric load levels; Neutral means "render like plain text".
const (
	MetricNeutral MetricLevel = iota
	MetricGood
	MetricWarning
	MetricLow
	MetricCritical
)

// MetricRowVM is one label/value line in a card body.
type MetricRowVM struct {
	Label string
	Value string
	Level MetricLevel
}

// GPUVM carries the metric rows for one GPU snapshot. Overview holds the
// three headline metrics (missing sensors are omitted); Detail holds every
// row with "—" placeholders for missing sensors.
type GPUVM struct {
	Overview []MetricRowVM
	Detail   []MetricRowVM
}

// SwapVM carries the running-model lines. Overview caps at three lines and
// reports the overflow in MoreCount; Detail carries all lines.
type SwapVM struct {
	ModelLines []string
	MoreCount  int
}

// QuotaWindowVM is one quota window row. The display is used-oriented:
// Used drives the bar fill and the "NN% used" label, while Remaining
// still drives the threshold color (how much quota is left).
type QuotaWindowVM struct {
	Name      string
	UsedText  string
	ResetText string // compact countdown, e.g. "2h14m"; empty when unknown
	// ResetAt is the absolute reset instant ResetText was derived from.
	// Carried so a rendered row can refresh its countdown as time passes
	// without a tree rebuild; nil means no countdown.
	ResetAt   *time.Time
	Secondary string
	Used      float64
	Remaining float64
	// Pace carries the derived speed/projection for this window (S7); nil
	// when the tracker has no estimate for it (non-pace kinds, or no data
	// yet) — the row then renders exactly as before.
	Pace *PaceVM
}

// QuotaVM carries the window rows of one quota snapshot. Account holds the
// optional plan/credit metadata rows of a subscription kind (currently the
// ChatGPT Codex detail card); it is nil for the API-quota kinds, whose
// rendering stays untouched.
type QuotaVM struct {
	Windows []QuotaWindowVM
	Account []MetricRowVM
}

// MoneyMetricVM is one labeled money row. Sensitive marks account-level
// monetary values that the global privacy setting conceals; per-key usage
// and limit rows are never sensitive — they are what the user monitors.
type MoneyMetricVM struct {
	Label     string
	Text      string
	Sensitive bool
}

// SpendLimitVM carries the single OpenRouter key-limit bar. Used drives
// the fill and the "NN% used" label; Remaining still drives the threshold
// color, mirroring QuotaWindowVM. RawText ("$12.47 / $25.00") is a detail
// tab extra.
type SpendLimitVM struct {
	Label     string // "Daily limit" / "Weekly limit" / "Monthly limit" / "Key limit"
	UsedText  string
	Used      float64
	Remaining float64
	RawText   string
}

// SpendVM carries an OpenRouter card's rows. Usage holds Today/Week/Month
// (plus Total in detail); Account holds the Management-key rows and is
// empty without that capability; Limit is nil when no usable key limit
// was reported (or there is no key data at all).
type SpendVM struct {
	Usage   []MoneyMetricVM
	Account []MoneyMetricVM
	Limit   *SpendLimitVM
	// Pace carries the derived spend speed (S7); nil without a spend
	// estimate. Never projectable — OpenRouter has no reset time.
	Pace    *PaceVM
	HasData bool
}

// SpendOverviewVM is the compact overview variant: usage rows without the
// Total line, plus a Balance row pointer when account data exists.
type SpendOverviewVM struct {
	Usage   []MoneyMetricVM
	Limit   *SpendLimitVM
	Balance *MoneyMetricVM
	HasData bool
}

// AgeRowVM is one row whose text is a relative age ("3m ago") that is
// re-derived as the clock advances. At is the absolute instant the age was
// measured from; nil renders the unavailable dash and never ticks. Value
// carries the text derived at build time (the QuotaWindowVM.ResetAt
// pattern).
type AgeRowVM struct {
	Label string
	Value string
	At    *time.Time
	Level MetricLevel
}

// ServiceVM carries one Hindsight deployment's sectioned detail rows
// (S5_UI_UX §5). Every section is conditional: a section is nil when
// nothing in it was observed, so absent optional groups disappear instead
// of showing placeholder lines. Memory rows age in place (last
// write/document); the other sections are snapshot-stable text.
type ServiceVM struct {
	Service  []MetricRowVM
	Memory   []AgeRowVM
	Activity []MetricRowVM
	Queue    []MetricRowVM // nil unless the deployment exposed backlog metrics
	Runtime  []MetricRowVM
	HasData  bool
}

// ServiceOverviewVM is the compact overview variant: a bold summary line
// plus at most three label/value pairs rendered two per row (S5_UI_UX §3).
// A pair is emitted only when its own value exists — a missing optional
// backlog family never yields an empty cell or a fabricated zero.
type ServiceOverviewVM struct {
	Summary string // "Healthy · v0.9.2"
	Rows    []MetricRowVM
	HasData bool
}

// OverviewKind discriminates Overview card presentation.
type OverviewKind string

// Overview card kinds; zero value means no data yet.
const (
	OverviewMetrics OverviewKind = "metrics"
	OverviewRuntime OverviewKind = "runtime"
	OverviewQuota   OverviewKind = "quota"
	OverviewSpend   OverviewKind = "spend"
	OverviewService OverviewKind = "service"
)

// MetricBarVM is the pure data for one labeled usage bar.
// Presence is determined by Text being non-empty. Fraction 0 is a valid
// zero-usage metric and renders as an empty track with its text.
type MetricBarVM struct {
	Text     string
	Fraction float64 // clamped [0,1]; 0 = empty track (valid)
	Level    MetricLevel
}

// MetricsOverviewVM carries compact metric bar data for amdgputop-web.
type MetricsOverviewVM struct {
	GPU     MetricBarVM // zero Text = omitted
	VRAM    MetricBarVM // zero Text = omitted
	Temp    MetricRowVM // zero Value = omitted
	HasData bool
}

// RuntimeOverviewVM carries compact running-model summary for llama-swap.
type RuntimeOverviewVM struct {
	Summary   string   // "2 models running", "No models running"
	Names     []string // first two model names
	MoreCount int      // 0 or N
	HasData   bool
}

// OverviewDonutVM is the pure data for one quota donut gauge.
type OverviewDonutVM struct {
	Label     string  // short label: "5h", "week", "month"
	PctText   string  // "14%"
	Used      float64 // arc fraction
	Remaining float64 // drives threshold color
	ResetText string  // countdown below donut
	// ResetAt is the absolute instant ResetText was derived from, so the
	// rendered countdown line can age in place; nil means no countdown.
	ResetAt *time.Time
	// Pace carries the derived projection for this window (S7); the
	// Overview renders only the projection bar, never the speed text.
	Pace *PaceVM
}

// QuotaOverviewVM carries up to two headline quota donuts.
type QuotaOverviewVM struct {
	Windows []OverviewDonutVM // 0, 1, or 2
	HasData bool
}

// OverviewCardVM is one card in the Overview tab.
type OverviewCardVM struct {
	Instance domain.InstanceID
	Kind     OverviewKind
	Title    string
	Status   string

	Metrics MetricsOverviewVM
	Runtime RuntimeOverviewVM
	Quota   QuotaOverviewVM
	Spend   SpendOverviewVM
	Service ServiceOverviewVM
}

// CardVM is one source card. Status is the per-source freshness label
// ("5s ago", "1m ago · stale", "Unavailable", "Loading…"); Footer carries
// the last refresh failure for Stale/Unavailable cards.
type CardVM struct {
	Instance domain.InstanceID
	Kind     CardKind
	Title    string
	Status   string
	Footer   string

	GPU     GPUVM
	Swap    SwapVM
	Quota   QuotaVM
	Spend   SpendVM
	Service ServiceVM
}

// OverviewVM is the full overview tab state.
type OverviewVM struct {
	LocalCards     []OverviewCardVM
	RemoteCards    []OverviewCardVM
	NothingEnabled bool
}

// overviewSwapLines caps the model list on the overview card.
const overviewSwapLines = 3

// BuildOverview derives the overview tab from the configured instances and
// their runtimes. Cards follow the configured order within each category;
// disabled instances are omitted entirely; enabled ones appear even before
// their first snapshot lands. paces carries the derived pace estimates by
// instance ID (nil is valid: pace is then simply absent).
func BuildOverview(instances []config.SourceInstance, states map[domain.InstanceID]domain.Runtime, paces map[domain.InstanceID]pace.InstancePaces, now time.Time) OverviewVM {
	vm := OverviewVM{
		LocalCards:  buildOverviewCards(instances, domain.CategoryLocal, states, paces, now),
		RemoteCards: buildOverviewCards(instances, domain.CategoryRemote, states, paces, now),
	}
	vm.NothingEnabled = len(vm.LocalCards) == 0 && len(vm.RemoteCards) == 0

	return vm
}

func buildOverviewCards(instances []config.SourceInstance, category domain.Category, states map[domain.InstanceID]domain.Runtime, paces map[domain.InstanceID]pace.InstancePaces, now time.Time) []OverviewCardVM {
	cards := make([]OverviewCardVM, 0, len(instances))
	for _, inst := range instances {
		if inst.Kind.Category() != category {
			continue
		}
		rt, ok := states[inst.ID]
		if !ok || !rt.Enabled {
			continue
		}
		cards = append(cards, BuildOverviewCard(inst, rt, paces[inst.ID], now))
	}

	return cards
}

// BuildLocalTab derives the local tab cards (full detail bodies).
func BuildLocalTab(instances []config.SourceInstance, states map[domain.InstanceID]domain.Runtime, paces map[domain.InstanceID]pace.InstancePaces, now time.Time) []CardVM {
	return buildCards(instances, domain.CategoryLocal, states, paces, now)
}

// BuildRemoteTab derives the remote tab cards (full detail bodies).
func BuildRemoteTab(instances []config.SourceInstance, states map[domain.InstanceID]domain.Runtime, paces map[domain.InstanceID]pace.InstancePaces, now time.Time) []CardVM {
	return buildCards(instances, domain.CategoryRemote, states, paces, now)
}

func buildCards(instances []config.SourceInstance, category domain.Category, states map[domain.InstanceID]domain.Runtime, paces map[domain.InstanceID]pace.InstancePaces, now time.Time) []CardVM {
	cards := make([]CardVM, 0, len(instances))
	for _, inst := range instances {
		if inst.Kind.Category() != category {
			continue
		}
		rt, ok := states[inst.ID]
		if !ok || !rt.Enabled {
			continue
		}
		cards = append(cards, BuildCard(inst, rt, paces[inst.ID], now, true))
	}

	return cards
}

// cardKind maps the configured kind onto the card body kind. It comes from
// the configuration, not the snapshot, so a card presents its configured
// kind even before the first snapshot lands.
func cardKind(kind domain.SourceKind) CardKind {
	switch kind {
	case domain.KindAMDGPUWeb:
		return KindGPU
	case domain.KindLlamaSwap:
		return KindSwap
	case domain.KindMiniMax, domain.KindZAI, domain.KindCodex:
		return KindQuota
	case domain.KindOpenRouter:
		return KindSpend
	case domain.KindHindsight:
		return KindService
	default:
		return KindNone
	}
}

// overviewKind maps the configured kind onto the overview card kind.
func overviewKind(kind domain.SourceKind) OverviewKind {
	switch kind {
	case domain.KindAMDGPUWeb:
		return OverviewMetrics
	case domain.KindLlamaSwap:
		return OverviewRuntime
	case domain.KindMiniMax, domain.KindZAI, domain.KindCodex:
		return OverviewQuota
	case domain.KindOpenRouter:
		return OverviewSpend
	case domain.KindHindsight:
		return OverviewService
	default:
		return OverviewKind("")
	}
}

// BuildCard derives one source card. The title is the instance's
// user-chosen name; the body is filled only when a snapshot of the
// matching type has landed. ip carries the instance's pace estimates (the
// zero value means none).
func BuildCard(inst config.SourceInstance, rt domain.Runtime, ip pace.InstancePaces, now time.Time, detail bool) CardVM {
	card := CardVM{
		Instance: inst.ID,
		Kind:     cardKind(inst.Kind),
		Title:    inst.Name,
		Status:   FreshnessText(rt, now),
		Footer:   FooterText(rt),
	}

	switch snap := rt.Snapshot.(type) {
	case domain.GPUSnapshot:
		card.GPU = BuildGPUVM(snap, detail)
	case domain.LlamaSwapSnapshot:
		card.Swap = BuildSwapVM(snap, detail)
	case domain.QuotaSnapshot:
		card.Quota = BuildQuotaVM(snap, ip, now, detail)
	case domain.CodexSnapshot:
		card.Quota = BuildCodexVM(snap, ip, now, detail)
	case domain.OpenRouterSnapshot:
		card.Spend = BuildSpendVM(snap, ip, detail)
	case domain.HindsightSnapshot:
		card.Service = BuildServiceVM(snap, now)
	}

	return card
}

// BuildOverviewCard derives one overview card from an instance and its
// runtime. The title is the instance's user-chosen name; the body is
// filled only when a snapshot of the matching type has landed.
func BuildOverviewCard(inst config.SourceInstance, rt domain.Runtime, ip pace.InstancePaces, now time.Time) OverviewCardVM {
	card := OverviewCardVM{
		Instance: inst.ID,
		Kind:     overviewKind(inst.Kind),
		Title:    inst.Name,
		Status:   FreshnessText(rt, now),
	}

	switch snap := rt.Snapshot.(type) {
	case domain.GPUSnapshot:
		card.Metrics = BuildMetricsOverview(snap)
	case domain.LlamaSwapSnapshot:
		card.Runtime = BuildRuntimeOverview(snap)
	case domain.QuotaSnapshot:
		card.Quota = BuildQuotaOverview(snap, ip, now)
	case domain.CodexSnapshot:
		card.Quota = BuildCodexOverview(snap, ip, now)
		// The plan name joins the card title ("ChatGPT Codex (Plus)") instead
		// of taking a line of its own, matching the other usage-based
		// providers' single-title cards. Only a landed snapshot knows it.
		if plan := registry.CodexPlanLabel(snap.PlanType); plan != "" {
			card.Title += " (" + plan + ")"
		}
	case domain.OpenRouterSnapshot:
		card.Spend = BuildSpendOverview(snap)
	case domain.HindsightSnapshot:
		card.Service = BuildServiceOverviewVM(snap)
	}

	return card
}

// BuildMetricsOverview derives compact metric bar data from a GPU snapshot.
func BuildMetricsOverview(snap domain.GPUSnapshot) MetricsOverviewVM {
	vm := MetricsOverviewVM{}

	if snap.GPUUtilizationPct != nil {
		pct := *snap.GPUUtilizationPct
		vm.GPU = MetricBarVM{
			Text:     "GPU " + FormatPct(snap.GPUUtilizationPct),
			Fraction: domain.Clamp01(pct / 100),
			Level:    utilLevel(pct),
		}
	}
	if s := formatBytePair(snap.VRAMUsedBytes, snap.VRAMTotalBytes); s != "" {
		frac := domain.Clamp01(float64(*snap.VRAMUsedBytes) / float64(*snap.VRAMTotalBytes))
		vm.VRAM = MetricBarVM{
			Text:     "VRAM " + s,
			Fraction: frac,
			Level:    bytePairLevel(snap.VRAMUsedBytes, snap.VRAMTotalBytes),
		}
	}
	if snap.TemperatureC != nil {
		vm.Temp = MetricRowVM{Label: "Temp", Value: FormatTemp(snap.TemperatureC), Level: tempLevel(*snap.TemperatureC)}
	}

	vm.HasData = vm.GPU.Text != "" || vm.VRAM.Text != "" || vm.Temp.Value != ""

	return vm
}

// BuildRuntimeOverview derives a compact running-model summary.
func BuildRuntimeOverview(snap domain.LlamaSwapSnapshot) RuntimeOverviewVM {
	n := len(snap.RunningModels)
	vm := RuntimeOverviewVM{HasData: true}

	switch n {
	case 0:
		vm.Summary = "No models running"
	case 1:
		vm.Summary = "1 model running"
	default:
		vm.Summary = fmt.Sprintf("%d models running", n)
	}

	for _, m := range snap.RunningModels {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		vm.Names = append(vm.Names, name)
		if len(vm.Names) == 2 {
			break
		}
	}
	vm.MoreCount = max(0, n-2)

	return vm
}

// BuildQuotaOverview derives up to two headline quota donuts. Pace
// estimates attach to the headline windows only — the Overview shows the
// projection of the windows it already displays.
func BuildQuotaOverview(snap domain.QuotaSnapshot, ip pace.InstancePaces, now time.Time) QuotaOverviewVM {
	vm := QuotaOverviewVM{}
	headlines := selectHeadlineWindows(snap.Windows)
	for _, w := range headlines {
		donut := OverviewDonutVM{
			Label:     shortLabel(w),
			PctText:   formatPctOnly(w.UsedFraction),
			Used:      w.UsedFraction,
			Remaining: w.RemainingFraction,
			ResetText: FormatResetCountdown(w.ResetAt, now),
			ResetAt:   w.ResetAt,
		}
		if est, ok := ip.Windows[w.ID]; ok {
			donut.Pace = buildPaceVM(est, now)
		}
		vm.Windows = append(vm.Windows, donut)
	}
	vm.HasData = len(vm.Windows) > 0

	return vm
}

// shortLabel maps a quota window ID to a compact label for the donut.
func shortLabel(w domain.QuotaWindow) string {
	switch w.ID {
	case "interval", "tokens-5h", "credits-5h":
		return "5h"
	case "weekly", "credits-weekly":
		return "week"
	case "mcp-monthly":
		return "month"
	case "codex-primary", "codex-secondary":
		if s := codexShortName(w.Name); s != "" {
			return s
		}
	}

	return w.Name
}

// codexShortName compacts the Codex window names for the donut; unknown
// names keep their full form.
func codexShortName(name string) string {
	switch name {
	case "5-hour":
		return "5h"
	case "1-hour":
		return "1h"
	case "Weekly":
		return "week"
	}

	return ""
}

// selectHeadlineWindows picks at most 2 windows, preferring the 5-hour
// and weekly windows when both exist.
func selectHeadlineWindows(windows []domain.QuotaWindow) []domain.QuotaWindow {
	priority := func(id string) int {
		switch id {
		case "interval", "tokens-5h", "credits-5h", "codex-primary":
			return 0
		case "weekly", "credits-weekly", "codex-secondary":
			return 1
		default:
			return 2
		}
	}
	sorted := make([]domain.QuotaWindow, len(windows))
	copy(sorted, windows)
	sort.Slice(sorted, func(i, j int) bool {
		return priority(sorted[i].ID) < priority(sorted[j].ID)
	})

	if len(sorted) > 2 {
		sorted = sorted[:2]
	}

	return sorted
}

// formatPctOnly renders the rounded used percentage without the "used"
// suffix. A positive usage that rounds to 0% reads "<1%" so real usage
// never displays as zero; only true zero shows "0%".
func formatPctOnly(fraction float64) string {
	pct := int(math.Round(domain.Clamp01(fraction) * 100))
	if pct == 0 && fraction > 0 {
		return "<1%"
	}

	return fmt.Sprintf("%d%%", pct)
}

// FreshnessText renders the per-source freshness label (legacy decision D7, docs/DECISION_LOG.md).
// There is no global timestamp; every source carries its own age.
func FreshnessText(rt domain.Runtime, now time.Time) string {
	switch rt.DeriveState() {
	case domain.StateDisabled:
		return ""
	case domain.StateLoading:
		return "Loading…"
	case domain.StateUnavailable:
		return "Unavailable"
	case domain.StateStale:
		return FormatAge(rt.LastSuccessAt, now) + " · stale"
	default:
		if rt.LastSuccessAt.IsZero() {
			return ""
		}

		return FormatAge(rt.LastSuccessAt, now)
	}
}

// FooterText renders the failure footer shown on Stale/Unavailable cards.
// The text carries the meaning; it is never color-only.
func FooterText(rt domain.Runtime) string {
	switch rt.DeriveState() {
	case domain.StateStale, domain.StateUnavailable:
		if msg := userMessage(rt.LastError); msg != "" {
			return "Last refresh failed: " + msg
		}
	}

	return ""
}

func userMessage(err error) string {
	var serr *source.Error
	if errors.As(err, &serr) {
		return serr.UserMessage()
	}

	return ""
}

// BuildGPUVM derives the GPU metric rows. Overview shows util, VRAM and
// temperature, omitting missing sensors; Detail shows every contracted
// row with "—" for missing sensors. Util, VRAM and temperature values
// carry a load level for coloring in both variants.
func BuildGPUVM(snap domain.GPUSnapshot, detail bool) GPUVM {
	vm := GPUVM{}

	if detail {
		rows := []MetricRowVM{
			{Label: "GPU util", Value: dashWhenNil(snap.GPUUtilizationPct, FormatPct), Level: metricLevel(snap.GPUUtilizationPct, utilLevel)},
			{Label: "VRAM", Value: dashIfEmpty(formatBytePair(snap.VRAMUsedBytes, snap.VRAMTotalBytes)), Level: bytePairLevel(snap.VRAMUsedBytes, snap.VRAMTotalBytes)},
			{Label: "GTT", Value: dashIfEmpty(formatBytePair(snap.GTTUsedBytes, snap.GTTTotalBytes))},
			{Label: "Temp", Value: dashWhenNil(snap.TemperatureC, FormatTemp), Level: metricLevel(snap.TemperatureC, tempLevel)},
			{Label: "Power", Value: dashWhenNil(snap.PowerWatts, FormatWatts)},
			{Label: "SCLK", Value: dashWhenNil(snap.SCLKMHz, FormatMHz)},
			{Label: "MCLK", Value: dashWhenNil(snap.MCLKMHz, FormatMHz)},
		}
		vm.Detail = rows

		return vm
	}

	if snap.GPUUtilizationPct != nil {
		vm.Overview = append(vm.Overview, MetricRowVM{Label: "GPU util", Value: FormatPct(snap.GPUUtilizationPct), Level: utilLevel(*snap.GPUUtilizationPct)})
	}
	if s := formatBytePair(snap.VRAMUsedBytes, snap.VRAMTotalBytes); s != "" {
		vm.Overview = append(vm.Overview, MetricRowVM{Label: "VRAM", Value: s, Level: bytePairLevel(snap.VRAMUsedBytes, snap.VRAMTotalBytes)})
	}
	if snap.TemperatureC != nil {
		vm.Overview = append(vm.Overview, MetricRowVM{Label: "Temp", Value: FormatTemp(snap.TemperatureC), Level: tempLevel(*snap.TemperatureC)})
	}

	return vm
}

// loadLevel classifies a load fraction (0–1) with the quota-bar
// thresholds mirrored onto usage: >0.90 critical, >0.80 low, >0.50
// warning, else good.
func loadLevel(frac float64) MetricLevel {
	switch {
	case frac > 0.90:
		return MetricCritical
	case frac > 0.80:
		return MetricLow
	case frac > 0.50:
		return MetricWarning
	default:
		return MetricGood
	}
}

// utilLevel classifies a GPU utilization percentage.
func utilLevel(pct float64) MetricLevel { return loadLevel(pct / 100) }

// tempLevel classifies a temperature: ≥80 °C critical, ≥70 low, ≥60
// warning, else good.
func tempLevel(c float64) MetricLevel {
	switch {
	case c >= 80:
		return MetricCritical
	case c >= 70:
		return MetricLow
	case c >= 60:
		return MetricWarning
	default:
		return MetricGood
	}
}

// metricLevel levels a sensor value when present, neutral when absent.
func metricLevel(v *float64, level func(float64) MetricLevel) MetricLevel {
	if v == nil {
		return MetricNeutral
	}

	return level(*v)
}

// bytePairLevel classifies the used fraction of a byte pair; neutral
// when either side is missing or the total is zero.
func bytePairLevel(used, total *uint64) MetricLevel {
	if used == nil || total == nil || *total == 0 {
		return MetricNeutral
	}

	return loadLevel(float64(*used) / float64(*total))
}

// BuildSwapVM derives the running-model lines.
func BuildSwapVM(snap domain.LlamaSwapSnapshot, detail bool) SwapVM {
	lines := make([]string, 0, len(snap.RunningModels))
	for _, m := range snap.RunningModels {
		if m.Name != "" {
			lines = append(lines, m.Name)
		} else {
			lines = append(lines, m.ID)
		}
	}

	vm := SwapVM{ModelLines: lines}
	if !detail && len(lines) > overviewSwapLines {
		vm.ModelLines = lines[:overviewSwapLines]
		vm.MoreCount = len(lines) - overviewSwapLines
	}

	return vm
}

// BuildQuotaVM derives the quota window rows. Secondary (raw counts) is a
// detail-tab extra; the overview shows the used lines only. A window with
// a tracker estimate carries its pace; without one the row renders exactly
// as before.
func BuildQuotaVM(snap domain.QuotaSnapshot, ip pace.InstancePaces, now time.Time, detail bool) QuotaVM {
	vm := QuotaVM{Windows: make([]QuotaWindowVM, 0, len(snap.Windows))}
	for _, w := range snap.Windows {
		row := QuotaWindowVM{
			Name:      w.Name,
			UsedText:  FormatUsed(w.UsedFraction),
			ResetText: FormatResetCountdown(w.ResetAt, now),
			ResetAt:   w.ResetAt,
			Used:      w.UsedFraction,
			Remaining: w.RemainingFraction,
		}
		if detail {
			row.Secondary = formatCounts(w)
		}
		if est, ok := ip.Windows[w.ID]; ok {
			row.Pace = buildPaceVM(est, now)
		}
		vm.Windows = append(vm.Windows, row)
	}

	return vm
}

// BuildCodexVM derives a ChatGPT Codex card from its snapshot: the shared
// quota window rows plus, in detail view, an optional Account section
// (plan, reset credits, rate-limit-reached). Absent optional fields are
// omitted, never faked (docs/S6_UI_UX.md §Detail); no row is
// money-sensitive, so the privacy setting never touches them.
func BuildCodexVM(snap domain.CodexSnapshot, ip pace.InstancePaces, now time.Time, detail bool) QuotaVM {
	vm := QuotaVM{Windows: make([]QuotaWindowVM, 0, len(snap.Windows))}
	for _, w := range snap.Windows {
		row := QuotaWindowVM{
			Name:      w.Name,
			UsedText:  FormatUsed(w.UsedFraction),
			ResetText: FormatResetCountdown(w.ResetAt, now),
			ResetAt:   w.ResetAt,
			Used:      w.UsedFraction,
			Remaining: w.RemainingFraction,
		}
		if est, ok := ip.Windows[w.ID]; ok {
			row.Pace = buildPaceVM(est, now)
		}
		vm.Windows = append(vm.Windows, row)
	}
	if !detail {
		return vm
	}
	if plan := registry.CodexPlanLabel(snap.PlanType); plan != "" {
		vm.Account = append(vm.Account, MetricRowVM{Label: "Plan", Value: plan})
	}
	if snap.ResetCreditsAvailable != nil {
		vm.Account = append(vm.Account, MetricRowVM{
			Label: "Reset credits",
			Value: FormatCount(float64(*snap.ResetCreditsAvailable)),
		})
	}
	if snap.RateLimitReachedType != "" {
		vm.Account = append(vm.Account, MetricRowVM{
			Label: "Rate limit",
			Value: "Reached (" + snap.RateLimitReachedType + ")",
			Level: MetricWarning,
		})
	}

	return vm
}

// BuildCodexOverview derives the compact overview card: the shared headline
// donuts. The plan name is not part of the body — the card title carries it.
func BuildCodexOverview(snap domain.CodexSnapshot, ip pace.InstancePaces, now time.Time) QuotaOverviewVM {
	vm := QuotaOverviewVM{}
	headlines := selectHeadlineWindows(snap.Windows)
	for _, w := range headlines {
		donut := OverviewDonutVM{
			Label:     shortLabel(w),
			PctText:   formatPctOnly(w.UsedFraction),
			Used:      w.UsedFraction,
			Remaining: w.RemainingFraction,
			ResetText: FormatResetCountdown(w.ResetAt, now),
			ResetAt:   w.ResetAt,
		}
		if est, ok := ip.Windows[w.ID]; ok {
			donut.Pace = buildPaceVM(est, now)
		}
		vm.Windows = append(vm.Windows, donut)
	}
	vm.HasData = len(vm.Windows) > 0

	return vm
}

// formatCounts renders the raw provider counts when both ends are known,
// used-oriented to match the bar: "used / limit [unit]".
func formatCounts(w domain.QuotaWindow) string {
	if w.Remaining == nil || w.Limit == nil {
		return ""
	}
	used := *w.Limit - *w.Remaining
	if used < 0 {
		used = 0
	}
	s := fmt.Sprintf("%s / %s", FormatCount(used), FormatCount(*w.Limit))
	if w.Unit != "" {
		s += " " + w.Unit
	}

	return s
}

// BuildSpendVM derives an OpenRouter card's usage, limit and account rows.
// The Total line is a detail-tab extra; account values are always marked
// sensitive (the global privacy setting conceals them). ip.Spend attaches
// the derived spend speed when the tracker has an estimate.
func BuildSpendVM(snap domain.OpenRouterSnapshot, ip pace.InstancePaces, detail bool) SpendVM {
	var vm SpendVM

	if snap.Key != nil {
		k := snap.Key
		vm.Usage = []MoneyMetricVM{
			moneyRow("Today", k.UsageDaily),
			moneyRow("Week", k.UsageWeekly),
			moneyRow("Month", k.UsageMonthly),
		}
		if detail {
			vm.Usage = append(vm.Usage, moneyRow("Total", k.UsageTotal))
		}
		vm.Limit = spendLimitVM(k.Limit, detail)
	}
	if snap.Account != nil {
		a := snap.Account
		vm.Account = []MoneyMetricVM{
			sensitiveMoneyRow("Balance", a.Balance),
			sensitiveMoneyRow("Purchased credits", a.TotalCredits),
			sensitiveMoneyRow("Account usage", a.TotalUsage),
		}
	}
	if ip.Spend != nil {
		vm.Pace = buildSpendPaceVM(*ip.Spend)
	}
	vm.HasData = snap.Key != nil || snap.Account != nil

	return vm
}

// BuildSpendOverview derives the compact overview variant: no Total row,
// the Balance surfaced as its own optional row. No pace — the Overview
// spend card stays as compact as it is today (S7_UI_UX).
func BuildSpendOverview(snap domain.OpenRouterSnapshot) SpendOverviewVM {
	detail := BuildSpendVM(snap, pace.InstancePaces{}, false)
	ov := SpendOverviewVM{Usage: detail.Usage, Limit: detail.Limit}
	if len(detail.Account) > 0 {
		balance := detail.Account[0]
		ov.Balance = &balance
	}
	ov.HasData = detail.HasData

	return ov
}

// spendLimitVM maps one usable limit onto the shared quota-bar data; nil
// when there is no limit to graph. Fractions derive from limit_remaining,
// never from the period usage counters.
func spendLimitVM(l *domain.OpenRouterLimit, detail bool) *SpendLimitVM {
	if l == nil || l.Amount <= 0 {
		return nil
	}
	used := l.Amount - l.Remaining
	vm := &SpendLimitVM{
		Label:     limitLabel(l.Cadence),
		UsedText:  FormatUsed(domain.Clamp01(used / l.Amount)),
		Used:      domain.Clamp01(used / l.Amount),
		Remaining: domain.Clamp01(l.Remaining / l.Amount),
	}
	if detail {
		vm.RawText = FormatMoney(max(0, used)) + " / " + FormatMoney(l.Amount)
	}

	return vm
}

// limitLabel names the bar after its enforced cadence; an unknown or null
// cadence keeps the neutral "Key limit".
func limitLabel(cadence string) string {
	switch cadence {
	case domain.CadenceDaily:
		return "Daily limit"
	case domain.CadenceWeekly:
		return "Weekly limit"
	case domain.CadenceMonthly:
		return "Monthly limit"
	default:
		return "Key limit"
	}
}

func moneyRow(label string, v float64) MoneyMetricVM {
	return MoneyMetricVM{Label: label, Text: FormatMoney(v)}
}

func sensitiveMoneyRow(label string, v float64) MoneyMetricVM {
	return MoneyMetricVM{Label: label, Text: FormatMoney(v), Sensitive: true}
}

func dashWhenNil(v *float64, format func(*float64) string) string {
	if v == nil {
		return "—"
	}

	return format(v)
}

// BuildServiceVM derives a Hindsight card's sectioned rows from one
// snapshot (S5_UI_UX §5). Unavailable values render as "—" — availability,
// never a fabricated zero — and a section with nothing observed is omitted
// entirely. now anchors the memory section's relative ages.
func BuildServiceVM(snap domain.HindsightSnapshot, now time.Time) ServiceVM {
	vm := ServiceVM{HasData: true}

	vm.Service = serviceRows(snap.Service)
	vm.Memory = memoryRows(snap.Memory, now)
	vm.Activity = activityRows(snap.Activity, snap.ActivitySeen)
	vm.Queue = queueRows(snap.Queue)
	vm.Runtime = runtimeRows(snap)

	return vm
}

// serviceRows builds the Service section: the health status plus every
// optional identity/diagnostic value that was observed.
func serviceRows(s domain.HindsightService) []MetricRowVM {
	rows := []MetricRowVM{{Label: "Status", Value: titleFirst(s.Status), Level: serviceStatusLevel(s.Status)}}
	if s.Version != "" {
		rows = append(rows, MetricRowVM{Label: "Version", Value: s.Version})
	}
	if s.Uptime > 0 {
		rows = append(rows, MetricRowVM{Label: "Uptime", Value: FormatUptime(s.Uptime)})
	}
	if s.Database != "" {
		rows = append(rows, MetricRowVM{Label: "Database", Value: s.Database, Level: serviceStatusLevel(s.Database)})
	}
	if s.DBAcquireMS != nil {
		rows = append(rows, MetricRowVM{Label: "DB acquire", Value: fmt.Sprintf("%.1f ms", *s.DBAcquireMS)})
	}

	return rows
}

// serviceStatusLevel colors a healthy service/database state green and
// leaves anything else neutral — S5 invents no threshold system.
func serviceStatusLevel(state string) MetricLevel {
	switch strings.ToLower(state) {
	case "healthy", "connected", "ok":
		return MetricGood
	default:
		return MetricNeutral
	}
}

// memoryRows builds the Memory section: aggregate counts and the two
// relative timestamps that age in place. nil memory means the slow fetch
// has not landed yet — no section at all, not a block of dashes.
func memoryRows(m *domain.HindsightMemory, now time.Time) []AgeRowVM {
	if m == nil {
		return nil
	}
	rows := []AgeRowVM{
		{Label: "Banks", Value: FormatCount(float64(m.Banks))},
		{Label: "Facts", Value: FormatCount(float64(m.Facts))},
		{Label: "Last write", Value: FormatAgePtr(m.LastWriteAt, now), At: m.LastWriteAt},
		{Label: "Last document", Value: FormatAgePtr(m.LastDocumentAt, now), At: m.LastDocumentAt},
	}

	return rows
}

// activityRows builds the rate rows of the families the deployment has
// exported at least once. A never-seen family has no row at all — recall is
// never recorded on some deployments, and a permanent dash reads as broken —
// while a seen-but-underivable rate keeps its "—" (first sample, counter
// reset) rather than 0/m. The adapter latches the seen-set, so a row can
// appear when a counter first materializes but never disappears.
func activityRows(a domain.HindsightActivity, seen domain.HindsightActivitySeen) []MetricRowVM {
	rows := make([]MetricRowVM, 0, 7)
	add := func(ok bool, label string, rate *float64) {
		if ok {
			rows = append(rows, MetricRowVM{Label: label, Value: FormatRatePerMin(rate)})
		}
	}
	add(seen.Retain, "Retain", a.RetainPerMin)
	add(seen.Recall, "Recall", a.RecallPerMin)
	add(seen.Reflect, "Reflect", a.ReflectPerMin)
	add(seen.Consolidate, "Consolidation", a.ConsolidatePerMin)
	add(seen.LLMCalls, "LLM calls", a.LLMCallsPerMin)
	add(seen.InputTokens, "Input tokens", a.InputTokensPerMin)
	add(seen.OutputTokens, "Output tokens", a.OutputTokensPerMin)

	return rows
}

// queueRows builds the Queue section, which exists only when the
// deployment exposed at least one backlog family; within it only observed
// values become rows. Failed backlogs read as critical.
func queueRows(q *domain.HindsightQueue) []MetricRowVM {
	if q == nil {
		return nil
	}
	var rows []MetricRowVM
	if q.Pending != nil {
		rows = append(rows, MetricRowVM{Label: "Pending", Value: FormatCount(*q.Pending)})
	}
	if q.Processing != nil {
		rows = append(rows, MetricRowVM{Label: "Processing", Value: FormatCount(*q.Processing)})
	}
	if q.Failed != nil {
		rows = append(rows, MetricRowVM{Label: "Failed", Value: FormatCount(*q.Failed), Level: failureLevel(*q.Failed)})
	}
	if q.ConsolidationBacklog != nil {
		rows = append(rows, MetricRowVM{Label: "Consolidation backlog", Value: FormatCount(*q.ConsolidationBacklog)})
	}
	if q.ConsolidationFailed != nil {
		rows = append(rows, MetricRowVM{Label: "Consolidation failed", Value: FormatCount(*q.ConsolidationFailed), Level: failureLevel(*q.ConsolidationFailed)})
	}

	return rows
}

// failureLevel marks a positive failed count critical; zero is normal.
func failureLevel(v float64) MetricLevel {
	if v > 0 {
		return MetricCritical
	}

	return MetricNeutral
}

// runtimeRows builds the Runtime section. DB Waiting prefers the /health
// diagnostic and falls back to the Prometheus gauge only when the health
// value is absent (neither source is required).
func runtimeRows(snap domain.HindsightSnapshot) []MetricRowVM {
	var rows []MetricRowVM
	if active, max, ok := dbPoolActive(snap.Runtime.DBPoolSize, snap.Runtime.DBPoolIdle, snap.Runtime.DBPoolMax); ok {
		value := FormatCount(active)
		if !math.IsNaN(max) {
			value += " / " + FormatCount(max)
		}
		rows = append(rows, MetricRowVM{Label: "DB pool active", Value: value})
	}
	if waiting := preferredDBWaiting(snap); waiting != nil {
		rows = append(rows, MetricRowVM{Label: "DB waiting", Value: FormatCount(*waiting), Level: positiveLevel(*waiting)})
	}
	if snap.Runtime.HTTPInProgress != nil {
		rows = append(rows, MetricRowVM{Label: "HTTP in progress", Value: FormatCount(*snap.Runtime.HTTPInProgress)})
	}
	if snap.Activity.HTTP5xxPercent != nil {
		rows = append(rows, MetricRowVM{Label: "HTTP 5xx", Value: FormatPct1(snap.Activity.HTTP5xxPercent), Level: positiveLevel(*snap.Activity.HTTP5xxPercent)})
	}

	return rows
}

// preferredDBWaiting applies the S5 sourcing rule: the /health
// db_pool_waiting diagnostic wins; the Prometheus gauge is the fallback.
func preferredDBWaiting(snap domain.HindsightSnapshot) *float64 {
	if snap.Service.DBPoolWaiting != nil {
		return snap.Service.DBPoolWaiting
	}

	return snap.Runtime.DBPoolWaiting
}

// dbPoolActive derives active connections as size−idle (floored at 0) and
// caps the result at max when the gauge exists. max returns NaN when no
// gauge was exposed. ok is false when size or idle is missing — the pair
// is meaningless half-present.
func dbPoolActive(size, idle, poolMax *float64) (active, max float64, ok bool) {
	if size == nil || idle == nil {
		return 0, math.NaN(), false
	}
	active = math.Max(0, *size-*idle)
	if poolMax != nil {
		max = *poolMax
		active = math.Min(active, max)
	} else {
		max = math.NaN()
	}

	return active, max, true
}

// positiveLevel warns on any positive value (DB waiting, 5xx share).
func positiveLevel(v float64) MetricLevel {
	if v > 0 {
		return MetricWarning
	}

	return MetricNeutral
}

// titleFirst upper-cases a status word's first rune for display
// ("healthy" → "Healthy").
func titleFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)

	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// BuildServiceOverviewVM derives the compact overview body (S5_UI_UX §3-4):
// health first, then Banks/Facts, then the Recall/LLM rates (only for
// families the deployment exports), then the most
// useful available bottom row — async backlog when any of it exists,
// otherwise consolidation backlog, otherwise DB pressure. Pairs whose own
// value is unavailable show "—"; a family with nothing at all is skipped
// rather than rendered empty.
func BuildServiceOverviewVM(snap domain.HindsightSnapshot) ServiceOverviewVM {
	vm := ServiceOverviewVM{HasData: true}

	vm.Summary = titleFirst(snap.Service.Status)
	if snap.Service.Version != "" {
		vm.Summary += " · v" + snap.Service.Version
	}

	// Memory aggregates land with the first slow refresh (nil before that);
	// once landed, a real empty deployment renders as Banks 0 / Facts 0.
	if snap.Memory != nil {
		vm.Rows = append(vm.Rows,
			MetricRowVM{Label: "Banks", Value: FormatCount(float64(snap.Memory.Banks))},
			MetricRowVM{Label: "Facts", Value: FormatCount(float64(snap.Memory.Facts))},
		)
	}

	// Rates are headline even before their baseline exists ("—") — but only
	// for families the deployment has actually exported.
	if snap.ActivitySeen.Recall {
		vm.Rows = append(vm.Rows, MetricRowVM{Label: "Recall", Value: FormatRatePerMin(snap.Activity.RecallPerMin)})
	}
	if snap.ActivitySeen.LLMCalls {
		vm.Rows = append(vm.Rows, MetricRowVM{Label: "LLM calls", Value: FormatRatePerMin(snap.Activity.LLMCallsPerMin)})
	}

	vm.Rows = append(vm.Rows, overviewBacklogRows(snap)...)

	return vm
}

// overviewBacklogRows picks the overview's bottom row by availability:
// async Pending/Failed when at least one of them exists, else the
// consolidation pair, else DB pressure with whatever sides are known.
func overviewBacklogRows(snap domain.HindsightSnapshot) []MetricRowVM {
	q := snap.Queue
	if q != nil && (q.Pending != nil || q.Failed != nil) {
		return []MetricRowVM{
			{Label: "Pending", Value: dashWhenNil(q.Pending, func(v *float64) string { return FormatCount(*v) })},
			{Label: "Failed", Value: dashWhenNil(q.Failed, func(v *float64) string { return FormatCount(*v) }), Level: failureLevel(derefOrZero(q.Failed))},
		}
	}
	if q != nil && (q.ConsolidationBacklog != nil || q.ConsolidationFailed != nil) {
		return []MetricRowVM{
			{Label: "Consol. backlog", Value: dashWhenNil(q.ConsolidationBacklog, func(v *float64) string { return FormatCount(*v) })},
			{Label: "Consol. failed", Value: dashWhenNil(q.ConsolidationFailed, func(v *float64) string { return FormatCount(*v) }), Level: failureLevel(derefOrZero(q.ConsolidationFailed))},
		}
	}

	active, poolMax, ok := dbPoolActive(snap.Runtime.DBPoolSize, snap.Runtime.DBPoolIdle, snap.Runtime.DBPoolMax)
	waiting := preferredDBWaiting(snap)
	if !ok && waiting == nil {
		return nil
	}
	var rows []MetricRowVM
	if ok {
		value := FormatCount(active)
		if !math.IsNaN(poolMax) {
			value += " / " + FormatCount(poolMax)
		}
		rows = append(rows, MetricRowVM{Label: "DB active", Value: value})
	}
	if waiting != nil {
		rows = append(rows, MetricRowVM{Label: "Waiting", Value: FormatCount(*waiting), Level: positiveLevel(*waiting)})
	}

	return rows
}

func derefOrZero(v *float64) float64 {
	if v == nil {
		return 0
	}

	return *v
}

// FormatRatePerMin renders a per-minute rate: unavailable as "—", exact
// zero as "0/m", a small positive below the first decimal as "<0.1/m",
// sub-10 rates with one useful decimal (whole numbers stay bare, "2/m"),
// and larger ones compactly ("12.4k/m").
func FormatRatePerMin(v *float64) string {
	if v == nil {
		return "—"
	}
	switch {
	case *v == 0:
		return "0/m"
	case *v < 0.1:
		return "<0.1/m"
	case *v < 10:
		if *v == math.Trunc(*v) {
			return fmt.Sprintf("%.0f/m", *v)
		}

		return fmt.Sprintf("%.1f/m", *v)
	default:
		return FormatCount(*v) + "/m"
	}
}

// FormatPct1 renders a percentage with one decimal ("0.0%", "9.1%").
func FormatPct1(v *float64) string {
	if v == nil {
		return "—"
	}

	return fmt.Sprintf("%.1f%%", *v)
}

// FormatUptime renders a compact process uptime: "42s", "3m", "2h 5m",
// "2d 7h".
func FormatUptime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours()/24), int(d.Hours())%24)
	}
}

// FormatAgePtr renders a relative age for an optional instant; nil is the
// unavailable dash.
func FormatAgePtr(t *time.Time, now time.Time) string {
	if t == nil {
		return "—"
	}

	return FormatAge(*t, now)
}

// formatBytePair renders "13.8 / 16.0 GiB"; an empty string when either
// side is missing (the pair is meaningless half-present).
func formatBytePair(used, total *uint64) string {
	if used == nil || total == nil {
		return ""
	}

	return FormatBytesGiB(*used) + " / " + FormatBytesGiB(*total) + " GiB"
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}

	return s
}

// FormatAge renders a relative age: "5s ago", "2m ago", "1h ago", "3d ago".
func FormatAge(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "0s ago"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// FormatUsed renders the NN% used label (used-oriented quota display).
// Sub-rounding usage reads "<1% used"; only true zero shows "0% used".
func FormatUsed(fraction float64) string {
	return formatPctOnly(fraction) + " used"
}

// FormatBytesGiB renders a byte count in GiB with one decimal.
func FormatBytesGiB(bytes uint64) string {
	return fmt.Sprintf("%.1f", float64(bytes)/(1<<30))
}

// FormatTemp renders "63 °C".
func FormatTemp(c *float64) string { return fmt.Sprintf("%.0f °C", *c) }

// FormatMHz renders "2420 MHz".
func FormatMHz(v *float64) string { return fmt.Sprintf("%.0f MHz", *v) }

// FormatWatts renders "172 W".
func FormatWatts(v *float64) string { return fmt.Sprintf("%.0f W", *v) }

// FormatPct renders "74%".
func FormatPct(v *float64) string { return fmt.Sprintf("%.0f%%", *v) }

// FormatCount renders a raw count compactly: "312", "12.4k", "1.2M".
func FormatCount(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", v/1_000_000)
	case v >= 10_000:
		return fmt.Sprintf("%.1fk", v/1_000)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// FormatMoney renders an account-level dollar amount. A cent or more uses
// two decimals; a smaller positive value keeps up to four decimals (with
// trailing zeros trimmed, never below two decimals) so sub-cent usage does
// not read as zero. Negative amounts keep their sign ("-") unless they
// round away entirely (no "-$0.00"); non-finite values render as an em dash.
func FormatMoney(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	neg := ""
	if v < 0 {
		neg = "-"
		v = -v
	}
	var s string
	if v == 0 || v >= 0.01 {
		s = fmt.Sprintf("%.2f", v)
	} else {
		s = strings.TrimRight(strconv.FormatFloat(v, 'f', 4, 64), "0")
		if strings.HasSuffix(s, ".") { // rounding ate the whole fraction
			s += "00"
		}
	}
	if s == "0.00" {
		return "$0.00"
	}

	return neg + "$" + s
}

// FormatResetCountdown renders a compact relative countdown from an
// absolute ResetAt. Pure function — takes an explicit now.
//
//	nil ResetAt  → empty string
//	past/now     → "now"
//	< 1 min     → "NNs"
//	< 1 hour    → "NNm"
//	< 24 hours  → "NhNNm" (no space)
//	≥ 24 hours  → "NNdNNh"
func FormatResetCountdown(resetAt *time.Time, now time.Time) string {
	if resetAt == nil {
		return ""
	}
	d := resetAt.Sub(now)
	if d <= 0 {
		return "now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60

		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours() / 24)
		h := int(d.Hours()) % 24

		return fmt.Sprintf("%dd%dh", days, h)
	}
}
