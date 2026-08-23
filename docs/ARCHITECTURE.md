# Architecture Specification

## 1. Purpose

The application aggregates several independent local and remote monitoring sources into a small tray UI.

The architecture must make partial configuration and partial failure normal operating modes rather than exceptional states.

## 2. High-level design

```text
                         ┌────────────────────┐
                         │ Non-secret settings│
                         │ Fyne Preferences   │
                         └─────────┬──────────┘
                                   │
                         ┌─────────▼──────────┐
                         │ Credential Store   │
                         │ system keychain    │
                         └─────────┬──────────┘
                                   │
                                   ▼
┌──────────────────────────────────────────────────────────┐
│                     Source Manager                       │
│                                                          │
│  ┌────────────┐ ┌────────────┐ ┌───────────┐ ┌────────┐ │
│  │ AMDGPU Web │ │ llama-swap │ │ MiniMax   │ │ Z.AI   │ │
│  │ adapter    │ │ adapter    │ │ adapter   │ │ adapter│ │
│  └──────┬─────┘ └─────┬──────┘ └─────┬─────┘ └───┬────┘ │
│         │             │              │            │      │
│         └─────────────┴──────┬───────┴────────────┘      │
│                              ▼                            │
│                    normalized snapshots                  │
│                              │                            │
│                    per-source runtime state              │
└──────────────────────────────┬───────────────────────────┘
                               │
                  ┌────────────┴────────────┐
                  ▼                         ▼
          Monitor view models       Settings view models
                  │                         │
                  ▼                         ▼
          tray splash window         decorated window
```

## 3. Core concepts

### 3.1 Source

A `Source` is one independently configurable monitoring integration.

Each source owns:

- its HTTP/API adapter;
- source-specific request/response DTOs;
- normalization into domain snapshots;
- its refresh timeout requirements.

A source must not know about Fyne widgets.

A minimal conceptual interface:

```go
type Source interface {
    ID() SourceID
    Refresh(ctx context.Context) (Snapshot, error)
}
```

The concrete implementation can use typed snapshots or a generic envelope, but do not trade type safety for a single `map[string]any` abstraction.

### 3.2 Source configuration

Common source configuration:

```go
type SourceConfig struct {
    Enabled      bool
    PollInterval time.Duration
}
```

Source-specific configuration extends this with values such as base URL, selected GPU ID, or credential reference.

Secrets are not part of persisted non-secret configuration values.

### 3.3 Snapshot

A snapshot is immutable data produced by one successful refresh.

Every stored snapshot has metadata:

```go
type SnapshotMeta struct {
    SourceID     SourceID
    CollectedAt  time.Time
}
```

The source manager additionally tracks attempt/runtime metadata; do not force every domain snapshot to carry error state.

### 3.4 Runtime state

Track per-source state separately from snapshot data.

Conceptual shape:

```go
type SourceRuntime struct {
    Enabled       bool
    Refreshing    bool
    LastAttemptAt time.Time
    LastSuccessAt time.Time
    LastError     error
    Snapshot      Snapshot // absent until first success
}
```

Derived presentation status:

- `Disabled`: source is not configured/enabled;
- `Loading`: enabled, first refresh in progress, no snapshot yet;
- `Healthy`: last refresh succeeded and a snapshot exists;
- `Stale`: a snapshot exists, but the most recent refresh failed;
- `Unavailable`: enabled, no valid snapshot exists, and refresh failed / credential backend is unavailable.

A source can recover from `Stale` or `Unavailable` on any later successful refresh.

## 4. Graceful degradation rules

Graceful degradation is a fundamental architecture requirement.

1. No source is required for application startup.
2. Disabled sources do not participate in polling.
3. A failed source does not cancel or slow another source.
4. A failed refresh does not erase the last successful snapshot.
5. Authentication/keychain failure affects only sources requiring that credential.
6. Missing optional fields from an upstream API do not fail the whole source if useful data remains.
7. UI rendering must tolerate absent fields.
8. No UI view should dereference data on the assumption that all four sources are configured.

## 5. Source manager and polling

The source manager owns one logical refresh loop per enabled source.

Requirements:

- refresh sources independently;
- serialize refreshes within one source;
- permit refreshes of different sources concurrently;
- use context cancellation for shutdown;
- use a per-request timeout;
- no automatic retry loop inside one poll iteration;
- manual refresh coalesces with an already-running refresh rather than creating duplicate requests;
- changing source configuration restarts/reconfigures only the affected source where practical.

Suggested default request timeouts:

- local HTTP sources: 3 seconds;
- remote provider sources: 10 seconds.

Timeout values are implementation defaults, not user-facing settings in S1.

### 5.1 Poll scheduling

Do not synchronize every source to one global ticker.

Each source has its own interval. A simple independent ticker per enabled source is sufficient for S1.

The next scheduled refresh should be measured from the normal poll loop; avoid accumulating multiple missed ticks after a slow request.

### 5.2 Manual refresh

Manual Refresh should:

- request refresh for all enabled sources;
- return control to the UI immediately;
- not wait synchronously in the event callback;
- not start a duplicate refresh for a source already in flight.

## 6. Domain model

Keep domain types small and semantic.

### 6.1 GPU

Conceptual example:

