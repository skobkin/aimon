// Package openrouter adapts the two OpenRouter account-capability endpoints
// onto one domain snapshot (docs/S4_OPENROUTER.md):
//
//	GET https://openrouter.ai/api/v1/key      <- ordinary API key only
//	GET https://openrouter.ai/api/v1/credits  <- Management key only
//
// Credentials are capabilities: each endpoint is called only when its own
// credential slot holds a value, never with the other slot's token as a
// fallback, and an instance with neither configured makes zero requests.
// The key endpoint returns spend values plus one optional spending limit;
// it exposes no reset timestamp (only a cadence label), so none is ever
// synthesized.
package openrouter

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"git.skobk.in/skobkin/aimonitor/internal/credentials"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/source"
)

// KeyEndpoint is the current-API-key capability endpoint; CreditsEndpoint is
// the account-credit capability endpoint. Both are official and fixed.
const (
	KeyEndpoint     = "https://openrouter.ai/api/v1/key"
	CreditsEndpoint = "https://openrouter.ai/api/v1/credits" //nolint:gosec // endpoint URL, not a credential
)

// requestTimeout bounds the whole refresh (remote provider).
const requestTimeout = 10 * time.Second

// balanceNoiseEpsilon bounds the binary-representation noise below which a
// derived balance is clamped to zero. A materially negative balance survives.
const balanceNoiseEpsilon = 1e-9

// keyEnvelope / creditsEnvelope split the documented {"data": …} wrappers.
// OpenRouter may serve other undocumented envelope shapes around errors;
// only the documented data object is interpreted.
type keyEnvelope struct {
	Data *keyData `json:"data"`
}

type keyData struct {
	// Usage fields are REQUIRED for a usable key capability; they are
	// pointers so a missing/null field is detected instead of read as zero.
	Usage        *float64 `json:"usage"`
	UsageDaily   *float64 `json:"usage_daily"`
	UsageWeekly  *float64 `json:"usage_weekly"`
	UsageMonthly *float64 `json:"usage_monthly"`

	// BYOK equivalents are optional and retained but not displayed in S4.
	BYOKUsage          *float64 `json:"byok_usage"`
	BYOKUsageDaily     *float64 `json:"byok_usage_daily"`
	BYOKUsageWeekly    *float64 `json:"byok_usage_weekly"`
	BYOKUsageMonthly   *float64 `json:"byok_usage_monthly"`
	IncludeBYOKInLimit *bool    `json:"include_byok_in_limit"`

	// The single per-key spending cap and its remaining amount. Both
	// optional; limit_reset is a cadence label, not three quotas.
	Limit          *float64 `json:"limit"`
	LimitReset     *string  `json:"limit_reset"`
	LimitRemaining *float64 `json:"limit_remaining"`

	// Key-type flags. Their only role here is refusing a Management or
	// provisioning key in the ordinary api-key slot WITHOUT probing: no
	// extra request is ever made to classify a credential.
	IsManagementKey   *bool `json:"is_management_key"`
	IsProvisioningKey *bool `json:"is_provisioning_key"`
}

type creditsEnvelope struct {
	Data *creditsData `json:"data"`
}

type creditsData struct {
	TotalCredits *float64 `json:"total_credits"`
	TotalUsage   *float64 `json:"total_usage"`
}

// Adapter polls the OpenRouter capability endpoints. Pointer type per the
// manager contract (replacement is detected by pointer identity).
type Adapter struct {
	// One stable account per credential slot; tokens never interchange.
	apiKeyAccount  credentials.AccountID
	mgmtKeyAccount credentials.AccountID
	creds          credentials.Store
	client         *http.Client
	log            *slog.Logger

	// Endpoints are the fixed production URLs; tests point them at an
	// httptest server without exposing a base URL in user configuration.
	keyEndpoint     string
	creditsEndpoint string
}

// New creates an adapter reading the two instance-scoped keychain slots.
func New(apiKeyAccount, mgmtKeyAccount credentials.AccountID, creds credentials.Store, client *http.Client, log *slog.Logger) *Adapter {
	return &Adapter{
		apiKeyAccount:   apiKeyAccount,
		mgmtKeyAccount:  mgmtKeyAccount,
		creds:           creds,
		client:          client,
		log:             log,
		keyEndpoint:     KeyEndpoint,
		creditsEndpoint: CreditsEndpoint,
	}
}

// Kind implements source.Source.
func (a *Adapter) Kind() domain.SourceKind { return domain.KindOpenRouter }

// RequestTimeout implements source.Source.
func (a *Adapter) RequestTimeout() time.Duration { return requestTimeout }

