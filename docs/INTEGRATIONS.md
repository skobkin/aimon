# Integration Specification

## 1. General adapter rules

All integrations are independent adapters behind normalized domain snapshots.

Each adapter must:

- accept an explicit base URL/endpoint configuration where appropriate;
- use an injected HTTP client or otherwise be easy to test with `httptest.Server`;
- use request contexts/timeouts;
- treat non-2xx responses as refresh failures;
- parse responses defensively;
- return partial optional fields when the response is otherwise valid;
- never log Authorization headers or secret values;
- never expose provider-specific JSON DTOs to UI packages.

No integration is mandatory for application startup.

## 2. amdgputop-web

### Role

Authoritative S1 source for local AMD GPU telemetry.

### Upstream project

`https://github.com/skobkin/amdgputop-web`

The current project documents these relevant endpoints:

- `GET /api/gpus`
- `GET /api/gpus/<id>/metrics`
- `GET /api/gpus/<id>/procs`
- WebSocket `/ws`

S1 uses polling, not WebSocket streaming, because the desired monitor cadence is only every 5+ seconds.

### S1 endpoints

Use:

- `/api/gpus` for discovery/connection testing;
- `/api/gpus/<id>/metrics` for current metrics.

Do not use `/procs` in S1.

### Normalized fields

Overview:

- GPU/display name;
- GPU utilization;
- VRAM used/total;
- temperature.

Local detail, if available from the backend:

- GTT used/total;
- power;
- SCLK;
- MCLK;
- other small current-value fields only if they are clearly useful and already available.

Missing fields are normal. Do not fabricate zeros.

### Multi-GPU behavior

S1 monitors one selected GPU from this source.

- if exactly one GPU is discovered, select it automatically unless an existing preference says otherwise;
- if multiple GPUs are present, Settings exposes a `widget.Select`;
- if a previously selected GPU disappears, keep the source enabled but report it unavailable and allow reselection.

### Authentication

No first-class amdgputop-web authentication scheme is required in S1.

Reverse-proxy authentication support is out of scope unless the repository already provides a trivial generic mechanism without expanding UI scope.

## 3. llama-swap

### Role

Authoritative S1 source for:

- currently running model(s);
- runtime health;
- active/waiting request information only when a current public endpoint/metric exposes it reliably.

Do not use llama-swap as the S1 GPU telemetry source.

### Upstream project

`https://github.com/mostlygeek/llama-swap`

Current documented relevant endpoints include:

- `GET /health`
- `GET /running`
- `GET /metrics`
- `/ui`

llama-swap also exposes write operations such as unload; S1 must not call them.

### S1 behavior

Connection/health test:

1. use `/health` where useful;
2. fetch `/running` for actual model state.

Use `/metrics` only for request/activity fields that can be mapped with stable, verified semantics.

If stable `active requests` / `waiting requests` metrics cannot be identified, omit those values rather than blocking the rest of S1 or inventing a heuristic.

Do not infer request state from GPU utilization.

### Authentication

llama-swap can be configured with API keys.

Support an optional local API key credential if required by the relevant endpoints/deployment.

Store it in the system keychain under its own account ID. Do not persist it in Preferences.

### Open UI

The Local detail card may contain `Open llama-swap UI`, navigating to `<base>/ui`.

This is navigation only and does not turn S1 into an admin UI.

## 4. MiniMax Token Plan

### Role

Remote quota monitor.

### Official endpoint

MiniMax documents:

```text
GET https://www.minimax.io/v1/token_plan/remains
Authorization: Bearer <API Key>
Content-Type: application/json
```

Reference:

`https://platform.minimax.io/subscribe/coding-plan`

MiniMax currently describes included Token Plan quota as controlled by 5-hour rolling and weekly windows.

### Authentication

Store the Token Plan/Coding Plan key in the system keychain.

Default credential account ID: `source/minimax`.

### Parsing

The official documentation publishes the endpoint but does not provide a stable formal response schema on the referenced page.

Therefore:

- keep response DTOs private to the adapter;
- parse the current `model_remains` style payload defensively;
- support the current interval and weekly remaining percentage fields when present;
- validate provider error/status envelopes;
- normalize into generic `QuotaWindow` values;
- add sanitized adapter fixtures/tests based on observed response shapes;
- do not expose undocumented raw field names to UI.

