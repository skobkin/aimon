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
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// CardKind discriminates which body a card renders.
type CardKind string

// Card body kinds; KindNone means the source has no data yet.
const (
	KindNone  CardKind = ""
	KindGPU   CardKind = "gpu"
	KindSwap  CardKind = "swap"
	KindQuota CardKind = "quota"
	KindSpend CardKind = "spend"
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
	Secondary string
	Used      float64
	Remaining float64
}

// QuotaVM carries the window rows of one quota snapshot.
type QuotaVM struct {
	Windows []QuotaWindowVM
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

// OverviewKind discriminates Overview card presentation.
type OverviewKind string

// Overview card kinds; zero value means no data yet.
const (
	OverviewMetrics OverviewKind = "metrics"
	OverviewRuntime OverviewKind = "runtime"
	OverviewQuota   OverviewKind = "quota"
	OverviewSpend   OverviewKind = "spend"
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

	GPU   GPUVM
	Swap  SwapVM
	Quota QuotaVM
	Spend SpendVM
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
// their first snapshot lands.
func BuildOverview(instances []config.SourceInstance, states map[domain.InstanceID]domain.Runtime, now time.Time) OverviewVM {
	vm := OverviewVM{
		LocalCards:  buildOverviewCards(instances, domain.CategoryLocal, states, now),
		RemoteCards: buildOverviewCards(instances, domain.CategoryRemote, states, now),
	}
	vm.NothingEnabled = len(vm.LocalCards) == 0 && len(vm.RemoteCards) == 0
	return vm
}

func buildOverviewCards(instances []config.SourceInstance, category domain.Category, states map[domain.InstanceID]domain.Runtime, now time.Time) []OverviewCardVM {
	cards := make([]OverviewCardVM, 0, len(instances))
	for _, inst := range instances {
		if inst.Kind.Category() != category {
			continue
		}
		rt, ok := states[inst.ID]
		if !ok || !rt.Enabled {
			continue
		}
		cards = append(cards, BuildOverviewCard(inst, rt, now))
	}
	return cards
}

// BuildLocalTab derives the local tab cards (full detail bodies).
func BuildLocalTab(instances []config.SourceInstance, states map[domain.InstanceID]domain.Runtime, now time.Time) []CardVM {
	return buildCards(instances, domain.CategoryLocal, states, now)
}

// BuildRemoteTab derives the remote tab cards (full detail bodies).
func BuildRemoteTab(instances []config.SourceInstance, states map[domain.InstanceID]domain.Runtime, now time.Time) []CardVM {
	return buildCards(instances, domain.CategoryRemote, states, now)
}

func buildCards(instances []config.SourceInstance, category domain.Category, states map[domain.InstanceID]domain.Runtime, now time.Time) []CardVM {
	cards := make([]CardVM, 0, len(instances))
	for _, inst := range instances {
		if inst.Kind.Category() != category {
			continue
		}
		rt, ok := states[inst.ID]
		if !ok || !rt.Enabled {
			continue
		}
		cards = append(cards, BuildCard(inst, rt, now, true))
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
	case domain.KindMiniMax, domain.KindZAI:
		return KindQuota
	case domain.KindOpenRouter:
		return KindSpend
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
	case domain.KindMiniMax, domain.KindZAI:
		return OverviewQuota
	case domain.KindOpenRouter:
		return OverviewSpend
	default:
		return OverviewKind("")
	}
}

// BuildCard derives one source card. The title is the instance's
// user-chosen name; the body is filled only when a snapshot of the
// matching type has landed.
func BuildCard(inst config.SourceInstance, rt domain.Runtime, now time.Time, detail bool) CardVM {
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
		card.Quota = BuildQuotaVM(snap, now, detail)
	case domain.OpenRouterSnapshot:
		card.Spend = BuildSpendVM(snap, detail)
	}
	return card
}

// BuildOverviewCard derives one overview card from an instance and its
// runtime. The title is the instance's user-chosen name; the body is
// filled only when a snapshot of the matching type has landed.
func BuildOverviewCard(inst config.SourceInstance, rt domain.Runtime, now time.Time) OverviewCardVM {
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
		card.Quota = BuildQuotaOverview(snap, now)
	case domain.OpenRouterSnapshot:
		card.Spend = BuildSpendOverview(snap)
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

	switch {
	case n == 0:
		vm.Summary = "No models running"
	case n == 1:
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

// BuildQuotaOverview derives up to two headline quota donuts.
func BuildQuotaOverview(snap domain.QuotaSnapshot, now time.Time) QuotaOverviewVM {
	vm := QuotaOverviewVM{}
	headlines := selectHeadlineWindows(snap.Windows)
	for _, w := range headlines {
		vm.Windows = append(vm.Windows, OverviewDonutVM{
			Label:     shortLabel(w),
			PctText:   formatPctOnly(w.UsedFraction),
			Used:      w.UsedFraction,
			Remaining: w.RemainingFraction,
			ResetText: FormatResetCountdown(w.ResetAt, now),
		})
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
	default:
		return w.Name
	}
}

// selectHeadlineWindows picks at most 2 windows, preferring the 5-hour
// and weekly windows when both exist.
func selectHeadlineWindows(windows []domain.QuotaWindow) []domain.QuotaWindow {
	priority := func(id string) int {
		switch id {
		case "interval", "tokens-5h", "credits-5h":
			return 0
		case "weekly", "credits-weekly":
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

// FreshnessText renders the per-source freshness label (DECISIONS D7).
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
// detail-tab extra; the overview shows the used lines only.
func BuildQuotaVM(snap domain.QuotaSnapshot, now time.Time, detail bool) QuotaVM {
	vm := QuotaVM{Windows: make([]QuotaWindowVM, 0, len(snap.Windows))}
	for _, w := range snap.Windows {
		row := QuotaWindowVM{
			Name:      w.Name,
			UsedText:  FormatUsed(w.UsedFraction),
			ResetText: FormatResetCountdown(w.ResetAt, now),
			Used:      w.UsedFraction,
			Remaining: w.RemainingFraction,
		}
		if detail {
			row.Secondary = formatCounts(w)
		}
		vm.Windows = append(vm.Windows, row)
	}
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
// sensitive (the global privacy setting conceals them).
func BuildSpendVM(snap domain.OpenRouterSnapshot, detail bool) SpendVM {
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
	vm.HasData = snap.Key != nil || snap.Account != nil
	return vm
}

// BuildSpendOverview derives the compact overview variant: no Total row,
// the Balance surfaced as its own optional row.
func BuildSpendOverview(snap domain.OpenRouterSnapshot) SpendOverviewVM {
	detail := BuildSpendVM(snap, false)
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
