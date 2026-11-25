// Package claude adapts the claude.ai subscription usage endpoint (the one
// Claude Code's own /usage screen calls) onto the domain quota snapshot.
//
// The endpoint is undocumented, so every field is nullable and unknown
// fields are ignored. The OAuth access token comes from the credentials
// file Claude Code owns. Hard rules, kept here in one place:
//
//   - Read-only: the token is NEVER refreshed (a refresh rotates the
//     refresh token and logs Claude Code out). Only the access token,
//     its expiry and the subscription type are decoded; the refresh token
//     and the MCP OAuth entries are never touched and the raw file text is
//     not retained.
//   - No request goes out with an expired token.
//   - Errors are fixed strings. File contents, parse errors (which can
//     quote the input), the token, headers and bodies never reach errors,
//     logs or the UI.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// UsageURL is the official usage endpoint (the "windows" read, which skips
// spend computation).
const UsageURL = "https://api.anthropic.com/api/oauth/usage?skip_spend=1"

const (
	betaHeader     = "oauth-2025-04-20"
	requestTimeout = 5 * time.Second
	maxModelName   = 40
)

// Window identities.
const (
	idFiveHour = "claude-5h"
	idWeekly   = "claude-7d"
	idModel    = "claude-model-"
)

// Fixed user-facing messages.
const (
	msgMissing    = "credentials file missing"
	msgInvalid    = "credentials file invalid"
	msgNoLogin    = "no claude.ai login in credentials"
	msgExpired    = "token expired, run claude to refresh"
	msgRejected   = "auth rejected, run claude to log in again"
	msgBackingOff = "rate limited, backing off"
)