```go
type GPUSnapshot struct {
    DeviceID       string
    Name           string
    GPUUtilization *float64
    VRAMUsedBytes  *uint64
    VRAMTotalBytes *uint64
    GTTUsedBytes   *uint64
    GTTTotalBytes  *uint64
    TemperatureC   *float64
    PowerWatts     *float64
    SCLKMHz        *float64
    MCLKMHz        *float64
}
```

Use optional fields because hardware/kernel/backend support varies.

### 6.2 llama-swap

Conceptual example:

```go
type LlamaSwapSnapshot struct {
    RunningModels []RunningModel
    Activity      *InferenceActivity
}

type RunningModel struct {
    ID   string
    Name string // optional display name
}

type InferenceActivity struct {
    ActiveRequests  *int
    WaitingRequests *int
}
```

Only populate activity fields when current llama-swap APIs/metrics expose them with reliable semantics.

Do not infer "busy" from GPU utilization.

### 6.3 Quota

Normalize provider quota windows:

```go
type QuotaSnapshot struct {
    Provider string
    Plan     string // optional
    Windows  []QuotaWindow
}

type QuotaWindow struct {
    ID                string
    Name              string
    RemainingFraction float64 // normalized 0..1
    UsedFraction      float64 // derived or normalized 0..1
    Remaining         *float64
    Limit             *float64
    Unit              string
    ResetAt           *time.Time
}
```

`RemainingFraction` is the S1 presentation primitive.

Clamp externally supplied percentage values to valid display bounds only after validating/parsing them. Log/retain an adapter error for nonsensical upstream values rather than silently treating arbitrary bad data as valid.

## 7. Configuration persistence

Use Fyne Preferences for non-secret settings.

Persist at least:

- source enabled flags;
- base URLs;
- selected GPU ID where needed;
- polling interval selections;
- whether an optional local API credential is configured.

Do not persist secret values in Preferences.

Use stable preference keys behind a small configuration repository rather than reading/writing raw keys throughout UI code.

## 8. Credentials architecture

Use `github.com/zalando/go-keyring` behind an interface.

Conceptual interface:

```go
type Store interface {
    Status(context.Context) Status
    Get(context.Context, AccountID) (string, error)
    Set(context.Context, AccountID, string) error
    Delete(context.Context, AccountID) error
}
```

The exact interface can differ, but it must be injectable for tests.

Credential backend states:

- available;
- locked;
- unavailable;
- unsupported.

Suggested service name: `llm-monitor`.

Suggested account IDs:

- `source/llama-swap`;
- `source/minimax`;
- `source/zai`.

Do not add a plaintext fallback.

## 9. UI boundary

UI code should subscribe to or query normalized runtime snapshots and create view models.

Do not let UI code:

- perform HTTP;
- parse JSON;
- parse Prometheus text;
- know MiniMax/Z.AI response field names;
- query the system keychain directly;
- own polling timers.

## 10. Fyne threading

Background refreshes must run outside Fyne callbacks.

Any UI mutation originating from those goroutines must be queued with `fyne.Do` (or `fyne.DoAndWait` only when waiting is genuinely necessary).

Prefer publishing immutable state changes into a UI controller/view-model layer and doing one bounded UI update per source refresh instead of mutating individual labels from network code.

## 11. Suggested package layout

This is guidance, not a requirement to create empty packages.

```text
cmd/llm-monitor/
    main.go

internal/app/
    app.go

internal/config/
    config.go
    preferences.go

internal/credentials/
    store.go
    keyring.go

internal/domain/
    source.go
    gpu.go
    llamaswap.go
    quota.go

internal/source/
    manager.go
    amdgpuweb/
    llamaswap/
    minimax/
    zai/

internal/ui/
    monitor.go
    settings.go
    viewmodel.go
    components/
        quota_bar.go
        source_card.go
        metric_row.go

internal/resources/
    ...
```

Avoid a generic `utils` package.

## 12. Logging

Use standard structured/logical logging (`log/slog` is sufficient).

Never log:

- API keys;
- Authorization headers;
- keychain payloads;
- raw provider responses unless explicitly sanitized.

Useful logs:

- source enabled/disabled;
- refresh failure with source ID and HTTP status/category;
- recovery after failure;
- keychain backend state changes;
- startup/shutdown.

Avoid logging every successful 5-second local poll at INFO level.

## 13. Testing strategy

### Unit tests

- quota normalization and threshold/color selection;
- source runtime state derivation;
- preference/config validation;
- keychain error-state classification using fake functions/store;
- provider response parsing;
- byte/percentage formatting.

### HTTP adapter tests

Use `httptest.Server` for:

- success;
- malformed JSON;
- missing optional fields;
- non-2xx response;
- timeout/cancellation;
- provider-specific status/error envelope.

### Source manager tests

Verify:

- independent sources do not block one another;
- same-source refresh does not overlap;
- stale snapshot is retained after failure;
- unavailable source recovers;
- disabled source is not called;
- manual refresh coalesces correctly.

### UI logic

Keep threshold/formatting/view-model logic outside rendering where practical so it can be tested without pixel-level GUI tests.

## 14. Out of scope for S1

- charts/history;
- database persistence;
- notifications/alerts;
- adaptive tray icon fill/animation;
- GPU process list;
- embedded logs;
- llama-swap write actions;
- provider account management;
- remote quota forecasting;
- automatic focus-loss hiding that requires native hacks.
