// Package minimax adapts the MiniMax Token Plan usage endpoint onto the
// domain quota snapshot.
//
// The endpoint serves (at least) two shapes, both supported here:
//
//  1. current, fields at the top level: {"base_resp":…, "model_remains":[…]}
//     with per-entry current_{interval,weekly}_remaining_percent and
//     epoch-ms start_time/end_time;
//  2. legacy, count-based under "data": current_*_{total,usage}_count with
//     RFC3339 reset strings.
//
// Two upstream behaviors are load-bearing and deliberately encoded:
//
//   - *_usage_count fields are mislabeled: they report REMAINING quota,
//     not consumed usage (MiniMax-M2 issue #99). Nothing here ever
//     computes total−usage.
//   - remains_time / weekly_remains_time are real-time countdowns of a
//     time budget that drains even with zero API calls (MiniMax-M2.7
//     issue #47). They are neither quota nor reset times and are ignored.
package minimax

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// Endpoint is the official, fixed Token Plan usage URL.
const Endpoint = "https://www.minimax.io/v1/token_plan/remains"

// requestTimeout bounds the whole refresh (remote provider).
const requestTimeout = 10 * time.Second

// crossCheckTolerance is the allowed disagreement between the fraction
// implied by counts and the reported remaining percent before the percent
// wins and a warning is logged.
const crossCheckTolerance = 0.05

// authFailureCodes are provider error codes meaning bad credentials.
var authFailureCodes = map[int64]bool{401: true, 403: true, 1004: true}

