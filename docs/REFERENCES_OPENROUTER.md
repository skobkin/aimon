# OpenRouter references used for S4

Research date: **2026-08-27**.

Use current official documentation during implementation if these pages have
changed.

## Current API key

Documentation:

https://openrouter.ai/docs/api/api-reference/api-keys/get-current-api-key

Endpoint:

```text
GET https://openrouter.ai/api/v1/key
```

Documented fields include:

- `usage`
- `usage_daily`
- `usage_weekly`
- `usage_monthly`
- BYOK equivalents
- `limit`
- `limit_remaining`
- `limit_reset`
- `include_byok_in_limit`
- key metadata

Authentication: Bearer API key.

## Account credits

Documentation:

https://openrouter.ai/docs/api/api-reference/credits/get-remaining-credits

Endpoint:

```text
GET https://openrouter.ai/api/v1/credits
```

Management key required.

Documented fields:

- `total_credits`
- `total_usage`

The response does not directly return `balance`; derive it.

## Key limit/reset semantics

Create/update key docs:

https://openrouter.ai/docs/api/api-reference/api-keys/create-keys
https://openrouter.ai/docs/api/api-reference/api-keys/update-keys

Management key guide:

https://openrouter.ai/docs/guides/overview/auth/management-api-keys

Relevant semantics:

- a key has one optional credit `limit`;
- `limit_reset` is `daily`, `weekly`, `monthly`, or null;
- reset cadence is not three separate quotas;
- OpenRouter documentation says resets occur at midnight UTC and weeks are
  Monday through Sunday, but the current-key response does not return a
  concrete next-reset timestamp;
- S4 intentionally does not synthesize a countdown.

## OpenRouter web UI

Activity:

https://openrouter.ai/activity

## Deferred analytics

Do not implement these in S4, but they are candidates for the next stage:

https://openrouter.ai/docs/api/api-reference/analytics/get-user-activity
https://openrouter.ai/docs/api/api-reference/beta-analytics/query-analytics

## Privacy background

OpenRouter data collection documentation:

https://openrouter.ai/docs/guides/privacy/data-collection

S4 does not retrieve prompt or completion content.