// Refresh implements source.Source: fetch every CONFIGURED capability and
// combine them into one snapshot. Any configured request failing fails the
// refresh so the manager keeps the last good snapshot and marks the source
// stale. Zero configured capabilities is a local credential problem reached
// before any network activity.
func (a *Adapter) Refresh(ctx context.Context) (domain.Snapshot, error) {
	key, kerr := a.FetchKeyUsage(ctx)
	if kerr != nil {
		return nil, kerr
	}
	account, aerr := a.FetchAccountCredits(ctx)
	if aerr != nil {
		return nil, aerr
	}
	if key == nil && account == nil {
		return nil, &source.Error{
			Category: source.CatCredentials,
			Detail:   "No API key or Management key configured",
		}
	}

	return domain.OpenRouterSnapshot{
		CollectedAt: time.Now(),
		Key:         key,
		Account:     account,
	}, nil
}

// FetchKeyUsage performs the API-key capability: per-key usage and the
// optional per-key limit. It resolves the api-key slot first and returns
// (nil, nil) when that slot has no stored credential — the endpoint is then
// not requested at all.
func (a *Adapter) FetchKeyUsage(ctx context.Context) (*domain.OpenRouterKeyUsage, error) {
	token, present, err := a.secret(ctx, a.apiKeyAccount)
	if err != nil || !present {
		return nil, err
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)

	var env keyEnvelope
	if gerr := source.GetJSON(ctx, a.client, a.keyEndpoint, header, &env); gerr != nil {
		return nil, gerr
	}
	if env.Data == nil {
		return nil, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "no key data object in response",
		}
	}
	// Keep the typed *source.Error in its own variable: returning it
	// directly would wrap a nil pointer as a non-nil error interface.
	usage, serr := normalizeKeyData(env.Data, a.log)
	if serr != nil {
		return nil, serr
	}

	return usage, nil
}

// FetchAccountCredits performs the Management-key capability: purchased
// credits and total account usage, with the balance derived here. It
// resolves the management-key slot first and returns (nil, nil) when that
// slot has no stored credential.
func (a *Adapter) FetchAccountCredits(ctx context.Context) (*domain.OpenRouterAccountCredits, error) {
	token, present, err := a.secret(ctx, a.mgmtKeyAccount)
	if err != nil || !present {
		return nil, err
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)

	var env creditsEnvelope
	if gerr := source.GetJSON(ctx, a.client, a.creditsEndpoint, header, &env); gerr != nil {
		return nil, gerr
	}
	if env.Data == nil {
		return nil, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "no account data object in response",
		}
	}
	// Same typed-nil discipline as FetchKeyUsage above.
	credits, cerr := normalizeCreditsData(env.Data)
	if cerr != nil {
		return nil, cerr
	}

	return credits, nil
}

// secret resolves one credential slot. ErrNotFound means the capability is
// simply not configured ("", false, nil); a broken keychain is a categorized
// credentials error. The token is never logged nor embedded in an error.
func (a *Adapter) secret(ctx context.Context, account credentials.AccountID) (string, bool, error) {
	token, err := a.creds.Get(ctx, account)
	if err == nil {
		if token == "" {
			return "", false, nil
		}

		return token, true, nil
	}
	if errors.Is(err, credentials.ErrNotFound) {
		return "", false, nil
	}
	var stateErr *credentials.StateError
	if errors.As(err, &stateErr) {
		return "", false, &source.Error{
			Category: source.CatCredentials,
			Detail:   "Keychain unavailable: " + stateErr.Message,
			Err:      stateErr,
		}
	}

	return "", false, &source.Error{
		Category: source.CatCredentials,
		Detail:   "Keychain read failed",
		Err:      err,
	}
}

// requiredUsage is one required numeric field with its JSON name.
type requiredUsage struct {
	field string
	value *float64
}

