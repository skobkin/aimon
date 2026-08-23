// Package zai adapts the Z.AI / GLM Coding Plan usage-quota endpoint onto
// the domain quota snapshot.
//
// Verified against real payloads (same endpoint the working statusline
// integrations read): entries carry numeric counts where usage is the
// LIMIT and remaining is what is left, plus an absolute epoch-ms
// nextResetTime. Counts have known semantics, so they are preferred; the
// usage-oriented percentage (whose scale is undocumented, 0–100 or 0–1)
// is only a fallback and is scale-resolved per entry by cross-check
// against currentValue/usage. Newer plans report two CREDIT_LIMIT entries
// — one per window — distinguishable only by reset time: the one resetting
// sooner is the 5-hour window, the later one the weekly window.
package zai

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/skobkin/llm-monitor/internal/credentials"
	"github.com/skobkin/llm-monitor/internal/domain"
	"github.com/skobkin/llm-monitor/internal/source"
)

// quotaPath is the usage-quota endpoint under the configured base URL.
const quotaPath = "/api/monitor/usage/quota/limit"

// requestTimeout bounds the whole refresh (remote provider).
const requestTimeout = 10 * time.Second

// Scale-resolution tolerances: a percentage matching ratio*100 within
// percentTol points indicates a 0–100 scale; matching ratio itself within
// fractionTol indicates a 0–1 scale.
const (
	percentTolerance  = 2.0
	fractionTolerance = 0.02
)

// countTolerance is the allowed disagreement between the fraction implied
// by the counts and the one implied by the percentage before the counts
// win and a warning is logged.
const countTolerance = 0.05

// Limit entry types observed on the endpoint.
const (
	typeTokens = "TOKENS_LIMIT"
	typeTime   = "TIME_LIMIT"
	typeCredit = "CREDIT_LIMIT"
)

// limitEntry is one entry of limits[]. The count and time fields are
// decoded loosely: upstream types them any and real payloads have been
// seen with numbers where a string was expected (unit) and vice versa.
type limitEntry struct {
	Type         string   `json:"type"`
	Percentage   *float64 `json:"percentage"`
	Remaining    any      `json:"remaining"`
	CurrentValue any      `json:"currentValue"`
	Usage        any      `json:"usage"`
	UsageDetails any      `json:"usageDetails"`
	Unit         any      `json:"unit"`
	Number       any      `json:"number"`
	NextResetMs  any      `json:"nextResetTime"`
}

// quotaData is the wrapped shape: {"data":{"limits":[…]}}.
type quotaData struct {
	Limits []limitEntry `json:"limits"`
}

// envelope accepts both the wrapped and the top-level shapes.
type envelope struct {
	Data   *quotaData   `json:"data"`
	Limits []limitEntry `json:"limits"`
}

// windowSpec maps a limit entry onto its stable window identity.
type windowSpec struct {
	ID   string
	Name string
}

// fixedSpecs are the entry types with a single, statically known window.
var fixedSpecs = map[string]windowSpec{
	typeTokens: {ID: "tokens-5h", Name: "Token usage · 5 hour"},
	typeTime:   {ID: "mcp-monthly", Name: "MCP usage · 1 month"},
}

// The two credit windows of newer Coding Plans: one CREDIT_LIMIT entry
// per window, told apart by reset time.
var (
	credit5hSpec     = windowSpec{ID: "credits-5h", Name: "Credit usage · 5 hour"}
	creditWeeklySpec = windowSpec{ID: "credits-weekly", Name: "Credit usage · Weekly"}
)

// Adapter polls the Z.AI usage-quota endpoint. Pointer type: the manager
// detects configuration changes by pointer identity.
type Adapter struct {
	baseURL string
	creds   credentials.Store
	client  *http.Client
	log     *slog.Logger
}

// New creates a Z.AI adapter for the given base URL (default
// https://api.z.ai).
func New(baseURL string, creds credentials.Store, client *http.Client, log *slog.Logger) *Adapter {
	return &Adapter{baseURL: baseURL, creds: creds, client: client, log: log}
}

