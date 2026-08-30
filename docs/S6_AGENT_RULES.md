# S6 Agent Rules — ChatGPT Codex quotas

These rules are authoritative for S6 where older assumptions conflict.

## 1. Source identity

Add compiled source kind `codex`, default display name `ChatGPT Codex`, category `Remote`.
The category reflects the monitored resource (OpenAI/ChatGPT subscription quota),
not the fact that transport is a local subprocess.

## 2. Codex CLI is the integration boundary

S6 talks only to a locally installed Codex CLI via:

```text
<launcher> app-server --stdio
```

and JSON-RPC over stdin/stdout.

Do not read Codex auth/config files, duplicate OAuth/session handling, call
ChatGPT backend endpoints directly, or substitute an OpenAI API key. Codex CLI
owns auth refresh and backend compatibility.

## 3. Launcher semantics

Default launcher: `codex`.
It may be a bare PATH-resolved name or an explicit executable/wrapper path.
AI Monitor always appends `app-server --stdio` as argv. Never evaluate a shell
command string. S6 does not expose arbitrary prefix args; custom launch chains
belong in a wrapper executable.

## 4. CLI discovery is mandatory setup UX

The editor must resolve and display launcher status immediately.

Found:

```text
Codex CLI found: /resolved/path
```

Missing default launcher:

```text
Codex CLI not found in PATH.
Install Codex CLI or choose its executable/wrapper below.
```

Explicit paths must clearly report missing/not-executable states. This is visible
before Test Connection. Pure persisted-config validation need not depend on the
filesystem.

## 5. Environment

Inherit the parent environment and apply optional per-instance overrides.
Overrides are persisted as ordinary non-secret config: never log values and tell
users not to put credentials in them. Prefer wrappers for secret-bearing proxy
configuration.

Useful examples: `PATH`, `CODEX_HOME`, `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY`,
`NO_PROXY`. Validate variable names and never perform shell expansion.

## 6. Execution modes

Supported: `on-demand`, `persistent`. Default: `on-demand`.

On-demand: spawn -> initialize -> initialized -> read -> close stdin -> wait/reap.
No process remains after Refresh.

Persistent: start lazily, initialize once, reuse. Deterministically close/reap on
source disable/remove, adapter replacement, app shutdown, or broken connection.
If the manager lacks lifecycle support, add the smallest generic optional-close
mechanism; do not hardcode Codex into manager logic.

## 7. Persistent failure recovery

On EOF, broken pipe, protocol desync, timeout, or child exit: fail current
refresh, tear down/reap the process, discard stream state, and start clean on the
next refresh. Never reuse a JSON-RPC stream after a timed-out request whose late
response could break correlation.

## 8. Test Connection is one-shot

Always use a throwaway on-demand probe, even when draft/saved mode is persistent.
Never leave a process running from an unsaved Settings draft.

## 9. JSON-RPC

Required flow:

1. `initialize`
2. wait for matching response id
3. send `initialized`
4. `account/rateLimits/read`
5. wait for matching response id

Tolerate unsolicited notifications and correlate strictly by id. Do not request
experimental capabilities unnecessarily.

## 10. Read-only account surface

Allowed S6 account method: `account/rateLimits/read` only.

Never call `account/rateLimitResetCredit/consume`, `account/sendAddCreditsNudgeEmail`,
`account/logout`, `account/login/start`, `account/usage/read`, or `account/read`.
The latter is unnecessary and adds account email/PII.

## 11. Rate-limit selection

Prefer `rateLimitsByLimitId["codex"]`; fall back to top-level `rateLimits`.
Do not assume the map has only one family.

## 12. Windows are data-driven

Use `usedPercent`, optional `windowDurationMins`, optional `resetsAt`. Do not
hardcode 5h/week as protocol assumptions. Friendly labels may special-case 300
minutes -> 5-hour and 10080 -> Weekly. Missing reset means no countdown, not
failure when usage is otherwise valid.

## 13. Optional metadata

Useful optional fields: `planType`, `rateLimitReachedType`, and
`rateLimitResetCredits.availableCount`. Do not require them for quota success.
Do not expose reset-credit IDs/details. Purchased-credit/spend-control fields are
not required UI in S6.

## 14. Existing quota presentation

Reuse used-oriented quota semantics/components: `NN% used`, fill = used,
absolute ResetAt countdown. Do not create a second Codex-specific quota visual
language.

## 15. Poll defaults

Default interval: `5m`. Allow suitable existing Remote presets including `1m`.
Persistent users may choose 1m. No sub-minute preset for this source.

## 16. Managed daemon is out of scope

Do not start/stop/bootstrap/depend on `codex app-server daemon` or
`codex app-server proxy`. A future attach-only mode is separate work.

## 17. No extra binary prerequisite

Research showed account-only flow does not start `codex-code-mode-host`.
Require only the Codex CLI launcher. Do not probe for code-mode-host during
setup; surface future CLI failures normally if upstream behavior changes.
