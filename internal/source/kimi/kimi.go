// Package kimi adapts the Kimi Code plan usage endpoint onto the domain
// quota snapshot.
//
// The endpoint is the one Kimi Code CLI's /usage command calls. It is
// undocumented, so every field is decoded loosely and absence stays absence
// (docs/INTEGRATIONS.md, Kimi section). The 5-hour burst window comes only
// from limits[] (selected by its duration, never by array position): the
// newer usages.limit_5h block duplicates it and is known to go stale. The
// monthly pool comes from usages.limit_month_total; limit_month_code is a
// slice of that pool, not a separate allowance, and is skipped.
package kimi

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

const (
	usagesPath = "/coding/v1/usages"
	mePath     = "/coding/v1/me"

	// requestTimeout bounds the whole refresh.
	requestTimeout = 10 * time.Second
	// meTimeout bounds the optional plan-name request on its own so a slow
	// /me never eats the usage refresh's budget.
	meTimeout = 4 * time.Second

	// planTTL is how long a fetched plan name is trusted; the tier changes
	// rarely, and the membership level in the usage payload triggers an
	// earlier refetch when it does.
	planTTL = 24 * time.Hour
	// planRetryAfter spaces /me retries after a failure.
	planRetryAfter = time.Hour

	burstMinutes = 300
)

// Window identities.
const (
	idBurst   = "kimi-5h"
	idMonthly = "kimi-monthly"
	idWeekly  = "kimi-weekly"
)

// block is one usage entry in any of its observed shapes: absolute counts
// (limit/used/remaining, possibly strings) or a consumed ratio, with the
// reset time under any of its aliases.
type block struct {
	Limit     any `json:"limit"`
	Used      any `json:"used"`
	Remaining any `json:"remaining"`
	UsedRatio any `json:"used_ratio"`

	ResetTime      any `json:"resetTime"`
	ResetAt        any `json:"resetAt"`
	ResetAtSnake   any `json:"reset_at"`
	ResetTimeSnake any `json:"reset_time"`
}

type limitEntry struct {
	Window struct {
		Duration any    `json:"duration"`
		TimeUnit string `json:"timeUnit"`
	} `json:"window"`
	Detail block `json:"detail"`
}

type response struct {
	Limits []limitEntry `json:"limits"`
	Usages struct {
		MonthTotal *block `json:"limit_month_total"`
	} `json:"usages"`
	// Usage is the legacy singular weekly block.
	Usage *block `json:"usage"`
	User  struct {
		Membership struct {
			Level string `json:"level"`
		} `json:"membership"`
	} `json:"user"`
}

type meResponse struct {
	UserLevelName string `json:"user_level_name"`
}

// Adapter polls the Kimi Code usage endpoint. Pointer type: the manager
// detects configuration changes by pointer identity.
type Adapter struct {
	baseURL string
	account credentials.AccountID
	creds   credentials.Store
	client  *http.Client
	log     *slog.Logger
	now     func() time.Time

	mu       sync.Mutex
	planName string
	planFor  string // membership level the cached name was fetched under
	planAt   time.Time
	failAt   time.Time
}

// New creates a Kimi adapter for the given base URL, reading the plan key
// from the instance-scoped account on each refresh.
func New(baseURL string, account credentials.AccountID, creds credentials.Store, client *http.Client, log *slog.Logger) *Adapter {
	return &Adapter{baseURL: baseURL, account: account, creds: creds, client: client, log: log, now: time.Now}
}

// Kind implements source.Source.
func (a *Adapter) Kind() domain.SourceKind { return domain.KindKimi }

// RequestTimeout implements source.Source. It covers the usage request and
// the optional, separately bounded plan-name request.
func (a *Adapter) RequestTimeout() time.Duration { return requestTimeout + meTimeout }

// Refresh implements source.Source.
func (a *Adapter) Refresh(ctx context.Context) (domain.Snapshot, error) {
	token, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	header.Set("Accept", "application/json")

	var resp response
	if gerr := source.GetJSON(ctx, a.client, a.baseURL+usagesPath, header, &resp); gerr != nil {
		return nil, gerr
	}

	var windows []domain.QuotaWindow
	if w, ok := burstWindow(resp.Limits); ok {
		windows = append(windows, w)
	}
	if resp.Usages.MonthTotal != nil {
		if w, ok := blockWindow(idMonthly, "Monthly", *resp.Usages.MonthTotal); ok {
			windows = append(windows, w)
		}
	}
	if resp.Usage != nil {
		if w, ok := blockWindow(idWeekly, "Weekly", *resp.Usage); ok {
			windows = append(windows, w)
		}
	}
	if len(windows) == 0 {
		return nil, &source.Error{Category: source.CatInvalidResponse, Detail: "no usable quota limits in response"}
	}

	level := strings.TrimSpace(resp.User.Membership.Level)

	return domain.QuotaSnapshot{
		Kind:        domain.KindKimi,
		CollectedAt: a.now(),
		Plan:        a.plan(ctx, header, level),
		Windows:     windows,
	}, nil
}

