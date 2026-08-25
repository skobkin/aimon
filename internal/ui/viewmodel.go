// Package ui builds the presentation layer. View models are pure data
// derived from domain runtimes — no Fyne objects — so all formatting and
// state mapping is unit-testable without a display. The Fyne-facing code
// consumes these VMs.
package ui

import (
	"errors"
	"fmt"
	"math"
	"time"

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

// CardVM is one source card. Status is the per-source freshness label
// ("5s ago", "1m ago · stale", "Unavailable", "Loading…"); Footer carries
// the last refresh failure for Stale/Unavailable cards.
type CardVM struct {
	ID     domain.SourceID
	Kind   CardKind
	Title  string
	Status string
	Footer string

	GPU   GPUVM
	Swap  SwapVM
	Quota QuotaVM
}

// OverviewVM is the full overview tab state.
type OverviewVM struct {
	LocalCards     []CardVM
	RemoteCards    []CardVM
	NothingEnabled bool
}

// localOrder and remoteOrder fix the card order within each category.
var (
	localOrder  = []domain.SourceID{domain.SourceAMDGPUWeb, domain.SourceLlamaSwap}
	remoteOrder = []domain.SourceID{domain.SourceMiniMax, domain.SourceZAI}
)

// overviewSwapLines caps the model list on the overview card.
const overviewSwapLines = 3

// BuildOverview derives the overview tab from the current runtimes.
// Disabled sources are omitted entirely; enabled ones appear even before
// their first snapshot lands.
func BuildOverview(states map[domain.SourceID]domain.Runtime, now time.Time) OverviewVM {
	vm := OverviewVM{
		LocalCards:  buildCards(localOrder, states, now, false),
		RemoteCards: buildCards(remoteOrder, states, now, false),
	}
	vm.NothingEnabled = len(vm.LocalCards) == 0 && len(vm.RemoteCards) == 0
	return vm
}

// BuildLocalTab derives the local tab cards (full detail bodies).
func BuildLocalTab(states map[domain.SourceID]domain.Runtime, now time.Time) []CardVM {
	return buildCards(localOrder, states, now, true)
}

// BuildRemoteTab derives the remote tab cards (full detail bodies).
func BuildRemoteTab(states map[domain.SourceID]domain.Runtime, now time.Time) []CardVM {
	return buildCards(remoteOrder, states, now, true)
}

func buildCards(order []domain.SourceID, states map[domain.SourceID]domain.Runtime, now time.Time, detail bool) []CardVM {
	cards := make([]CardVM, 0, len(order))
	for _, id := range order {
		rt, ok := states[id]
		if !ok || !rt.Enabled {
			continue
		}
		cards = append(cards, BuildCard(id, rt, now, detail))
	}
	return cards
}

// BuildCard derives one source card.
func BuildCard(id domain.SourceID, rt domain.Runtime, now time.Time, detail bool) CardVM {
	card := CardVM{
		ID:     id,
		Kind:   KindNone,
		Title:  id.DisplayName(),
		Status: FreshnessText(rt, now),
		Footer: FooterText(rt),
	}

	switch snap := rt.Snapshot.(type) {
	case domain.GPUSnapshot:
		card.Kind = KindGPU
		card.GPU = BuildGPUVM(snap, detail)
	case domain.LlamaSwapSnapshot:
		card.Kind = KindSwap
		card.Swap = BuildSwapVM(snap, detail)
	case domain.QuotaSnapshot:
		card.Kind = KindQuota
		card.Quota = BuildQuotaVM(snap, now, detail)
	}
	return card
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
func FormatUsed(fraction float64) string {
	return fmt.Sprintf("%d%% used", int(math.Round(domain.Clamp01(fraction)*100)))
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