// normalizeKeyData converts the documented key fields defensively. The four
// per-window usage values are REQUIRED (absence/garbage would silently read
// as zero spend, which is exactly the mistake this monitor exists to avoid);
// everything else degrades individually without failing valid usage data.
func normalizeKeyData(d *keyData, log *slog.Logger) (*domain.OpenRouterKeyUsage, *source.Error) {
	// Refuse credential types that do not belong in the ordinary api-key
	// slot. Fixed sanitized detail: no key material, no provider payload.
	switch {
	case d.IsManagementKey != nil && *d.IsManagementKey:
		log.Warn("openrouter: api-key slot holds a management key")

		return nil, &source.Error{
			Category: source.CatCredentials,
			Detail:   "The stored API key is a Management key, not an ordinary API key",
		}
	case d.IsProvisioningKey != nil && *d.IsProvisioningKey:
		log.Warn("openrouter: api-key slot holds a provisioning key")

		return nil, &source.Error{
			Category: source.CatCredentials,
			Detail:   "The stored API key is a provisioning key, not an ordinary API key",
		}
	}

	usage := []requiredUsage{
		{"usage", d.Usage},
		{"usage_daily", d.UsageDaily},
		{"usage_weekly", d.UsageWeekly},
		{"usage_monthly", d.UsageMonthly},
	}
	for _, r := range usage {
		if r.value == nil || !finite(*r.value) {
			log.Warn("openrouter: key response missing required usage field", "field", r.field)

			return nil, &source.Error{
				Category: source.CatInvalidResponse,
				Detail:   "key response is missing required usage values",
			}
		}
	}

	u := &domain.OpenRouterKeyUsage{
		UsageTotal:   *d.Usage,
		UsageDaily:   *d.UsageDaily,
		UsageWeekly:  *d.UsageWeekly,
		UsageMonthly: *d.UsageMonthly,
	}

	// Optional BYOK block: retained when clean, defaulted when absent.
	for _, pair := range []struct {
		dst  *float64
		src  *float64
		name string
	}{
		{&u.BYOKTotal, d.BYOKUsage, "byok_usage"},
		{&u.BYOKDaily, d.BYOKUsageDaily, "byok_usage_daily"},
		{&u.BYOKWeekly, d.BYOKUsageWeekly, "byok_usage_weekly"},
		{&u.BYOKMonthly, d.BYOKUsageMonthly, "byok_usage_monthly"},
	} {
		if pair.src != nil && finite(*pair.src) {
			*pair.dst = *pair.src
		} else if pair.src != nil {
			log.Warn("openrouter: ignoring non-finite optional field", "field", pair.name)
		}
	}
	if d.IncludeBYOKInLimit != nil {
		u.IncludeBYOKInLimit = *d.IncludeBYOKInLimit
	}

	u.Limit = normalizeLimit(d.Limit, d.LimitRemaining, d.LimitReset, log)

	return u, nil
}

// normalizeLimit validates the single optional per-key limit. An unusable or
// internally inconsistent limit degrades the LIMIT GRAPH ONLY — it must never
// fail otherwise-valid usage data. limit_remaining is preferred as the
// enforced-counter source; no reset timestamp is invented from the cadence.
func normalizeLimit(limit, remaining *float64, reset *string, log *slog.Logger) *domain.OpenRouterLimit {
	if limit == nil {
		return nil
	}
	if !finite(*limit) || *limit <= 0 {
		log.Warn("openrouter: ignoring unusable key limit")

		return nil
	}
	if remaining == nil {
		// Without a remaining amount there is nothing trustworthy to draw
		// the enforced counter from; period usage fields must NOT stand in.
		log.Warn("openrouter: key limit reported without a remaining amount; dropping the limit graph")

		return nil
	}
	if !finite(*remaining) {
		log.Warn("openrouter: ignoring key limit with a non-finite remaining amount")

		return nil
	}
	lim := *limit
	rem := *remaining
	// Materially impossible states degrade instead of drawing nonsense.
	if rem < -balanceNoiseEpsilon || rem > lim+balanceNoiseEpsilon {
		log.Warn("openrouter: ignoring inconsistent key limit amounts")

		return nil
	}
	// Float noise just outside [0, limit] clamps back into range.
	if rem < 0 {
		rem = 0
	} else if rem > lim {
		rem = lim
	}

	return &domain.OpenRouterLimit{
		Amount:    lim,
		Remaining: rem,
		Cadence:   normalizeCadence(reset),
	}
}

// normalizeCadence maps the optional limit_reset label onto its known
// values; anything else (null, empty, unknown) becomes "" meaning "no known
// recurring reset", displayed as a plain "Key limit".
func normalizeCadence(reset *string) string {
	if reset == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(*reset)) {
	case domain.CadenceDaily:
		return domain.CadenceDaily
	case domain.CadenceWeekly:
		return domain.CadenceWeekly
	case domain.CadenceMonthly:
		return domain.CadenceMonthly
	default:
		return ""
	}
}

// normalizeCreditsData converts the documented account fields. Both totals
// are REQUIRED for a usable account capability.
func normalizeCreditsData(d *creditsData) (*domain.OpenRouterAccountCredits, *source.Error) {
	if d.TotalCredits == nil || d.TotalUsage == nil {
		return nil, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "account response is missing required credit totals",
		}
	}
	if !finite(*d.TotalCredits) || !finite(*d.TotalUsage) {
		return nil, &source.Error{
			Category: source.CatInvalidResponse,
			Detail:   "account response has non-finite credit totals",
		}
	}
	c := &domain.OpenRouterAccountCredits{
		TotalCredits: *d.TotalCredits,
		TotalUsage:   *d.TotalUsage,
	}
	c.Balance = c.TotalCredits - c.TotalUsage
	if c.Balance < 0 && c.Balance >= -balanceNoiseEpsilon {
		c.Balance = 0
	}

	return c, nil
}

// finite rejects the NaN/Inf values decoding could in principle introduce.
func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
