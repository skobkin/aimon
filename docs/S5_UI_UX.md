# S5 UI/UX — Hindsight

Hindsight is an aggregate Local service card.

The goal is to answer:

1. Is the memory service healthy?
2. How much aggregate memory exists?
3. Is it actively processing/recalling?
4. Is work queued or failing?
5. Is the DB pool under pressure?

without revealing which projects/banks exist.

## 1. Settings editor

Hindsight source:

```text
Type          Hindsight
Name          Hindsight
Enabled       [x]
Refresh every [existing Local presets]

API URL       [ http://127.0.0.1:8888 ]
Web UI URL    [                         ]  optional

API key       [ ...................... ]  optional
              No credential stored
```

Use the existing credential grammar.

API key explanation:

```text
Optional Bearer token for protected/cloud Hindsight APIs
```

Blank Web UI URL is valid.

No setting for backlog metrics. Detection is automatic.

No setting for bank IDs or filters.

## 2. Test connection

Test should validate the configured surface without retrieving per-bank detail.

Suggested result:

```text
Health: OK
Metrics: OK
Banks: OK (27 visible)
```

The bank check should use a tiny page (`limit=1` is enough for Test Connection)
because the purpose is reachability/schema/auth, not full aggregation.

If API key is absent, test unauthenticated.

Do not call LLM health.

## 3. Overview

Add a dedicated compact service/memory presentation rather than reusing GPU or
quota views.

Example after rate baseline exists:

```text
┌ Hindsight ───────────────────────── 4s ago ┐
│ Healthy · v0.9.2                          │
│                                           │
│ Banks      27          Facts       18.4k │
│ Recall   18/m          LLM calls      4/m│
│ Pending     3          Failed           0 │
└───────────────────────────────────────────┘
```

### Backlog metrics absent

Do not show `Pending 0 / Failed 0` merely because the optional gauges are
missing.

Instead use a useful non-backlog row when available:

```text
DB active  2/10          Waiting          0
```

If DB-waiting itself is unavailable:

```text
DB active  2/10
```

Do not force empty placeholders solely to make the card rectangular.

### First rate sample

Before a counter delta exists, a rate may show:

```text
Recall      —
LLM calls   —
```

Do not display fake `0/m`.

## 4. Overview field priority

Use this priority:

1. health/version;
2. Banks + Facts;
3. Recall rate + LLM-call rate;
4. optional backlog Pending + Failed, otherwise DB pressure.

Do not put every detail metric into Overview.

## 5. Local detail

Suggested structure:

```text
Hindsight                               4s ago

Service
Status                                  Healthy
Version                                   0.9.2
Uptime                                    2d 7h
Database                               connected
DB acquire                               0.4 ms

Memory
Banks                                        27
Facts                                     18.4k
Last write                                3m ago
Last document                             6m ago

Activity
Retain                                      2/m
Recall                                     18/m
Reflect                                     1/m
Consolidation                             0.4/m
LLM calls                                   4/m
Input tokens                             12.4k/m
Output tokens                             1.8k/m

Queue
Pending                                       3
Processing                                    1
Failed                                        0
Consolidation backlog                         8
Consolidation failed                          0

Runtime
DB pool active                           2 / 10
DB waiting                                    0
HTTP in progress                              1
HTTP 5xx                                   0.0%

[ Open Hindsight ]
```

Sections/rows are conditional.

If optional Queue metrics are absent, omit the entire Queue section.

If a metric/rate has never been observed and showing `—` adds no value in
detail, it may be omitted. Use consistent behavior within each section.

## 6. Relative timestamps

`Last write` and `Last document` use compact relative time consistent with
existing freshness/reset formatting where possible:

```text
42s ago
3m ago
2h ago
4d ago
```

Do not show raw RFC3339 in the normal card.

## 7. Number formatting

Reuse current compact integer/byte formatting where it exists.

Examples:

```text
Facts          18.4k
Tokens         12.4k/m
Banks             27
```

Rates under 1/min may retain one useful decimal:

```text
0.4/m
```

Do not round a small non-zero rate to misleading `0/m`; use `<0.1/m` or enough
precision according to shared formatting.

## 8. Status colors

Do not invent an elaborate new threshold system.

Suggested:

- service/database healthy: normal/green status text if existing component
  supports semantic status color cleanly;
- backlog failed > 0: error/red;
- DB waiting > 0: warning/amber;
- HTTP 5xx > 0: warning/error according to existing semantic colors.

If current UI lacks a clean semantic row color abstraction, neutral text is
acceptable in S5; correctness is more important than custom paint.

## 9. Dynamic instances

All existing behavior applies:

- multiple Hindsight instances;
- custom names;
- ordering;
- enable/disable;
- independent Base URLs;
- independent optional credentials;
- independent rate baselines and slow caches.

No bank list appears in Settings or monitoring UI.
