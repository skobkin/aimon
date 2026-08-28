# S5 Implementation Specification — Hindsight

Target release: **v0.5.0**

Inspect actual repository HEAD after S4 before deciding exact files/types.

## 1. Source kind and registry

Add:

```text
SourceKind: hindsight
Category:   Local
DefaultName: Hindsight
```

Use the existing Local polling preset/default architecture.

Registry definition should support:

- provider settings;
- optional `api-key` credential slot;
- Build;
- Test Connection;
- optional Web URL from configured Web UI URL.

## 2. Settings/config

Suggested provider settings:

```go
type HindsightSettings struct {
    BaseURL  string
    WebUIURL string
}
```

Exact naming follows current code.

Validation:

- BaseURL required and valid HTTP(S) URL under existing URL policy;
- WebUIURL optional, but if non-empty must be valid HTTP(S) URL;
- no bank fields.

Configuration persistence must contain no credential.

## 3. Domain snapshot

Use a dedicated snapshot, not `MetricsSnapshot` if that would force unrelated
GPU semantics.

Suggested shape:

```go
type HindsightSnapshot struct {
    CollectedAt time.Time

    Service HindsightService
    Memory  HindsightMemory
    Activity HindsightActivity
    Runtime HindsightRuntime
    Queue   *HindsightQueue
}

type HindsightService struct {
    Status      string
    Database    string
    Version     string
    Uptime      time.Duration
    DBAcquireMS *float64
}

type HindsightMemory struct {
    Banks          int64
    Facts          int64
    LastWriteAt    *time.Time
    LastDocumentAt *time.Time
}

type HindsightActivity struct {
    RetainPerMin        *float64
    RecallPerMin        *float64
    ReflectPerMin       *float64
    ConsolidatePerMin   *float64
    LLMCallsPerMin      *float64
    InputTokensPerMin   *float64
    OutputTokensPerMin  *float64
    HTTP5xxPercent      *float64
}

type HindsightRuntime struct {
    DBPoolSize      *float64
    DBPoolIdle      *float64
    DBPoolMax       *float64
    DBPoolWaiting   *float64
    HTTPInProgress  *float64
}

type HindsightQueue struct {
    Pending               *float64
    Processing            *float64
    Failed                *float64
    ConsolidationBacklog  *float64
    ConsolidationFailed   *float64
}
```

Use integer types for gauges when actual parsed semantics allow; pointers/option
types should preserve "metric absent" vs zero.

No bank ID/name field is permitted.

The adapter may keep raw aggregate counter baseline privately; do not expose raw
Prometheus label maps in the domain.

## 4. HTTP adapter

Create standard source adapter with:

- normalized Base URL;
- optional API-key account ID;
- credential store;
- HTTP client;
- logger;
- slow cache/baseline state.

Request timeout should match current local-source conventions unless actual
Hindsight behavior argues for a slightly larger safe timeout.

### Authorization helper

Resolve optional API key once per Refresh where practical.

- NotFound -> empty token, no auth header.
- credential-store failure -> CatCredentials.
- present -> Bearer header.

Never log it.

## 5. Refresh flow and request budget

### First refresh / slow cache expired

Fetch:

1. `/health`
2. `/metrics`
3. `/health/live`
4. all necessary pages of `/v1/default/banks?limit=1000&offset=N`

### Normal refresh while slow cache fresh

Fetch only:

1. `/health`
2. `/metrics`

Target slow TTL: approximately 60 seconds.

Do not add `/version`.

### Cache update atomicity

Only replace cached slow data after the full slow fetch succeeds.

Only advance the Prometheus rate baseline after `/metrics` parses successfully.

If overall refresh later fails because another required request failed, decide
carefully whether consuming the new counter baseline would cause misleading
next rates. Preferred behavior: construct the candidate snapshot/baseline and
commit adapter ephemeral state only when the Refresh succeeds.

Test this pure/state behavior.

## 6. Bank pagination

DTO should intentionally omit identity/profile fields:

```go
type bankListResponse struct {
    Banks  []bankAggregateItem `json:"banks"`
    Total  int                 `json:"total"`
    Limit  int                 `json:"limit"`
    Offset int                 `json:"offset"`
}

type bankAggregateItem struct {
    FactCount      *int64      `json:"fact_count"`
    LastDocumentAt *time.Time  `json:"last_document_at"`
    LastWriteAt    *time.Time  `json:"last_write_at"`
}
```

Use custom string timestamps if upstream formats require it.

Rules:

- page size 1000;
- sum facts;
- max timestamps;
- validate non-negative total/offset/count;
- no-progress page before total reached -> invalid response;
- guard integer overflow;
- do not rely blindly on echoed `limit`;
- no per-bank request.

Tests must include >1 page.

## 7. Prometheus exposition parser

Parse selected metric samples from text.

Required capabilities:

- ignore comments/HELP/TYPE lines;
- parse metric name;
- parse labels needed by S5:
  - `operation`
  - `status`
  - `status_class`
  - optionally `type`
