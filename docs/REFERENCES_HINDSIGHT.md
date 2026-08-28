# Hindsight upstream references for S5

Research date: **2026-08-27**.

Repository:

https://github.com/vectorize-io/hindsight

Research snapshot inspected around upstream commit:

```text
dc1664d0c7416598c5a68a9cd70467ac38ed8bda
```

Generated API models at that snapshot identify the OpenAPI document as `0.9.2`.

## Monitoring

https://github.com/vectorize-io/hindsight/blob/main/skills/hindsight-docs/references/developer/monitoring.md

Documents:

- `/metrics`
- `/health/live`
- `/health/ready`
- `/health`
- operation metrics
- LLM calls/tokens
- HTTP metrics
- DB pool metrics
- process metrics

Prometheus examples show normalized metric names such as:

```text
hindsight_operation_total
hindsight_llm_calls_total
hindsight_llm_tokens_input_total
hindsight_http_requests_total
hindsight_db_pool_size
```

## Configuration / optional backlog metrics

https://github.com/vectorize-io/hindsight/blob/main/skills/hindsight-docs/references/developer/configuration.md

Relevant variables:

```text
HINDSIGHT_API_METRICS_BACKLOG_ENABLED
HINDSIGHT_API_METRICS_INCLUDE_BANK_ID
```

Backlog-enabled metric families:

```text
hindsight_async_operations
hindsight_consolidation_backlog
hindsight_consolidation_failed
```

Backlog metrics are disabled by default and S5 must not require them.

## Current bank list models

Generated Python model:

https://github.com/vectorize-io/hindsight/blob/main/hindsight-clients/python/hindsight_client_api/models/bank_list_item.py

Relevant fields:

```text
fact_count
last_document_at
last_write_at
```

Response model:

https://github.com/vectorize-io/hindsight/blob/main/hindsight-clients/python/hindsight_client_api/models/bank_list_response.py

Relevant response fields:

```text
banks
total
limit
offset
```

Pagination tests:

https://github.com/vectorize-io/hindsight/blob/main/hindsight-api-slim/tests/test_list_banks_pagination.py

## Backlog implementation/tests

https://github.com/vectorize-io/hindsight/blob/main/hindsight-api-slim/tests/test_backlog_metrics.py

Important semantics:

```text
hindsight_async_operations{operation_type,status}
hindsight_consolidation_backlog
hindsight_consolidation_failed
```

The async-operations metric is queue/task depth. Consolidation backlog counts
unconsolidated source memories; these must not be combined.

## Control Plane

Installation:

https://github.com/vectorize-io/hindsight/blob/main/skills/hindsight-docs/references/developer/installation.md

Typical Docker defaults:

```text
API           http://localhost:8888
Control Plane http://localhost:9999
```

S5 deliberately does not infer one URL from the other.

## Endpoint deliberately excluded

Per-bank LLM health performs real provider calls and is explicitly not intended
for polling:

```text
POST /v1/default/banks/{bank_id}/health/llm
```

Do not use it.