// ID implements source.Source.
func (a *Adapter) ID() domain.SourceID { return domain.SourceZAI }

// RequestTimeout implements source.Source.
func (a *Adapter) RequestTimeout() time.Duration { return requestTimeout }

// Refresh implements source.Source.
func (a *Adapter) Refresh(ctx context.Context) (domain.Snapshot, error) {
	token, err := a.token(ctx)
	if err != nil {
		return nil, err
	}

	header := http.Header{}
	// Upstream expects the raw token, NOT a Bearer-prefixed one.
	header.Set("Authorization", token)
	header.Set("Accept-Language", "en-US,en")
	header.Set("Content-Type", "application/json")

	var env envelope
	if gerr := source.GetJSON(ctx, a.client, a.baseURL+quotaPath, header, &env); gerr != nil {
		return nil, gerr
	}

	limits := env.Limits
	if env.Data != nil && len(env.Data.Limits) > 0 {
		limits = env.Data.Limits
	}

	windows := make([]domain.QuotaWindow, 0, len(limits))
	var credits []limitEntry
	for _, e := range limits {
		if e.Type == typeCredit {
			// Credit entries are window-assigned only after all of
			// them are known (their order is reset-time dependent).
			credits = append(credits, e)
			continue
		}
		spec, known := fixedSpecs[e.Type]
		if !known {
			a.log.Debug("zai: skipping unknown limit type", "type", e.Type)
			continue
		}
		if w, ok := a.limitWindow(e, spec); ok {
			windows = append(windows, w)
		}
	}
	windows = append(windows, a.creditWindows(credits)...)

	if len(windows) == 0 {
		return nil, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "no usable quota limits in response",
		}
	}

	return domain.QuotaSnapshot{
		Source:      domain.SourceZAI,
		CollectedAt: time.Now(),
		Windows:     windows,
	}, nil
}

// token resolves the plan token; a missing token is a config problem
// surfaced to the user, a broken keychain a credentials problem.
func (a *Adapter) token(ctx context.Context) (string, error) {
	token, err := a.creds.Get(ctx, credentials.AccountZAI)
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

// creditWindows maps the CREDIT_LIMIT entries onto the 5-hour and weekly
// windows. The entries carry no window name; the one resetting sooner is
// the 5-hour window, the one resetting later the weekly one. A lone entry
// can only be the 5-hour window; beyond two, the extremes by reset time
// win and the middle ones are skipped.
func (a *Adapter) creditWindows(entries []limitEntry) []domain.QuotaWindow {
	if len(entries) == 0 {
		return nil
	}

	sorted := append([]limitEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return resetKey(sorted[i]) < resetKey(sorted[j])
	})
	for _, e := range sorted {
		if resetKey(e) == math.MaxInt64 {
			a.log.Warn("zai: credit limit entry without nextResetTime; window ordering may be wrong")
			break
		}
	}

	if len(sorted) == 1 {
		a.log.Debug("zai: single credit limit entry; treating it as the 5-hour window")
		if w, ok := a.limitWindow(sorted[0], credit5hSpec); ok {
			return []domain.QuotaWindow{w}
		}
		return nil
	}
	if extra := len(sorted) - 2; extra > 0 {
		a.log.Warn("zai: more than two credit limit entries; keeping the first and last by reset time", "extra", extra)
	}

	var out []domain.QuotaWindow
	if w, ok := a.limitWindow(sorted[0], credit5hSpec); ok {
		out = append(out, w)
	}
	if w, ok := a.limitWindow(sorted[len(sorted)-1], creditWeeklySpec); ok {
		out = append(out, w)
	}
	return out
}

// resetKey is the entry's reset time as a sortable integer; entries
// without a usable one sort last.
func resetKey(e limitEntry) int64 {
	if ms, ok := toFloat(e.NextResetMs); ok && ms > 0 {
		return int64(ms)
	}
	return math.MaxInt64
}