// plan resolves the plan label. The tier name comes from a best-effort,
// cached /me request, made only when nothing is cached, the membership
// level changed, or the cache is older than planTTL, and never sooner than
// planRetryAfter after a failure. Any failure falls back to the prettified
// membership level; it never fails the refresh.
func (a *Adapter) plan(ctx context.Context, header http.Header, level string) string {
	a.mu.Lock()
	now := a.now()
	fresh := a.planName != "" && a.planFor == level && now.Sub(a.planAt) < planTTL
	backoff := !a.failAt.IsZero() && now.Sub(a.failAt) < planRetryAfter
	cached := a.planName
	a.mu.Unlock()

	if fresh {
		return cached
	}
	if !backoff {
		if name, ok := a.fetchPlanName(ctx, header); ok {
			a.mu.Lock()
			a.planName, a.planFor, a.planAt, a.failAt = name, level, now, time.Time{}
			a.mu.Unlock()

			return name
		}
		a.mu.Lock()
		a.failAt = now
		a.mu.Unlock()
	}
	if cached != "" && a.planFor == level {
		return cached
	}

	return prettyLevel(level)
}

func (a *Adapter) fetchPlanName(ctx context.Context, header http.Header) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, meTimeout)
	defer cancel()

	var me meResponse
	if gerr := source.GetJSON(ctx, a.client, a.baseURL+mePath, header, &me); gerr != nil {
		a.log.Debug("kimi: plan name request failed", "category", gerr.Category)

		return "", false
	}
	name := strings.TrimSpace(me.UserLevelName)

	return name, name != ""
}

// prettyLevel turns LEVEL_INTERMEDIATE into "Intermediate"; empty stays
// empty (no plan label rather than an invented one).
func prettyLevel(level string) string {
	level = strings.TrimPrefix(strings.TrimSpace(level), "LEVEL_")
	if level == "" {
		return ""
	}
	level = strings.ReplaceAll(strings.ToLower(level), "_", " ")

	return strings.ToUpper(level[:1]) + level[1:]
}

// burstWindow picks the 5-hour entry of limits[] by its duration.
func burstWindow(entries []limitEntry) (domain.QuotaWindow, bool) {
	for _, e := range entries {
		minutes, ok := windowMinutes(e.Window.Duration, e.Window.TimeUnit)
		if !ok || minutes != burstMinutes {
			continue
		}

		return blockWindow(idBurst, "5-hour", e.Detail)
	}

	return domain.QuotaWindow{}, false
}

func windowMinutes(duration any, unit string) (float64, bool) {
	d, ok := toFloat(duration)
	if !ok || d <= 0 {
		return 0, false
	}
	switch strings.ToUpper(unit) {
	case "TIME_UNIT_MINUTE":
		return d, true
	case "TIME_UNIT_HOUR":
		return d * 60, true
	case "TIME_UNIT_DAY":
		return d * 1440, true
	case "TIME_UNIT_SECOND":
		return d / 60, true
	}

	return 0, false
}

// blockWindow normalizes one entry. Counts win when present and sane
// (limit > 0); otherwise the consumed ratio is used. An entry with neither
// yields no window.
func blockWindow(id, name string, b block) (domain.QuotaWindow, bool) {
	w := domain.QuotaWindow{ID: id, Name: name}

	limit, hasLimit := toFloat(b.Limit)
	if hasLimit && limit > 0 {
		used, hasUsed := toFloat(b.Used)
		if !hasUsed {
			if rem, ok := toFloat(b.Remaining); ok {
				used, hasUsed = limit-rem, true
			}
		}
		if hasUsed && used >= 0 {
			frac := domain.Clamp01(used / limit)
			rem := math.Max(limit-used, 0)
			w.UsedFraction, w.RemainingFraction = frac, 1-frac
			w.Limit, w.Remaining = &limit, &rem
			w.ResetAt = resetTime(b)

			return w, true
		}
	}
	if ratio, ok := toFloat(b.UsedRatio); ok && !math.IsNaN(ratio) && ratio >= 0 {
		frac := domain.Clamp01(ratio)
		w.UsedFraction, w.RemainingFraction = frac, 1-frac
		w.ResetAt = resetTime(b)

		return w, true
	}

	return domain.QuotaWindow{}, false
}

func resetTime(b block) *time.Time {
	for _, v := range []any{b.ResetTime, b.ResetAt, b.ResetAtSnake, b.ResetTimeSnake} {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return &t
		}
	}

	return nil
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, !math.IsNaN(t) && !math.IsInf(t, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)

		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	default:
		return 0, false
	}
}

// token resolves the plan key; a missing key is a credentials problem
// surfaced to the user, as is a broken keychain.
func (a *Adapter) token(ctx context.Context) (string, error) {
	token, err := a.creds.Get(ctx, a.account)
	if err == nil {
		return token, nil
	}
	if errors.Is(err, credentials.ErrNotFound) {
		return "", &source.Error{Category: source.CatCredentials, Detail: "API key not set"}
	}
	var stateErr *credentials.StateError
	if errors.As(err, &stateErr) {
		return "", &source.Error{
			Category: source.CatCredentials,
			Detail:   "Keychain unavailable: " + stateErr.Message,
			Err:      stateErr,
		}
	}

	return "", &source.Error{Category: source.CatCredentials, Detail: "Keychain read failed", Err: err}
}