- parse finite numeric sample value;
- tolerate irrelevant metric families/labels;
- handle ordinary Prometheus label escaping correctly enough for upstream
  exposition;
- optional sample timestamp may be ignored.

Do not build a general PromQL layer.

Test against representative real-style exposition fixtures using the actual
underscore metric names.

## 8. Metric aggregation

At each successful metrics parse create one aggregate sample:

```go
type metricsCounters struct {
    At time.Time

    RetainTotal       optional float64
    RecallTotal       optional float64
    ReflectTotal      optional float64
    ConsolidateTotal  optional float64

    LLMCallsTotal     optional float64
    InputTokensTotal  optional float64
    OutputTokensTotal optional float64

    HTTPRequestsTotal optional float64
    HTTP5xxTotal      optional float64
}
```

and current gauges separately.

Aggregation sums every exposed series matching the metric family/selected
operation/status, regardless of bank/provider/model labels.

Do not retain those labels.

## 9. Counter-rate derivation

Pure helper:

```text
rate_per_min = (current - previous) / elapsed_seconds * 60
```

Valid only when:

- both samples present;
- both finite;
- elapsed > 0;
- current >= previous.

On reset/decrease -> unavailable for that interval.

For HTTP error percentage:

```text
delta_all > 0
  ? delta_5xx / delta_all * 100
  : unavailable
```

Clamp only tiny floating error; materially impossible values should be omitted
or treated as invalid according to helper policy.

Tests:

- first sample unavailable;
- normal delta;
- zero delta;
- counter reset;
- missing current/previous;
- elapsed zero;
- small non-zero rates preserved.

## 10. Backlog detection

Do not use config/version feature flags.

Simply detect families in `/metrics`.

If no backlog families exist:

```text
Queue == nil
```

If some exist:

- preserve presence independently;
- missing one family does not force zero;
- build Queue when at least one queue/backlog value is available.

Sum across labels.

For `hindsight_async_operations`, aggregate statuses:

```text
pending
processing
failed
```

across all `operation_type`.

## 11. Health parsing

Parse required stable fields defensively.

`/health/live`:

- status;
- version;
- uptime_seconds.

`/health`:

- status;
- database;
- optional DB diagnostic fields.

Do not require every diagnostic field.

Unknown additive fields ignored.

## 12. Presentation

Add one dedicated overview/detail presentation category appropriate to a
service/memory source, e.g.:

```text
ServiceOverview / ServiceDetail
```

Do not make the adapter kind itself a UI type if current architecture already
separates those concepts.

Overview uses:

- status/version;
- Banks/Facts;
- Recall/LLM rates;
- Queue Pending/Failed when available;
- otherwise DB pool/Waiting when useful.

Detail follows `S5_UI_UX.md`.

## 13. Formatting

Reuse existing formatting helpers where possible.

Add pure helpers only where missing for:

- `/min` rate;
- relative age of Hindsight timestamps;
- compact counters.

Do not duplicate current generic freshness formatting unnecessarily.

## 14. Settings editor

Hindsight editor needs:

- Name;
- Enabled;
- Refresh interval;
- API URL;
- optional Web UI URL;
- optional API key.

No bank selector.

API key handling uses the current credential system and test-connection override
store.

## 15. Test Connection

Test must not perform full bank aggregation.

Call enough endpoints to validate configured monitoring:

```text
/health
/health/live
/metrics
/v1/default/banks?limit=1&offset=0
```

Suggested report:

```text
Health: OK
Metrics: OK
Banks: OK (27 visible)
```

Authentication follows normal optional-token behavior.

Do not mutate adapter rate baseline/cache from Test Connection.

## 16. Web UI

If `WebUIURL != ""`:

- source detail button: `Open Hindsight`;
- dynamic tray Web UIs entry exists under instance name.

If blank:

- omit both.

No port derivation.

## 17. Security/privacy tests

Prove:

- absent credential sends no Authorization header;
- configured credential sends exact Bearer header;
- key never appears in returned user-facing errors;
- bank identity/profile fields from fixture are ignored;
- normalized snapshot contains only aggregate data;
- no per-bank URL requested;
- pagination makes only list-page requests.

Use clearly fake fixture IDs/names.

## 18. Request-budget tests

With slow cache fresh, one Refresh should issue exactly:

```text
/health
/metrics
```

and zero liveness/bank requests.

When slow cache expires:

```text
/health
/metrics
/health/live
/banks page(s)
```

No unrelated endpoint.

Tests should use a fake clock or injected time seam rather than sleeps.

## 19. Version

After implementation and verification:

```text
Version 0.5.0
Build   5
```

Follow current metadata-mirror conventions.

## 20. Non-goals

Do not implement:

- bank stats;
- memory content;
- bank drill-down;
- Hindsight admin actions;
- LLM health;
- traces;
- charts/history;
- persistent counters;
- cloud-account billing;
- auto-detection of Hindsight instances.