type baseResp struct {
	StatusCode *int64 `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

type modelRemainEntry struct {
	// Legacy per-model counts ("remains" is remaining, matching the
	// usage-count semantics above).
	Remains *int64 `json:"remains"`
	Total   *int64 `json:"total"`

	Model     string `json:"model"`      // legacy name field
	ModelName string `json:"model_name"` // current name field

	CurIntervalRemainingPercent *float64 `json:"current_interval_remaining_percent"`
	CurWeeklyRemainingPercent   *float64 `json:"current_weekly_remaining_percent"`

	// Time-budget countdowns; ignored on purpose (see package comment).
	RemainsTime       *int64 `json:"remains_time"`
	WeeklyRemainsTime *int64 `json:"weekly_remains_time"`

	// Window bounds, epoch milliseconds.
	StartTime *int64 `json:"start_time"`
	EndTime   *int64 `json:"end_time"`
}

type tokenPlanData struct {
	CurIntervalTotal *int64 `json:"current_interval_total_count"`
	CurIntervalUsed  *int64 `json:"current_interval_usage_count"` // remaining (issue #99)
	CurIntervalReset string `json:"current_interval_reset_time"`  // RFC3339, legacy shape

	CurWeeklyTotal *int64 `json:"current_weekly_total_count"`
	CurWeeklyUsed  *int64 `json:"current_weekly_usage_count"` // remaining (issue #99)
	CurWeeklyReset string `json:"current_weekly_reset_time"`

	ModelRemains []modelRemainEntry `json:"model_remains"`
}

type tokenPlanResp struct {
	BaseResp     *baseResp          `json:"base_resp"`
	Code         *int64             `json:"code"`
	Data         *tokenPlanData     `json:"data"`
	ModelRemains []modelRemainEntry `json:"model_remains"`
}

// name returns the entry's display name from whichever field is set.
func (e modelRemainEntry) name() string {
	if e.ModelName != "" {
		return e.ModelName
	}
	return e.Model
}

// Adapter polls the MiniMax Token Plan endpoint. It is config-independent
// (the endpoint is fixed), reading the key from the store at refresh time,
// so the manager never needs to rebuild it. Pointer type per manager
// contract.
type Adapter struct {
	creds  credentials.Store
	client *http.Client
	log    *slog.Logger

	// endpoint is Endpoint; tests point it at an httptest server.
	endpoint string
}

// New creates a MiniMax adapter using the subscription key from creds.
func New(creds credentials.Store, client *http.Client, log *slog.Logger) *Adapter {
	return &Adapter{creds: creds, client: client, log: log, endpoint: Endpoint}
}

// ID implements source.Source.
func (a *Adapter) ID() domain.SourceID { return domain.SourceMiniMax }

// RequestTimeout implements source.Source.
func (a *Adapter) RequestTimeout() time.Duration { return requestTimeout }

// Refresh implements source.Source.
func (a *Adapter) Refresh(ctx context.Context) (domain.Snapshot, error) {
	token, err := a.token(ctx)
	if err != nil {
		return nil, err
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)

	var resp tokenPlanResp
	if gerr := source.GetJSON(ctx, a.client, a.endpoint, header, &resp); gerr != nil {
		return nil, gerr
	}

	// HTTP-200 error envelopes: base_resp wins, then the legacy code.
	if perr := a.checkProviderError(resp); perr != nil {
		return nil, perr
	}

	// NB: keep the typed *source.Error in its own variable — assigning it
	// to the `err` interface above would wrap a nil pointer as non-nil.
	entry, agg, serr := selectEntry(resp)
	if serr != nil {
		return nil, serr
	}

	now := time.Now()
	windows, werr := buildWindows(entry, agg, a.log)
	if werr != nil {
		return nil, werr
	}

	return domain.QuotaSnapshot{
		Source:      domain.SourceMiniMax,
		CollectedAt: now,
		Windows:     windows,
	}, nil
}

// token resolves the subscription key; a missing key is a config problem
// surfaced to the user, a broken keychain a credentials problem.
func (a *Adapter) token(ctx context.Context) (string, error) {
	token, err := a.creds.Get(ctx, credentials.AccountMiniMax)
	if err == nil {
		return token, nil
	}
	if errors.Is(err, credentials.ErrNotFound) {
		return "", &source.Error{
			Category: source.CatCredentials,
			Detail:   "API key not set",
		}
	}
	var stateErr *credentials.StateError
	if errors.As(err, &stateErr) {
		return "", &source.Error{
			Category: source.CatCredentials,
			Detail:   "Keychain unavailable: " + stateErr.Message,
			Err:      stateErr,
		}
	}
	return "", &source.Error{
		Category: source.CatCredentials,
		Detail:   "Keychain read failed",
		Err:      err,
	}
}

// checkProviderError inspects the in-band error fields. status_msg is
// logged but never surfaced: provider messages are not UI text (NFR-3).
func (a *Adapter) checkProviderError(resp tokenPlanResp) *source.Error {
	code := int64(0)
	haveCode := false
	msg := ""
	if resp.BaseResp != nil && resp.BaseResp.StatusCode != nil {
		code, haveCode = *resp.BaseResp.StatusCode, true
		msg = resp.BaseResp.StatusMsg
	} else if resp.Code != nil {
		code, haveCode = *resp.Code, true
	}
	if !haveCode || code == 0 {
		return nil
	}

	a.log.Warn("minimax provider error", "code", code, "status_msg", msg)

	detail := fmt.Sprintf("provider code %d", code)
	if authFailureCodes[code] {
		return &source.Error{Category: source.CatAuth, Detail: detail}
	}
	return &source.Error{Category: source.CatInvalidResponse, Detail: detail}
}

// selectEntry picks the plan entry to report. Selection is by name
// semantics, never by array position: the literal "general" bucket first
// (the text/chat plan lane real payloads expose — the same bucket the
// known-good statusline integrations read), then the coding and text name
// lanes for other plan shapes, then a lone entry. Ambiguity without a
// match is an error naming the entries seen. A nil entry with non-nil agg
// means the legacy aggregate-only shape.
func selectEntry(resp tokenPlanResp) (entry *modelRemainEntry, agg *tokenPlanData, err *source.Error) {
	entries := resp.ModelRemains
	agg = resp.Data
	if len(entries) == 0 && agg != nil {
		entries = agg.ModelRemains
	}

	if len(entries) == 0 {
		if agg != nil {
			return nil, agg, nil // legacy aggregate-only payload
		}
		return nil, nil, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "no plan data in response",
		}
	}

	for i := range entries {
		if entries[i].name() == "general" {
			return &entries[i], agg, nil
		}
	}
	for i := range entries {
		if strings.Contains(strings.ToLower(entries[i].name()), "cod") {
			return &entries[i], agg, nil
		}
	}
	for i := range entries {
		if strings.Contains(strings.ToLower(entries[i].name()), "text") {
			return &entries[i], agg, nil
		}
	}
	if len(entries) == 1 {
		return &entries[0], agg, nil
	}

	names := make([]string, len(entries))
	for i := range entries {
		names[i] = entries[i].name()
	}
	return nil, nil, &source.Error{
		Category: source.CatInvalidResponse,
		Detail: fmt.Sprintf("cannot identify plan entry among %d models: %s",
			len(entries), strings.Join(names, ", ")),
	}
}

// buildWindows derives the 5-hour and weekly windows in strict preference
// order: remaining-percent first (covers time-based plans with 0/0
// counts), then count fields with issue-#99 semantics.
func buildWindows(entry *modelRemainEntry, agg *tokenPlanData, log *slog.Logger) ([]domain.QuotaWindow, *source.Error) {
	var intervalPct, weeklyPct *float64
	var intervalCounts, weeklyCounts countPair

	if entry != nil {
		intervalPct = entry.CurIntervalRemainingPercent
		weeklyPct = entry.CurWeeklyRemainingPercent
		if entry.Remains != nil && entry.Total != nil {
			// Legacy per-model counts outrank the aggregate block.
			intervalCounts = countPair{remaining: entry.Remains, total: entry.Total}
		}
	}
	if agg != nil {
		if intervalCounts.total == nil && agg.CurIntervalTotal != nil {
			intervalCounts = countPair{remaining: agg.CurIntervalUsed, total: agg.CurIntervalTotal}
		}
		if weeklyCounts.total == nil && agg.CurWeeklyTotal != nil {
			weeklyCounts = countPair{remaining: agg.CurWeeklyUsed, total: agg.CurWeeklyTotal}
		}
	}

	windows := make([]domain.QuotaWindow, 0, 2)

	if w, ok, mismatch := deriveWindow("interval", "5-hour", intervalPct, intervalCounts); ok {
		windows = append(windows, w)
		if mismatch {
			log.Warn("minimax: 5-hour percent and counts disagree; trusting percent")
		}
	} else {
		log.Warn("minimax: dropping unusable 5-hour window")
	}
	if w, ok, mismatch := deriveWindow("weekly", "Weekly", weeklyPct, weeklyCounts); ok {
		windows = append(windows, w)
		if mismatch {
			log.Warn("minimax: weekly percent and counts disagree; trusting percent")
		}
	} else {
		log.Warn("minimax: dropping unusable weekly window")
	}

	if len(windows) == 0 {
		return nil, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "no usable quota windows in response",
		}
	}

	// Reset times: current shape carries epoch-ms window bounds on the
	// entry; the legacy shape carries RFC3339 strings on the aggregate.
	// Attach by window ID — a dropped window shifts indices.
	if entry != nil && entry.EndTime != nil && *entry.EndTime > 0 {
		reset := time.UnixMilli(*entry.EndTime)
		setResetAt(windows, "interval", &reset)
	} else if agg != nil && agg.CurIntervalReset != "" {
		if reset, perr := time.Parse(time.RFC3339, agg.CurIntervalReset); perr == nil {
			setResetAt(windows, "interval", &reset)
		}
	}
	if agg != nil && agg.CurWeeklyReset != "" {
		if reset, perr := time.Parse(time.RFC3339, agg.CurWeeklyReset); perr == nil {
			setResetAt(windows, "weekly", &reset)
		}
	}

	return windows, nil
}

func setResetAt(windows []domain.QuotaWindow, id string, reset *time.Time) {
	for i := range windows {
		if windows[i].ID == id {
			windows[i].ResetAt = reset
			return
		}
	}
}

// countPair holds raw counts with issue-#99 semantics: "usage"/"remains"
// fields carry the REMAINING value.
type countPair struct {
	remaining *int64
	total     *int64
}

func (p countPair) usable() bool {
	return p.remaining != nil && p.total != nil && *p.total > 0 && *p.remaining >= 0 && *p.remaining <= *p.total
}

// deriveWindow builds one window from the preferred source: the remaining
// percent when present (verified semantics; also covers time-based plans
// with 0/0 counts), else the count fields with issue-#99 semantics. When
// both are present the counts are attached for display and any
// disagreement beyond the tolerance is reported via mismatch for the
// caller to log — the percent always wins.
func deriveWindow(id, name string, pct *float64, counts countPair) (w domain.QuotaWindow, ok bool, mismatch bool) {
	w = domain.QuotaWindow{ID: id, Name: name}

	switch {
	case pct != nil && *pct >= 0 && *pct <= 100:
		frac, err := domain.Fraction(*pct / 100)
		if err != nil {
			return w, false, false
		}
		w.RemainingFraction = frac
		if counts.usable() {
			attachCounts(&w, counts)
			countFrac := float64(*counts.remaining) / float64(*counts.total)
			mismatch = math.Abs(countFrac-*pct/100) > crossCheckTolerance
		}
	case counts.usable():
		frac, err := domain.Fraction(float64(*counts.remaining) / float64(*counts.total))
		if err != nil {
			return w, false, false
		}
		w.RemainingFraction = frac
		attachCounts(&w, counts)
	default:
		return w, false, false
	}

	w.UsedFraction = 1 - w.RemainingFraction
	return w, true, mismatch
}

// attachCounts copies the raw counts (issue-#99 orientation) onto a window.
func attachCounts(w *domain.QuotaWindow, counts countPair) {
	rem := float64(*counts.remaining)
	lim := float64(*counts.total)
	w.Remaining = &rem
	w.Limit = &lim
	w.Unit = "requests"
}