type credentialsFile struct {
	ClaudeAiOauth *struct {
		AccessToken      string   `json:"accessToken"`
		ExpiresAt        *float64 `json:"expiresAt"` // epoch milliseconds
		SubscriptionType string   `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

type window struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type limitEntry struct {
	Kind  string `json:"kind"`
	Scope *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
	Percent  *float64 `json:"percent"`
	ResetsAt *string  `json:"resets_at"`
}

type extraUsage struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
	Currency     *string  `json:"currency"`
}

// breakdown is the weekly limit split by product: percent_of_limit is each
// product's share of the whole limit.
type breakdown struct {
	Rows []struct {
		DisplayName    string   `json:"display_name"`
		PercentOfLimit *float64 `json:"percent_of_limit"`
	} `json:"rows"`
}

type usageResponse struct {
	FiveHour       *window      `json:"five_hour"`
	SevenDay       *window      `json:"seven_day"`
	SevenDayOpus   *window      `json:"seven_day_opus"`
	SevenDaySonnet *window      `json:"seven_day_sonnet"`
	Limits         []limitEntry `json:"limits"`
	ExtraUsage     *extraUsage  `json:"extra_usage"`
	// Breakdown stays raw and is decoded best-effort (normalizeShares): a
	// renamed or retyped field must never fail the headline quota decode.
	Breakdown json.RawMessage `json:"seven_day_breakdown"`
}

// credential is the minimal copy kept from the credentials file.
type credential struct {
	token     string
	expiresAt time.Time // zero when the file has none
	plan      string
}

// Adapter polls the usage endpoint. Pointer type: the manager detects
// configuration changes by pointer identity.
type Adapter struct {
	path     string
	url      string
	interval time.Duration
	client   *http.Client
	log      *slog.Logger
	now      func() time.Time

	mu          sync.Mutex
	nextAllowed time.Time
}

// New creates a claude.ai adapter reading the credentials file at path.
// interval is the configured poll interval; a 429 pauses requests for
// twice that.
func New(path string, interval time.Duration, client *http.Client, log *slog.Logger) *Adapter {
	return newAdapter(path, UsageURL, interval, client, log)
}

func newAdapter(path, url string, interval time.Duration, client *http.Client, log *slog.Logger) *Adapter {
	return &Adapter{path: path, url: url, interval: interval, client: client, log: log, now: time.Now}
}

// Kind implements source.Source.
func (a *Adapter) Kind() domain.SourceKind { return domain.KindClaude }

// RequestTimeout implements source.Source. The 401 path may issue a
// second request after a credentials re-read.
func (a *Adapter) RequestTimeout() time.Duration { return 2 * requestTimeout }

// Refresh implements source.Source.
func (a *Adapter) Refresh(ctx context.Context) (domain.Snapshot, error) {
	a.mu.Lock()
	blocked := a.now().Before(a.nextAllowed)
	a.mu.Unlock()
	if blocked {
		return nil, &source.Error{Category: source.CatRateLimited, Detail: msgBackingOff}
	}

	cred, err := a.load()
	if err != nil {
		return nil, err
	}

	var resp usageResponse
	gerr := a.get(ctx, cred.token, &resp)
	if gerr != nil && gerr.Category == source.CatAuth {
		// Claude Code may have refreshed the file meanwhile: retry only
		// with a changed token.
		again, lerr := a.load()
		if lerr != nil || again.token == cred.token {
			return nil, authRejected()
		}
		cred = again
		if gerr = a.get(ctx, cred.token, &resp); gerr != nil && gerr.Category == source.CatAuth {
			return nil, authRejected()
		}
	}
	if gerr != nil {
		if gerr.Category == source.CatRateLimited {
			a.mu.Lock()
			a.nextAllowed = a.now().Add(2 * a.interval)
			a.mu.Unlock()
			a.log.Debug("claude: rate limited, pausing requests", "for", 2*a.interval)
		}

		return nil, gerr
	}

	snap, ok := a.normalize(resp, cred)
	if !ok {
		return nil, &source.Error{Category: source.CatInvalidResponse, Detail: "no usable quota limits in response"}
	}

	return snap, nil
}

func authRejected() *source.Error {
	return &source.Error{Category: source.CatAuth, Detail: msgRejected}
}

func (a *Adapter) get(ctx context.Context, token string, dst *usageResponse) *source.Error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	header.Set("anthropic-beta", betaHeader)
	header.Set("Content-Type", "application/json")

	return source.GetJSON(ctx, a.client, a.url, header, dst)
}

// load reads the credentials file fresh. It returns fixed-message errors
// only; the decode error is deliberately dropped because it can quote the
// file.
func (a *Adapter) load() (credential, error) {
	data, err := os.ReadFile(a.path)
	if err != nil {
		return credential{}, &source.Error{Category: source.CatCredentials, Detail: msgMissing}
	}
	var f credentialsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return credential{}, &source.Error{Category: source.CatCredentials, Detail: msgInvalid}
	}
	clear(data)
	if f.ClaudeAiOauth == nil || f.ClaudeAiOauth.AccessToken == "" {
		return credential{}, &source.Error{Category: source.CatCredentials, Detail: msgNoLogin}
	}
	o := f.ClaudeAiOauth
	c := credential{token: o.AccessToken, plan: strings.TrimSpace(o.SubscriptionType)}
	if o.ExpiresAt != nil && !math.IsNaN(*o.ExpiresAt) && *o.ExpiresAt > 0 {
		c.expiresAt = time.UnixMilli(int64(*o.ExpiresAt))
		if !c.expiresAt.After(a.now()) {
			return credential{}, &source.Error{Category: source.CatCredentials, Detail: msgExpired}
		}
	}

	return c, nil
}

// normalize maps the response onto a snapshot; ok is false when neither
// headline window (5-hour, weekly) is present.
func (a *Adapter) normalize(r usageResponse, cred credential) (domain.QuotaSnapshot, bool) {
	var windows []domain.QuotaWindow
	add := func(id, name string, util *float64, resets *string) {
		if util == nil || math.IsNaN(*util) || math.IsInf(*util, 0) || *util < 0 {
			return
		}
		// Utilization may legitimately exceed 100; QuotaWindow is a
		// [0,1] contract everywhere downstream, so it is clamped here.
		used := domain.Clamp01(*util / 100)
		windows = append(windows, domain.QuotaWindow{
			ID: id, Name: name,
			UsedFraction: used, RemainingFraction: 1 - used,
			ResetAt: parseTime(resets),
		})
	}
	if r.FiveHour != nil {
		add(idFiveHour, "5-hour", r.FiveHour.Utilization, r.FiveHour.ResetsAt)
	}
	if r.SevenDay != nil {
		add(idWeekly, "Weekly", r.SevenDay.Utilization, r.SevenDay.ResetsAt)
	}
	headline := len(windows)

	seen := map[string]bool{}
	model := func(name string, util *float64, resets *string) {
		name = strings.TrimSpace(name)
		if r := []rune(name); len(r) > maxModelName {
			name = string(r[:maxModelName])
		}
		key := strings.ToLower(name)
		if name == "" || seen[key] {
			return
		}
		before := len(windows)
		id := slug(key)
		if id == "" {
			id = fmt.Sprintf("%x", key)
		}
		add(idModel+id, name+" weekly", util, resets)
		if len(windows) > before {
			seen[key] = true
		}
	}
	if r.SevenDayOpus != nil {
		model("Opus", r.SevenDayOpus.Utilization, r.SevenDayOpus.ResetsAt)
	}
	if r.SevenDaySonnet != nil {
		model("Sonnet", r.SevenDaySonnet.Utilization, r.SevenDaySonnet.ResetsAt)
	}
	for _, l := range r.Limits {
		if l.Kind != "weekly_scoped" || l.Scope == nil || l.Scope.Model == nil {
			continue
		}
		model(l.Scope.Model.DisplayName, l.Percent, l.ResetsAt)
	}

	// Only per-model windows (or none) leave no headline quota to show.
	if headline == 0 {
		return domain.QuotaSnapshot{}, false
	}

	return domain.QuotaSnapshot{
		Kind:        domain.KindClaude,
		CollectedAt: a.now(),
		Plan:        titleCase(cred.plan),
		Windows:     windows,
		ExtraUsage:  normalizeExtra(r.ExtraUsage),
		Shares:      normalizeShares(r.Breakdown),
	}, true
}

// normalizeShares keeps the breakdown rows that carry a name and a finite,
// non-negative share. Names are trimmed and rune-capped like model names. A
// missing or malformed block yields no shares, never an error.
func normalizeShares(raw json.RawMessage) []domain.QuotaShare {
	if len(raw) == 0 {
		return nil
	}
	var b breakdown
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil
	}
	var out []domain.QuotaShare
	for _, row := range b.Rows {
		name := strings.TrimSpace(row.DisplayName)
		if r := []rune(name); len(r) > maxModelName {
			name = string(r[:maxModelName])
		}
		p := row.PercentOfLimit
		if name == "" || p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) || *p < 0 {
			continue
		}
		out = append(out, domain.QuotaShare{Name: name, PercentOfLimit: *p})
	}

	return out
}

// slug lowercases a model name into a stable window-ID suffix.
func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}

	return strings.Trim(b.String(), "-")
}

func titleCase(s string) string {
	if s == "" {
		return ""
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

func parseTime(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil
	}

	return &t
}

// zeroDecimal lists the currencies whose amounts are not in hundredths.
var zeroDecimal = map[string]bool{"JPY": true, "KRW": true, "VND": true}

func normalizeExtra(x *extraUsage) *domain.QuotaExtraUsage {
	if x == nil {
		return nil
	}
	out := &domain.QuotaExtraUsage{Enabled: x.IsEnabled}
	if !x.IsEnabled {
		return out
	}
	if x.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*x.Currency))
		if len(c) > 3 {
			c = c[:3]
		}
		out.Currency = c
	}
	div := 100.0
	if zeroDecimal[out.Currency] {
		div = 1
	}
	conv := func(v *float64) *float64 {
		if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
			return nil
		}
		c := *v / div

		return &c
	}
	out.Used, out.Limit = conv(x.UsedCredits), conv(x.MonthlyLimit)
	if x.Utilization != nil && !math.IsNaN(*x.Utilization) && !math.IsInf(*x.Utilization, 0) {
		u := *x.Utilization
		out.Utilization = &u
	}

	return out
}
