# S5 Agent Rules — Hindsight

These rules are authoritative for S5 where older assumptions conflict.

## 1. Scope

S5 adds one compiled source kind:

```text
hindsight
```

It integrates into the existing dynamic-instance architecture. It is not a
plugin and must not trigger a provider-framework rewrite.

Hindsight is categorized as **Local** in AI Monitor. "Local" is a product
grouping for user-controlled/local infrastructure; it does not mean the URL
must resolve to localhost.

## 2. Aggregate-only privacy boundary

AI Monitor must not present or persist Hindsight bank identity/content.

Do not expose:

- bank IDs;
- bank names;
- missions/background;
- memory/fact text;
- entities or graph data;
- observations;
- mental models;
- directives;
- document IDs/names;
- operation payloads tied to a bank.

The bank-list response may contain identifying fields, but the adapter DTO
should model only the fields needed for aggregate calculation.

The normalized domain snapshot must not have a place to store bank identifiers.

## 3. No per-bank fan-out

Never poll:

```text
/v1/default/banks/{bank_id}/stats
```

or any other endpoint once per bank.

The source is deliberately deployment-summary-only.

The only bank endpoint in S5 is:

```text
GET /v1/default/banks
```

Use pagination only when the server's `total` requires it.

## 4. Backlog metrics are opportunistic

These metrics are disabled by default in Hindsight:

```text
hindsight_async_operations
hindsight_consolidation_backlog
hindsight_consolidation_failed
```

S5 must work completely without them.

If they appear in `/metrics`, aggregate and display them.

Do not:

- require the user to enable them;
- display an error when absent;
- infer zero merely because a metric family is absent.

Absent means "not available".

## 5. Minimal polling

Fast-changing data:

```text
/health
/metrics
```

Slow-changing data:

```text
/health/live
/v1/default/banks
```

Use an adapter-local approximately 60-second cache/refresh interval for slow
data. First source refresh fetches both fast and slow data.

Do not add `/version` to routine polling.

## 6. Optional authentication

Hindsight self-hosted deployments can be unauthenticated; protected/cloud
deployments can require Bearer auth.

Reuse existing instance-scoped:

```text
api-key
```

credential slot.

Rules:

- credential absent -> send no Authorization header and continue normally;
- credential present -> `Authorization: Bearer <key>`;
- 401/403 -> normal categorized authentication error;
- keychain failure other than "not found" -> categorized credentials error.

Unlike OpenRouter, absence of the key is not a missing capability/error because
unauthenticated Hindsight is valid.

## 7. Prometheus metrics are input, not a new subsystem

S5 reads Hindsight's Prometheus exposition at `/metrics`.

Implement only enough parsing/aggregation for the explicitly selected metric
families. Do not add:

- a Prometheus server;
- PromQL;
- scrape configuration;
- service discovery;
- persistent TSDB/history.

Prefer a small focused parser over pulling a large monitoring dependency tree.
If using an external parser is materially safer, keep the dependency minimal and
explain the tradeoff in the implementation report.

## 8. Rate derivation

Rates come from counter deltas between successful adapter samples.

No persistence is required.

Rules:

- first sample -> rate unavailable;
- counter reset/decrease -> reset baseline, rate unavailable for that interval;
- non-positive elapsed duration -> rate unavailable;
- missing family -> unavailable, not zero;
- aggregate labels before deriving user-visible deployment-wide values;
- never retain bank-level labels in normalized snapshot state.

Rates may be shown as `/min`.

## 9. Existing stale semantics

Do not redesign `source.Manager`.

A request required for the current refresh that fails may fail the refresh as a
whole; manager keeps last-good snapshot and marks it stale.

Optional absent metric families are not failures.

## 10. Preserve prior releases

Do not regress:

- S3 dynamic instance CRUD/name/order;
- S4 OpenRouter;
- global privacy setting;
- current source error/stale behavior;
- existing Overview grid;
- Web UIs tray behavior;
- current Local/Remote detail tabs.
