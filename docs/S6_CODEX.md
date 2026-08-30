# S6 ChatGPT Codex integration contract

Research baseline: Codex CLI **0.150.1**, experimentally validated on Linux
through an executable wrapper. Remain defensive across compatible versions.

## Process and protocol

Run:

```text
<configured executable> app-server --stdio
```

No shell.

Send newline-delimited JSON-RPC. Minimum flow:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"aimon","title":"AI Monitor","version":"<app version>"}}}
{"jsonrpc":"2.0","method":"initialized"}
{"jsonrpc":"2.0","id":2,"method":"account/rateLimits/read"}
```

Wait for matching response ids. Unsolicited notifications can arrive after
initialize, so never assume the next stdout object is the requested response.
Do not surface unrelated notification payloads.

## Researched response shape

```json
{
  "result": {
    "rateLimits": {
      "limitId": "codex",
      "limitName": null,
      "primary": {"usedPercent": 0, "windowDurationMins": 300, "resetsAt": 1788041155},
      "secondary": {"usedPercent": 30, "windowDurationMins": 10080, "resetsAt": 1788489888},
      "planType": "plus",
      "rateLimitReachedType": null
    },
    "rateLimitsByLimitId": {"codex": {"...": "same snapshot shape"}},
    "rateLimitResetCredits": {"availableCount": 1, "credits": [{"id":"<opaque>","status":"available"}]}
  }
}
```

S6 does not need credit detail IDs/rows.

## Selection and normalization

Selection order:

1. `rateLimitsByLimitId["codex"]` when present;
2. top-level `rateLimits` fallback;
3. otherwise invalid response.

Window schema observed: `usedPercent` required within a window;
`windowDurationMins` nullable; `resetsAt` nullable Unix seconds.

Accept a window only with finite sane usage. Reject materially negative or >100
percent values; tiny float drift near boundaries may be clamped. Convert to
used fraction and absolute ResetAt; never synthesize reset time from duration.

Primary and secondary should be treated independently for forward compatibility.
One usable window is enough; zero usable windows is invalid.

Suggested labels:

```text
300 min     -> 5-hour / 5h
10080 min   -> Weekly / week
60 min      -> 1-hour / 1h
nil         -> Primary / Secondary
other       -> stable human duration label
```

## Optional metadata

`planType` may be shown as Plus/Pro/Go/Free where known; unknown non-empty values
are displayed safely. Never make an extra account call for plan type.

`rateLimitReachedType` may be surfaced as a compact warning in detail.

Use only `rateLimitResetCredits.availableCount`; never store/display opaque
credit IDs or call consume.

## Errors and timeouts

Distinguish missing/not-executable launcher, incompatible/old app-server method,
auth/backend failure, malformed response, timeout, EOF/child exit. Do not include
raw account/auth payloads or raw stderr in user-facing errors.

Research guidance: initialize timeout ~10s, rate-limit read ~15s, graceful exit
bounded to <=10s. Reconcile with the manager's outer Refresh timeout.

A persistent request timeout invalidates the stream: tear down and restart next
refresh.

## On-demand lifecycle

Resolve -> spawn -> initialize -> read -> normalize -> close stdin -> wait/reap.
On cancellation terminate/reap. Never leave a process after refresh.

## Persistent lifecycle

Start/initialize on first refresh, then reuse for reads. On any transport or
protocol failure terminate/reap and clear state; next refresh starts clean.

## Environment and executable resolution

Effective env = parent env + overrides. Override wins by name.
Executable lookup must use the **effective PATH**. Ordinary `exec.LookPath()`
uses the process environment, so when PATH is overridden implement lookup against
the effective env and set `cmd.Path` explicitly. Never run a shell.

If launcher exists but lacks the needed feature, use a message such as:

```text
This Codex CLI does not support the app-server rate-limit interface.
Update Codex CLI or choose a compatible executable.
```
