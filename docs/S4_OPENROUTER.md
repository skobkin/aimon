# S4 OpenRouter Integration Contract

Research date: **2026-08-27**.

Only official OpenRouter documentation is authoritative for the API contract in
this document. Real payloads may still be used during implementation to add
defensive fixtures, but they must be sanitized.

## 1. Endpoints

### 1.1 Current API key

```http
GET https://openrouter.ai/api/v1/key
Authorization: Bearer <ordinary API key>
```

Official documentation describes this endpoint as returning information for the
key used to authenticate the request.

Fields currently documented in `data` include:

```json
{
  "usage": 25.5,
  "usage_daily": 25.5,
  "usage_weekly": 25.5,
  "usage_monthly": 25.5,

  "byok_usage": 17.38,
  "byok_usage_daily": 17.38,
  "byok_usage_weekly": 17.38,
  "byok_usage_monthly": 17.38,

  "limit": 100,
  "limit_remaining": 74.5,
  "limit_reset": "monthly",
  "include_byok_in_limit": false,

  "label": "redacted/example",
  "is_free_tier": false,
  "is_management_key": false,
  "is_provisioning_key": false,
  "expires_at": "2027-12-31T23:59:59Z"
}
```

The adapter must parse defensively. In particular, optional limit/reset/expiry
fields should tolerate null/absence.

`rate_limit` is documented as deprecated and safe to ignore.

### 1.2 Account credits

```http
GET https://openrouter.ai/api/v1/credits
Authorization: Bearer <Management key>
```

This endpoint requires a Management key.

Documented response:

```json
{
  "data": {
    "total_credits": 100.5,
    "total_usage": 25.75
  }
}
```

Derived balance:

```text
balance = total_credits - total_usage
```

Clamp tiny floating-point negative noise to zero only within a small epsilon.
Do not silently hide a materially negative value if OpenRouter actually returns
one.

## 2. Request gating

The adapter must inspect credential availability before building requests.

### API key absent

Do not call `/api/v1/key`.

### Management key absent

Do not call `/api/v1/credits`.

### Both absent

Perform zero network requests and return a local categorized credential error.

### Both present

Call both required endpoints. Keep implementation simple; sequential requests
are acceptable at the existing remote polling cadence. Do not add concurrency
dependencies merely for two small calls.

If one of the two configured requests fails, the refresh may fail as a whole so
the existing manager retains the previous snapshot and marks the source stale.

## 3. Authentication separation

Both endpoint families use Bearer authentication, but credentials are not
interchangeable in this application:

```text
/api/v1/key      <- ordinary API key only
/api/v1/credits  <- Management key only
```

Tests must assert the exact Authorization token used by each endpoint and that
a missing slot does not cause a fallback to the other slot.

## 4. Usage semantics

The current-key response provides four per-key spend values:

- `usage_daily`
- `usage_weekly`
- `usage_monthly`
- `usage` (overall/total usage for that key)

S4 shows all four in Remote detail.

Overview shows:

- Today
- Week
- Month

Total is intentionally omitted from Overview to keep the glance surface compact.

Amounts are OpenRouter credit/USD-denominated spend values and should be shown
with a `$` prefix.

### Adaptive money formatting

Avoid turning a real small positive spend into `$0.00`.

Recommended formatting:

- zero -> `$0.00`
- `abs(value) >= 0.01` -> two decimal places
- `0 < abs(value) < 0.01` -> up to four decimal places, trimming unnecessary
  trailing zeros while keeping the value distinguishable from zero

Examples:

```text
0       -> $0.00
0.0042  -> $0.0042
0.04    -> $0.04
12.47   -> $12.47
```

Do not abbreviate money with `k` in S4.

## 5. Key limit semantics

`limit` is one per-key spending cap.

`limit_reset` can be:

- `daily`
- `weekly`
- `monthly`
- absent/null (non-recurring/no reset cadence)

This is **one limit**, not three limits.

When `limit` and a valid `limit_remaining` are both available:

```text
used_amount = limit - limit_remaining
used_fraction = used_amount / limit
remaining_fraction = limit_remaining / limit
```

Clamp display fractions to `[0,1]` after validating finite numeric inputs.

If optional limit data is internally inconsistent, do not fail otherwise valid
usage data merely to draw the limit graph. Omit/degrade the graph and log a
sanitized warning.

The limit visualization reuses existing usage-oriented quota semantics:

- bar fill = used fraction;
- threshold color is based on remaining fraction;
- numeric label says `NN% used` / `<1% used` using existing shared formatting.

### Limit labels

```text
daily   -> Daily limit
weekly  -> Weekly limit
monthly -> Monthly limit
null    -> Key limit
unknown -> Key limit
```

Do not synthesize a reset countdown in S4.

## 6. BYOK fields

Parse/retain the documented BYOK usage values if it fits cleanly into the
snapshot/DTO design:

- total
- daily
- weekly
- monthly
- `include_byok_in_limit`

They are not displayed in S4.

Do not add a BYOK section merely because the fields are available.

## 7. Management-key account metrics

With Management key configured, display:

- Balance = `total_credits - total_usage`
- Purchased credits = `total_credits`
- Account usage = `total_usage`

All three are sensitive UI values in S4.

Without Management key, the Account section is omitted — not shown as
Unavailable.

## 8. Errors

Use the application's existing categorized source errors.

Relevant HTTP behavior documented by OpenRouter includes:

- 401 missing/invalid authentication;
- 403 on Management-only operations when the wrong credential type is used;
- 5xx provider/server errors.

Never show raw response bodies or provider error payloads if they might contain
sensitive details.

## 9. Web navigation

OpenRouter source web action:

```text
Label: Open OpenRouter activity
URL:   https://openrouter.ai/activity
```

Tray `Web UIs` uses the instance display name as it does for other dynamic
instances.

## 10. Explicitly deferred

Not S4:

- `/api/v1/activity`
- `/api/v1/analytics/meta`
- `/api/v1/analytics/query`
- request/generation lists
- model/provider/API-key breakdown analytics
- charts/history
- local persistence of request activity
- API key CRUD
- management actions