// limitWindow normalizes one limits[] entry under its window spec. The
// counts are preferred (verified semantics: usage is the limit, remaining
// what is left); the scale-ambiguous percentage is the fallback. Entries
// with neither are dropped with a warning.
func (a *Adapter) limitWindow(e limitEntry, spec windowSpec) (domain.QuotaWindow, bool) {
	w := domain.QuotaWindow{ID: spec.ID, Name: spec.Name}

	rem, haveRem := toFloat(e.Remaining)
	lim, haveLim := toFloat(e.Usage)
	if haveRem && haveLim && (lim <= 0 || rem < 0 || rem > lim) {
		a.log.Warn("zai: nonsensical counts ignored", "type", e.Type, "remaining", rem, "usage", lim)
		haveRem, haveLim = false, false
	}

	switch {
	case haveRem && haveLim:
		frac, err := domain.Fraction(rem / lim)
		if err != nil {
			a.log.Warn("zai: count fraction out of range; entry dropped", "type", e.Type)
			return domain.QuotaWindow{}, false
		}
		w.RemainingFraction = frac
		r, l := rem, lim
		w.Remaining, w.Limit = &r, &l
		// A string unit names the counts; numeric unit codes carry no
		// verified display meaning and are ignored.
		if s, ok := e.Unit.(string); ok {
			w.Unit = s
		}
		if used := a.usedFractionFromPercentage(e); used != nil &&
			math.Abs(*used-(1-frac)) > countTolerance {
			a.log.Warn("zai: percentage and counts disagree; trusting counts", "type", e.Type)
		}
	case e.Percentage != nil:
		used := a.usedFractionFromPercentage(e)
		if used == nil {
			return domain.QuotaWindow{}, false
		}
		w.RemainingFraction = 1 - *used
	default:
		a.log.Warn("zai: limit entry without usable counts or percentage dropped", "type", e.Type)
		return domain.QuotaWindow{}, false
	}

	w.UsedFraction = 1 - w.RemainingFraction

	if t, ok := epochMilli(e.NextResetMs); ok {
		w.ResetAt = &t
	}
	return w, true
}

// usedFractionFromPercentage returns the used fraction implied by the
// entry's usage-oriented percentage, resolving its scale; nil when the
// percentage is absent or unusable.
func (a *Adapter) usedFractionFromPercentage(e limitEntry) *float64 {
	if e.Percentage == nil {
		return nil
	}
	pct := *e.Percentage
	// The value must be valid under SOME scale: 0–1 (fraction) or 0–100
	// (percent). Anything else is garbage, not a rounding edge.
	if math.IsNaN(pct) || pct < 0 || pct > 100 {
		a.log.Warn("zai: out-of-range percentage ignored", "type", e.Type, "percentage", pct)
		return nil
	}
	used := domain.Clamp01(a.resolveUsedFraction(e, pct))
	return &used
}

// resolveUsedFraction converts a usage-oriented percentage to a used
// fraction, resolving the scale per entry: when currentValue and usage
// are both numeric, whichever scale reproduces the percentage wins; only
// genuinely ambiguous entries fall back to the per-value rule.
func (a *Adapter) resolveUsedFraction(e limitEntry, pct float64) float64 {
	current, haveCurrent := toFloat(e.CurrentValue)
	usage, haveUsage := toFloat(e.Usage)

	if haveCurrent && haveUsage && usage > 0 {
		ratio := current / usage
		switch {
		case math.Abs(pct-ratio*100) <= percentTolerance:
			return pct / 100
		case math.Abs(pct-ratio) <= fractionTolerance:
			return pct
		}
		a.log.Warn("zai: percentage matches neither scale via cross-check; using per-value rule", "percentage", pct)
	}

	// Per-value fallback: values above 1 can only be a 0–100 percent.
	if pct > 1.0 {
		return pct / 100
	}
	return pct
}

// toFloat extracts a number from a loosely decoded JSON value. Numbers
// arrive as float64 (plain unmarshal); strings are tolerated.
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// epochMilli converts an absolute epoch-milliseconds value (loosely
// decoded) to a time.
func epochMilli(v any) (time.Time, bool) {
	ms, ok := toFloat(v)
	if !ok || ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(int64(ms)), true
}
