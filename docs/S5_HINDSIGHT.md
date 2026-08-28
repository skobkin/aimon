# S5 Hindsight Integration Contract

Research snapshot: **2026-08-27**.

Current upstream generated API clients in the Hindsight repository identify the
OpenAPI document as **0.9.2**. Implement defensively because the user's
self-hosted instance may run another compatible release.

## 1. Configured endpoints

Given configured API base URL:

```text
http://127.0.0.1:8888
```

S5 uses only:

```text
GET /health
GET /health/live
GET /metrics
GET /v1/default/banks?limit=1000&offset=N
```

No `/version` polling.

No per-bank endpoint.

## 2. Health

### `/health/live`

Hindsight documents the liveness response as containing:

```json
{
  "status": "alive",
  "version": "0.x.y",
  "uptime_seconds": 812.4
}
```

Use it for:

- service version;
- process uptime.

This endpoint does not check the DB.

### `/health`

`/health` is an alias of readiness and checks database connectivity.

Normal healthy deployments commonly return fields such as:

```json
{
  "status": "healthy",
  "database": "connected"
}
```

Newer versions can also expose diagnostic fields including:

```text
db_acquire_ms
db_pool_waiting
db_pool_in_use
db_pool_max
```

Treat those diagnostics as optional.

S5 does not need to duplicate Kubernetes probe semantics. Use the normal source
error/stale model for request failures/non-success unless current project error
handling already provides a clean way to preserve a degraded health payload.

## 3. Bank list

Current response shape is paginated:

```json
{
  "banks": [
    {
      "fact_count": 120,
      "last_document_at": "2026-08-27T00:00:00Z",
      "last_write_at": "2026-08-27T00:01:00Z"
    }
  ],
  "total": 27,
  "limit": 1000,
  "offset": 0
}
```

Actual upstream items also contain bank identity/profile fields. S5 must not
model or retain them.

Aggregate:

```text
BankCount       = response.total
FactCount       = sum(fact_count across every fetched page)
LastDocumentAt  = max(non-null last_document_at)
LastWriteAt     = max(non-null last_write_at)
```

`fact_count` is optional in the upstream model and defaults to 0.

### Pagination

Request:

```text
limit=1000
offset=0
```

If:

```text
offset + len(banks) < total
```

fetch the next page at the next offset.

Stop when all `total` items are covered.

Defend against malformed pagination that makes no progress; do not infinite-loop.
A page returning zero rows before reaching total is an invalid response.

Never convert pagination into calls to individual banks.

## 4. Prometheus metrics

Hindsight documents OTel metric names using dots, while Prometheus exposition
normalizes them to underscore-style names. The documented Prometheus examples
use names such as:

```text
hindsight_operation_total
hindsight_llm_calls_total
hindsight_llm_tokens_input_total
hindsight_llm_tokens_output_total
hindsight_http_requests_total
hindsight_http_requests_in_progress
hindsight_db_pool_size
hindsight_db_pool_idle
hindsight_db_pool_min
hindsight_db_pool_max
```

Newer runtime diagnostics may expose:

```text
hindsight_db_pool_waiting
```

Optional backlog metrics:

```text
hindsight_async_operations
hindsight_consolidation_backlog
hindsight_consolidation_failed
```

Do not fail when a documented metric family is absent; many counters are
created only after relevant activity.

## 5. Operation metrics

From:

```text
hindsight_operation_total{operation="...", ...}
```

aggregate across all other labels and retain counters for at least:

```text
retain
recall
reflect
consolidation
```

Convert deltas to rates per minute.

Other operation types can be ignored in S5 unless needed for the total queue
gauges.

Do not expose `bank_id`, provider/model, source, budget, or token-limit labels in
the UI.

## 6. LLM metrics

Aggregate across every label:

```text
hindsight_llm_calls_total
hindsight_llm_tokens_input_total
hindsight_llm_tokens_output_total
```

Derive:

```text
LLM calls / min
input tokens / min
output tokens / min
```

S5 intentionally does not show provider/model/scope breakdown.

## 7. HTTP metrics

Aggregate:

```text
hindsight_http_requests_total
```

Keep:

- all requests counter;
- 5xx requests counter (`status_class="5xx"`).

For each interval with valid deltas:

```text
HTTP 5xx % = delta5xx / deltaAll * 100
```

If denominator delta is zero, rate is unavailable rather than NaN.

`hindsight_http_requests_in_progress` may be summed and displayed as a current
gauge in detail.

## 8. DB pool metrics

Current gauges:

```text
size
idle
min
max
```

Derive when available:

```text
active = max(size - idle, 0)
```

Display:

```text
DB pool active   active / max
DB waiting       waiting
```

`waiting` is optional.

Do not interpret absent waiting as zero.

## 9. Optional backlog gauges

If present, aggregate all series across any tenant/bank labels exposed by the
metrics endpoint.

### Async operations

```text
hindsight_async_operations{operation_type,status}
```

Aggregate by status across operation types:

```text
pending
processing
failed
```

### Consolidation

```text
hindsight_consolidation_backlog
hindsight_consolidation_failed
```

Sum all exposed series.

Important:

```text
hindsight_consolidation_backlog
```

is the count of source memories not yet consolidated, while
`hindsight_async_operations` is operation/task queue depth. They are different
metrics and must not be combined.

## 10. Process metrics

Hindsight also exposes process CPU, memory, FD, and thread metrics.

S5 does **not** need to show all generic process telemetry.

If `hindsight_process_memory_bytes{type="rss_max"}` is straightforward to parse,
it may be retained/displayed in detail as RSS/Max RSS according to actual
metric semantics, but it is optional for S5 acceptance.

Do not expand S5 into a process-monitoring dashboard.

## 11. Rate baseline and slow-data cache

Adapter ephemeral state may contain:

```text
previous aggregate counters
previous counter timestamp
cached bank summary
cached liveness/version/uptime
slow-data fetched-at timestamp
```

Nothing is persisted to disk.

On adapter rebuild caused by Base URL settings change:

- baseline resets;
- slow cache resets.

Rename/order/enable behavior remains governed by the existing dynamic-source
manager rules.

## 12. Authentication

If configured API key exists:

```http
Authorization: Bearer <key>
```

Send it to Hindsight requests.

If absent, send no Authorization header.

Do not treat missing key as a configuration error.

## 13. Web UI

Hindsight Control Plane commonly runs separately from the API (for the standard
Docker setup, API is commonly port 8888 and UI port 9999), but deployments can
proxy or relocate it.

Therefore S5 config has an independent optional:

```text
Web UI URL
```

If blank:

- no per-card Web UI button;
- no tray Web UIs entry for that instance.

Do not derive it by rewriting the API port.

## 14. Explicit non-goals

Not S5:

- `/v1/default/banks/{bank_id}/stats`
- `/v1/default/banks/{bank_id}/health/llm`
- `/version` polling
- per-bank names/cards
- memory browsing
- operation payload/history
- OpenTelemetry traces
- provider/model breakdown
- bank-level graphs
- admin/mutation calls
