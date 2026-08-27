# S4 Implementation Specification — OpenRouter

Target release: **v0.4.0**

This document specifies architecture and acceptance criteria, not exact file
names. The agent must first inspect current HEAD because S3 implementation may
have evolved after its original plan.

## 1. Add OpenRouter source kind

Extend the current source-kind vocabulary/registry with:

```text
openrouter
```

Category: Remote.

Default instance name:

```text
OpenRouter
```

Default polling interval: use the current standard Remote default (expected
1 minute unless current HEAD changed it intentionally).

OpenRouter exposes no user-configurable API base URL in S4.

## 2. Credential slots

Extend credentials with two stable slots:

```text
api-key
management-key
```

The current instance-scoped account function should therefore produce
independent accounts similar to:

```text
source/<instance-id>/api-key
source/<instance-id>/management-key
```

Use the actual current S3 account naming helper rather than duplicating string
construction.

Delete cleanup must clean both OpenRouter slots best-effort.

## 3. Domain snapshot

Do not force OpenRouter into `QuotaSnapshot`; its data is spend/account data
with an optional per-key limit.

A provider-specific domain snapshot is acceptable and preferred over a vague
generic abstraction in S4, for example:

```go
type OpenRouterSnapshot struct {
    CollectedAt time.Time

    Key     *OpenRouterKeyUsage
    Account *OpenRouterAccountCredits
}

type OpenRouterKeyUsage struct {
    UsageDaily   float64
    UsageWeekly  float64
    UsageMonthly float64
    UsageTotal   float64

    BYOKDaily    float64
    BYOKWeekly   float64
    BYOKMonthly  float64
    BYOKTotal    float64

    IncludeBYOKInLimit bool

    Limit *OpenRouterLimit

    // Optional metadata retained only if useful/clean:
    Label      string
    ExpiresAt  *time.Time
    IsFreeTier bool
}

type OpenRouterLimit struct {
    Amount    float64
    Remaining float64
    Cadence   string // daily|weekly|monthly|""/unknown
}

type OpenRouterAccountCredits struct {
    TotalCredits float64
    TotalUsage   float64
    Balance      float64
}
```

Exact names may follow current project conventions.

`Key == nil` means no API-key capability was configured for this snapshot.

`Account == nil` means no Management-key capability was configured.

Do not put raw credentials, management capability tokens, HTTP errors, or
provider DTOs in the domain snapshot.

## 4. Adapter

Create a normal source adapter under the current source package structure.

Constructor receives:

- API-key account ID;
- Management-key account ID;
- credential store;
- HTTP client;
- logger;
- any existing common dependencies.

Production endpoints are fixed:

```text
https://openrouter.ai/api/v1/key
https://openrouter.ai/api/v1/credits
```

Tests must be able to target `httptest.Server` without exposing an arbitrary
OpenRouter Base URL in user config. Use constructor options/internal endpoint
injection/test seam consistent with the existing code style.

### Refresh algorithm

1. Read API-key slot.
   - not found: mark capability absent, not an error;
   - keychain unavailable/locked/etc: categorized credential error.

2. Read Management-key slot.
   - not found: capability absent, not an error;
   - keychain error: categorized credential error.

3. If both capabilities absent:
   - return local credential error;
   - make zero HTTP requests.

4. If API key present:
   - call `/api/v1/key` with that API key only;
   - parse key usage/limit.

5. If Management key present:
   - call `/api/v1/credits` with that Management key only;
   - parse account totals and derive balance.

6. If all configured capability requests succeed:
   - return one combined snapshot.

7. If any configured capability request fails:
   - return categorized error;
   - rely on existing manager stale-retention behavior.

No endpoint is called merely to determine credential type.

## 5. DTO defensiveness

Use pointer/nullable fields where OpenRouter documents or plausibly returns
null for optional values, especially:

- `limit`
- `limit_remaining`
- `limit_reset`
- `expires_at`

Reject NaN/Inf if JSON decoding or future test seams can introduce them.

Do not treat deprecated `rate_limit` as an S4 metric.

Sanitize provider errors; never log Authorization values.

## 6. Limit normalization

If no `limit` exists:

```text
Key.Limit = nil
```

If limit exists and is finite/positive:

- retain raw amount;
- retain valid `limit_remaining` when present;
- derive used amount/fractions in viewmodel/domain helper as appropriate.

Prefer `limit_remaining` as the source for the limit graph instead of assuming
one of daily/weekly/monthly usage fields exactly equals the enforced limit
counter.

For a valid positive limit:

```text
used = limit - remaining
used_fraction = clamp(used / limit)
remaining_fraction = clamp(remaining / limit)
```

If remaining is slightly outside due only to floating-point noise, clamp.
Materially inconsistent optional limit values should cause the graph to be
omitted/degraded, not cause the otherwise valid key usage snapshot to fail.

Unknown cadence does not invalidate the limit.

No `ResetAt` is generated.

## 7. Presentation type

Add a dedicated Overview presentation kind, e.g.:

```text
SpendOverview
```

This is a UI presentation type, not an OpenRouter adapter type.

The OpenRouter snapshot maps to it.

Suggested pure VMs:

```go
type SpendOverviewVM struct {
    UsageRows []MoneyMetricVM
    Limit     *SpendLimitVM
    Balance   *MoneyMetricVM
}

type MoneyMetricVM struct {
    Label     string
    Text      string
    Sensitive bool
}

type SpendLimitVM struct {
    Label             string
    UsedText          string
    UsedFraction      float64
    RemainingFraction float64
    RawText           string // Remote detail only if split VMs are used
}
```

Exact structure may adapt to current code.

## 8. Money formatting

Add pure shared formatting tests.

Required behavior:

