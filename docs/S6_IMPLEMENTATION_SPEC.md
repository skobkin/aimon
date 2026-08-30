# S6 Implementation Specification — ChatGPT Codex quotas

Target: **v0.6.0 / Build 6**. Inspect actual HEAD after S5 before fixing exact
file/type names.

## 1. Source/config

Add `KindCodex = "codex"`, Remote category, default name `ChatGPT Codex`, default
interval 5m. No credentials slots.

Suggested settings:

```go
type CodexMode string
const (
    CodexModeOnDemand CodexMode = "on-demand"
    CodexModePersistent CodexMode = "persistent"
)
type EnvOverride struct { Name, Value string }
type CodexSettings struct {
    Executable string
    Mode CodexMode
    Env []EnvOverride
}
```

Defaults: executable `codex`, mode on-demand, no overrides. Validate non-empty
launcher, known mode, portable env names, no duplicates/NUL. Runtime discovery
is not required for pure persisted-config validation.

## 2. Domain/presentation

Dedicated snapshot recommended:

```go
type CodexSnapshot struct {
    CollectedAt time.Time
    PlanType string
    Windows []QuotaWindow
    ResetCreditsAvailable *int64
    RateLimitReachedType string
}
```

Reuse existing neutral quota-window/domain type if suitable. No account email,
ID, auth token, reset-credit IDs, or raw RPC payload.

Map to existing QuotaOverview/quota detail presentation. Add only small optional
metadata VM fields if required; no provider branching inside generic widgets.

## 3. Launcher/discovery

Build effective env = parent + overrides. Resolve bare executable names against
effective PATH; explicit paths are checked directly. When PATH is overridden,
do not rely blindly on `exec.LookPath`; resolve against the effective env and set
`cmd.Path` explicitly.

Spawn argv exactly:

```text
<launcher> app-server --stdio
```

No shell. Capture stderr only in a bounded/sanitized way; never log env values or
raw stderr by default.

Settings discovery is cheap lookup only, refreshed when Executable or PATH
override changes. Status: found(path), not-found-in-PATH, explicit path missing,
not executable, invalid.

## 4. JSON-RPC client

Small client over stdin/stdout with monotonically unique request ids per process,
one JSON object per line, strict response-id correlation, notification skipping,
JSON-RPC error handling, EOF/timeout handling, and no concurrent calls required.
Never log full payloads.

Initialize once per process using current app version clientInfo, then send
`initialized`. Read method exactly `account/rateLimits/read`.

## 5. DTO/normalization

Model only needed fields, preserving absent-vs-zero. Conceptually:

```go
type rateLimitsReadResult struct {
    RateLimits *rateLimitSnapshot `json:"rateLimits"`
    RateLimitsByID map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
    ResetCredits *resetCreditsSummary `json:"rateLimitResetCredits"`
}
type rateLimitSnapshot struct {
    LimitID string `json:"limitId"`
    Primary *window `json:"primary"`
    Secondary *window `json:"secondary"`
    PlanType *string `json:"planType"`
    RateLimitReachedType *string `json:"rateLimitReachedType"`
}
type window struct {
    UsedPercent *float64 `json:"usedPercent"`
    WindowDurationMins *int64 `json:"windowDurationMins"`
    ResetsAt *int64 `json:"resetsAt"`
}
type resetCreditsSummary struct { AvailableCount *int64 `json:"availableCount"` }
```

Prefer map `codex`, else top-level. Build zero/one/two window candidates and
require at least one usable one. Validate finite sane percent; normalize to used
fraction. ResetAt comes only from valid Unix timestamp. Duration labels are
pure helpers and do not alter MiniMax/Z.AI behavior.

## 6. On-demand adapter

Refresh: resolve -> spawn -> initialize -> read -> normalize -> close stdin ->
wait/reap -> return. Every error path cleans up. Cancellation kills/reaps.

Graceful shutdown should be bounded; always reap. A valid snapshot should not be
lost merely because graceful EOF exit needed a forced cleanup after a short grace
unless current project policy requires otherwise.

## 7. Persistent adapter

Keep private RPC client. First refresh starts+initializes; later refreshes read.
On timeout/EOF/broken pipe/protocol failure: destroy/reap and return error; next
refresh starts clean. Avoid same-refresh automatic retry unless existing source
policy already does that.

## 8. Generic lifecycle

Persistent mode requires deterministic source disposal. If manager lacks it,
add smallest generic optional Close interface and invoke it on remove, disable,
adapter replacement, and app/manager shutdown. Tests must prove no orphan/zombie.
Rename/reorder do not restart; executable/mode/env changes do. Interval-only
changes preserve runtime if S3 already guarantees that.

## 9. Test Connection

Always throwaway one-shot regardless of mode. Use draft executable/env exactly.
Report stages: Codex CLI, ChatGPT, Rate limits. No saved config mutation and no
surviving process.

## 10. Errors

Provide explicit setup messages:

```text
Codex CLI not found in PATH. Install Codex CLI or choose its executable/wrapper.
Codex executable not found or not executable.
This Codex CLI does not support the app-server rate-limit interface. Update Codex CLI or choose a compatible executable.
```

Map auth/backend/timeout/invalid-response/process failures into current source
error taxonomy. Never include env values, auth/account payloads, raw stderr, or
unsanitized backend text.

## 11. Environment editor

Support add/remove override rows (or propose a strict smaller alternative during
planning if current Fyne settings architecture makes this disproportionate).
Tests: inherited PATH, overridden PATH, explicit wrapper, value containing `=`,
duplicate/invalid names, env values absent from logs/errors.

## 12. Quota/UI behavior

Reuse `<1%` and used-oriented colors/fill/reset countdown. Duration-label tests:
300 -> 5-hour/5h; 10080 -> Weekly/week; 60 -> 1-hour/1h; nil ->
Primary/Secondary; other -> stable human label.

Editor must show CLI discovery status before Test. UI tests cover found/missing
launcher, mode help, env warning, two donuts, one-window case, plan/reset-credit
rows, and manager stale-last-good behavior.

## 13. Process/RPC test matrix

Executable resolution: bare found/missing, explicit wrapper/missing path, PATH
override, inherited env.

RPC fake-child harness: initialize, unsolicited notification before response,
unrelated response id handling, JSON-RPC error, malformed JSON, EOF, timeout,
initialized notification sent, exact read method, forbidden methods never called.

Normalization: map codex preferred, fallback, 1/2 windows, null duration/reset,
invalid percent, optional plan/reset count/reached type.

On-demand: process only during refresh, stdin closes, reaped, cancellation kills.
Persistent: starts once, reuses, broken stream tears down, next refresh restarts,
Close/replacement/disable/app shutdown reap.

## 14. Poll discipline/version

Normal refresh performs exactly one `account/rateLimits/read` after required
initialization. Do not add account/read, account/usage/read, login status, or
--version polling.

Final metadata: Version 0.6.0, Build 6.

## 15. Non-goals

Managed daemon/proxy attach, reset redemption, account email, daily/per-thread
usage, API-key quota, OpenAI API billing, direct ChatGPT HTTP, Codex
sessions/threads, code-mode-host monitoring, history/charts.
