// Package zai adapts the Z.AI / GLM Coding Plan usage-quota endpoint onto
// the domain quota snapshot.
//
// The endpoint reports usage-oriented percentages whose scale is
// undocumented (0–100 or 0–1). Wherever an entry also carries numeric
// currentValue/usage, the scale is resolved per entry by cross-check;
// only ambiguous entries fall back to the per-value rule. Reset times are
// not exposed by the endpoint and stay nil (S1 omission).
package zai

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
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

// limitEntry is one entry of limits[]. CurrentValue and Usage are decoded
// loosely: the upstream types them any and the cross-check only needs
// numbers when they are numbers.
type limitEntry struct {
	Type         string   `json:"type"`
	Percentage   *float64 `json:"percentage"`
	CurrentValue any      `json:"currentValue"`
	Usage        any      `json:"usage"`
	UsageDetails any      `json:"usageDetails"`
	Unit         string   `json:"unit"`
	Number       any      `json:"number"`
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

// windowSpec maps a limit entry type onto its stable window identity.
type windowSpec struct {
	ID   string
	Name string
}

// windowSpecs are the entry types with verified display semantics. The
// credit entry rides the same endpoint on newer international plans.
var windowSpecs = map[string]windowSpec{
	"TOKENS_LIMIT": {ID: "tokens-5h", Name: "Token usage · 5 hour"},
	"TIME_LIMIT":   {ID: "mcp-monthly", Name: "MCP usage · 1 month"},
	"CREDIT_LIMIT": {ID: "credits-5h", Name: "Credit usage · 5 hour"},
}

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
	for _, e := range limits {
		w, ok := a.entryWindow(e)
		if !ok {
			continue
		}
		windows = append(windows, w)
	}

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

// entryWindow normalizes one limits[] entry. Unknown types are skipped
// (forward-compatible); unusable percentages are dropped with a warning.
func (a *Adapter) entryWindow(e limitEntry) (domain.QuotaWindow, bool) {
	spec, known := windowSpecs[e.Type]
	if !known {
		a.log.Debug("zai: skipping unknown limit type", "type", e.Type)
		return domain.QuotaWindow{}, false
	}
	if e.Percentage == nil {
		a.log.Warn("zai: limit entry without percentage dropped", "type", e.Type)
		return domain.QuotaWindow{}, false
	}
	pct := *e.Percentage
	// The value must be valid under SOME scale: 0–1 (fraction) or 0–100
	// (percent). Anything else is garbage, not a rounding edge.
	if math.IsNaN(pct) || pct < 0 || pct > 100 {
		a.log.Warn("zai: out-of-range percentage dropped", "type", e.Type, "percentage", pct)
		return domain.QuotaWindow{}, false
	}

	used := domain.Clamp01(a.resolveUsedFraction(e, pct))

	w := domain.QuotaWindow{
		ID:                spec.ID,
		Name:              spec.Name,
		UsedFraction:      used,
		RemainingFraction: 1 - used,
		// Raw counts and units are unverified semantics: not attached.
		// The endpoint exposes no reset time: ResetAt stays nil.
	}
	return w, true
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