```text
0       -> $0.00
0.0042  -> $0.0042
0.04    -> $0.04
12.47   -> $12.47
```

Small positive values must never become visually indistinguishable from zero.

Handle negative values defensively; do not silently turn them positive.

## 9. Global privacy preference

Add application preference:

```text
Hide sensitive info
```

Default `true`.

This should not require a source-config migration.

Preferred approach: store it as a separate ordinary Fyne application preference
(or a small existing app-preferences abstraction), because it is a global UI
preference rather than source-instance configuration.

Tests:

- absent preference -> true;
- persisted false -> false;
- toggling persists;
- monitor/detail refresh reflects the new state.

## 10. Sensitive-value component

Implement one generic reusable component that can present a real string or a
concealed string.

Desktop behavior:

- privacy disabled -> real string;
- privacy enabled + not hovered -> mask such as `$•••••`;
- `MouseIn` -> reveal;
- `MouseOut` -> conceal again.

Fyne exposes `desktop.Hoverable`; use public Fyne APIs only.

Do not implement a raster blur pipeline.

The component must never mutate/store the underlying metric value globally just
because it was revealed.

Unit-test state transitions/pure masking logic where practical. UI rendering
itself can be manually verified.

## 11. Settings integration

OpenRouter editor extends the current S3 dynamic-source editor.

Provider fields:

```text
Type          OpenRouter
Name          <editable>
Enabled       <check>
Refresh every <existing Remote presets>

API key
Management key
```

Each credential has independent:

- typed password entry;
- stored/not-stored state;
- Remove action.

The existing draft `InstanceID` workflow must be preserved.

### Test Connection

The Test operation builds temporary credential access from:

- typed value when non-empty;
- otherwise stored value for that instance/slot;
- absent when neither exists.

It then executes only available capabilities.

Result should distinguish both slots/capabilities, e.g.:

```text
API key: OK
Management key: not configured (skipped)
```

Do not save credentials during Test.

### Save

Do not redesign the S3 serialized config-mutation path.

Credential persistence failure should be handled consistently with current
credentialed providers. If current HEAD already has an explicit policy, follow
it and document any OpenRouter-specific edge case instead of inventing another
transaction framework.

An enabled OpenRouter instance with no credentials is allowed to exist; refresh
fails locally and performs no network calls. This keeps credential removal and
keychain failure recoverable.

## 12. Remote detail

Add OpenRouter detail renderer:

Usage section, only if API-key data exists:

- Today
- Week
- Month
- Total

Limit block, only if usable limit exists:

- `Daily limit`, `Weekly limit`, `Monthly limit`, or `Key limit`;
- used percentage;
- existing usage-oriented progress bar/color semantics;
- raw `$used / $limit` line.

Account section, only if Management-key data exists:

- Balance (sensitive)
- Purchased credits (sensitive)
- Account usage (sensitive)

Web button:

```text
Open OpenRouter activity
```

## 13. Overview

Overview remains in the current N-instance two-column layout.

OpenRouter card:

- Today/Week/Month when key data exists;
- optional single limit bar;
- Balance when account data exists;
- no Total row;
- no account purchased/usage rows;
- no BYOK rows.

Do not create one donut per usage period.

## 14. Request and security tests

Adapter tests must prove:

### API-only

- `/key` called exactly once;
- `/credits` called zero times;
- API Authorization header uses API key;
- Account snapshot nil.

### Management-only

- `/credits` called exactly once;
- `/key` called zero times;
- Management Authorization header uses Management key;
- Key snapshot nil.

### Both

- each endpoint called once;
- each receives the correct distinct token;
- combined snapshot contains both capabilities.

### Neither

- zero network requests;
- categorized local credential error.

### Wrong/failed credential

- 401/403 categorized using existing source error taxonomy;
- no raw token/body in returned user message.

Also test:

- malformed JSON;
- 5xx;
- request timeout if current adapter test style includes it;
- optional null limit/reset fields;
- limit normalization;
- account balance derivation;
- BYOK parsing if retained.

## 15. Viewmodel/component tests

Test at least:

- API-only Overview;
- Management-only Overview;
- combined Overview;
- no-limit omission;
- valid monthly/daily/weekly limit labels;
- unknown/null cadence -> `Key limit`;
- Remote includes Total;
- Overview omits Total;
- account fields marked sensitive;
- normal key usage not sensitive;
- small-positive money formatting;
- sensitive mask/reveal state;
- `<1% used` formatting reused for tiny positive limit usage if applicable.

## 16. Manual acceptance

Use fake/httptest endpoints and fake credentials for automated/manual
development where possible. Do not put real OpenRouter credentials in fixtures.

Recommended real-user manual checks after implementation:

1. Add API-key-only OpenRouter instance.
2. Verify Today/Week/Month/Total and optional limit.
3. Confirm no Account section.
4. Add Management key to same instance.
5. Verify Account section appears.
6. Hover masked account values; confirm reveal/mask.
7. Disable `Hide sensitive info`; confirm values remain visible.
8. Remove Management key; verify account endpoint is no longer requested and
   Account UI disappears after refresh.
9. Remove API key while Management key remains; verify usage endpoint stops and
   account-only monitoring continues.
10. Two OpenRouter instances with different API keys remain independent.
11. Stale behavior retains last good combined snapshot.
12. Web UIs tray entry opens OpenRouter Activity.

## 17. Explicit non-goals

Do not implement:

- Activity/Analytics APIs;
- request log;
- model/provider breakdown;
- generation lookup;
- history charts;
- local database;
- credential/key management;
- account/workspace administration;
- global spend aggregation across multiple OpenRouter instances;
- automatic deduplication of account balance when two instances use the same
  Management key.