Prefer provider-supplied remaining percentage when its semantics are established. When both raw counts and percentage are present, tests should verify they are internally consistent enough to avoid inverted display semantics.

### S1 windows

At minimum, expose the relevant text/coding-plan entry's:

- 5-hour/current interval remaining quota;
- weekly remaining quota.

If multiple model entries exist, choose the text/coding model entry using explicit response semantics/model naming, not array position alone.

### Reset time

Do not make reset time a required S1 field.

Only expose a reset time if the current response has a field with verified reset/countdown semantics. A missing reset time is normal.

## 5. Z.AI / GLM Coding Plan

### Role

Remote Coding Plan quota monitor.

### API stability warning

Z.AI currently ships an official `glm-plan-usage` Claude Code plugin, but the monitor endpoints it uses are not presented as a stable public API contract.

Treat this adapter as semidocumented and potentially changeable.

Official plugin repository:

`https://github.com/zai-org/zai-coding-plugins`

Current plugin script:

`plugins/glm-plan-usage/skills/usage-query-skill/scripts/query-usage.mjs`

The official script currently derives the base domain from the Anthropic-compatible provider URL and queries:

```text
GET /api/monitor/usage/model-usage
GET /api/monitor/usage/tool-usage
GET /api/monitor/usage/quota/limit
```

For S1 quota display, `/api/monitor/usage/quota/limit` is the primary endpoint.

### Default base

International Z.AI default base domain:

```text
https://api.z.ai
```

Keep the base domain configurable rather than hardcoding it deep inside the adapter.

### Authentication

Current official plugin behavior sends the token directly in the `Authorization` header.

Store the credential in system keychain with account ID `source/zai`.

### Quota normalization

The official plugin currently maps quota types including:

- `TOKENS_LIMIT` -> token usage (5 hour);
- `TIME_LIMIT` -> MCP usage (1 month).

Its `percentage` is usage-oriented in the plugin presentation.

Normalize this at the adapter boundary to:

```text
remaining = 1 - used
```

with correct 0..1/0..100 conversion after confirming the actual payload format in tests.

The UI must still say `NN% remaining`, just like MiniMax.

### Reset time

Do not synthesize a Z.AI reset time.

As of the current official plugin/API behavior, `/api/monitor/usage/quota/limit` does not provide the desired quota reset time; the upstream plugin repository has an open feature request for it.

### Compatibility

Keep Z.AI response parsing isolated and fixture-tested so endpoint/schema changes can be repaired without touching the domain or UI layers.

## 6. Credential storage

Use the same general design already proven in `skobkin/qBtRemoteGo`:

- `github.com/zalando/go-keyring`;
- a small injectable credential-store abstraction;
- explicit backend status;
- classification of locked/unavailable/unsupported states;
- no plaintext fallback.

Do not copy qBtRemoteGo's service/account names. Use names belonging to this application.

## 7. Polling and provider safety

Defaults:

| Source | Default | Minimum |
|---|---:|---:|
| amdgputop-web | 5s | 5s |
| llama-swap | 5s | 5s |
| MiniMax | 60s | 30s |
| Z.AI | 60s | 30s |

No adapter should perform hidden sub-second polling.

Do not retry remote API failures immediately in a loop.

A single logical refresh may require more than one HTTP request only when the integration genuinely requires it (for example llama-swap health + running state); keep that bounded.

## 8. HTTP error presentation

Adapters return errors with enough classification for logs/details but UI shows concise messages.

Examples:

- connection refused -> `Connection failed`;
- timeout -> `Request timed out`;
- 401/403 -> `Authentication failed`;
- 429 -> `Rate limited`;
- malformed response -> `Invalid provider response`;
- 5xx -> `Provider error (HTTP 5xx)`.

Do not render raw HTML error pages or full provider JSON errors in the compact monitor.

## 9. Commit-safe fixtures

Before committing any captured fixture:

- replace account/user IDs;
- remove API keys/tokens;
- remove cookies;
- remove request/trace IDs;
- remove IP addresses/hostnames that reveal private infrastructure if present;
- retain only fields needed to test parsing.

Synthetic fixtures are preferable where they exercise the same schema.
