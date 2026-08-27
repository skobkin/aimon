# S4 Agent Rules — OpenRouter

These rules are authoritative for S4 where they conflict with older assumptions.
Preserve unrelated current behavior from repository HEAD.

## 1. Scope

S4 adds one compiled source kind: `openrouter`.

It must integrate with the existing dynamic source-instance architecture from
S3. Do not build a plugin framework, dynamic module loader, provider DSL,
generic HTTP-source builder, or schema-driven Settings engine.

OpenRouter Activity/Analytics is out of scope for S4.

## 2. Credentials are capabilities

An OpenRouter instance can have:

- ordinary API key only;
- Management key only;
- both;
- temporarily neither (for example after credential removal).

Credential presence controls capabilities.

| Credential | Endpoint / capability |
|---|---|
| API key | `GET /api/v1/key` — per-key usage and key limit |
| Management key | `GET /api/v1/credits` — account credit totals/balance |

Hard rule: **absence of a credential means zero HTTP requests to the dependent
endpoint**.

Do not:

- call an endpoint without its required credential;
- use the API key as a fallback Management key;
- use the Management key as a fallback API key;
- discover credential type by intentionally receiving 401/403.

If neither key exists, refresh fails locally with a sanitized credential error
and performs no network request.

## 3. Secrets

Both credentials use the existing instance-scoped keychain scheme.

Required stable slots:

```text
api-key
management-key
```

No secret may be stored in the source configuration document or ordinary Fyne
preferences.

Typed credentials used by Test Connection are temporary and must not be saved.

## 4. Provider semantics stay in the adapter

The OpenRouter adapter owns:

- API DTOs;
- endpoint selection/gating;
- authentication headers;
- conversion from OpenRouter fields into the domain snapshot;
- defensive parsing.

The UI owns:

- money formatting;
- sensitive-value masking;
- Overview/Remote presentation;
- limit color/presentation.

Do not make the UI inspect OpenRouter JSON or API-specific DTOs.

## 5. No invented reset time

OpenRouter exposes `limit_reset` as a cadence (`daily`, `weekly`, `monthly`, or
no reset). The current-key endpoint does not expose a `next_reset_at`.

S4 may display the cadence but must not synthesize a countdown or `ResetAt`.
Do not infer future reset timestamps from the local clock.

## 6. Partial capability configuration

Missing optional capability is not an error:

- API-key-only instance: usage/limit shown, Account section omitted.
- Management-key-only instance: Account data shown, Usage/limit omitted.
- both: both sections shown.

When both credentials are configured and one configured endpoint fails, S4 may
keep the existing source-level all-or-nothing refresh semantics: return refresh
error so the manager retains the last known good snapshot and marks the source
stale. Do not redesign the manager for per-capability stale state in S4.

## 7. Privacy

Add one global preference:

```text
Hide sensitive info
```

Default: `true`.

This is an application UI preference, not an OpenRouter credential or
provider-specific API setting.

S4 sensitive data:

- account balance;
- total credits purchased;
- total account usage.

Normal per-key monitoring fields (Today/Week/Month/Total and key limit) remain
visible.

Sensitive-value conceal/reveal must be implemented as a generic reusable UI
component/capability. On desktop, pointer hover reveals the value only while
hovered. When concealment is disabled globally, the real value is always shown.

Do not implement expensive image blur/shaders merely to satisfy the word
"blur". The product requirement is that the value is unreadable until hover.
A stable masked representation such as `$•••••` is acceptable and preferred
for S4.

## 8. Preserve existing behavior

Do not regress:

- dynamic instances and duplicate source kinds;
- renameable instance names;
- manual source ordering;
- instance-scoped credentials;
- enable/disable behavior;
- source freshness/stale handling;
- Overview 2-column pairing;
- current GPU/runtime/quota presentations;
- tray mechanics;
- Web UIs submenu;
- read-only product boundary.
