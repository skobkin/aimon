# Task — S6 / v0.6.0: ChatGPT Codex subscription quota source

Implement a new `codex` Remote source that reads ChatGPT Codex subscription
rate-limit windows through a local Codex CLI app-server.

## Required deliverables

1. `codex` source kind, default name `ChatGPT Codex`, Remote category.
2. Settings: launcher executable (default `codex`), On demand/Persistent mode,
   optional non-secret environment overrides.
3. Clear in-editor Codex CLI discovery status, including explicit install/choose
   executable guidance when missing.
4. argv-only launcher `<executable> app-server --stdio`, wrappers supported,
   effective PATH respected.
5. Minimal JSON-RPC client with initialize, initialized, exact
   `account/rateLimits/read`, id correlation, notification tolerance.
6. On-demand lifecycle with guaranteed reaping.
7. Persistent-owned lifecycle with deterministic Close/restart behavior.
8. Generic manager lifecycle hook if persistent disposal requires it.
9. Snapshot normalization: prefer `rateLimitsByLimitId["codex"]`, fallback
   `rateLimits`; primary/secondary windows; used%, optional duration/reset;
   optional plan/reached state/reset-credit count.
10. Reuse existing quota Overview/detail widgets.
11. One-shot Test Connection.
12. Version 0.6.0 / Build 6.
13. Thorough process/RPC/config/UI tests.

## Hard acceptance conditions

- no direct ChatGPT backend HTTP;
- no reading/parsing Codex auth/config files;
- no API key;
- no account/read or account/usage/read;
- no reset-credit consume/mutating account calls;
- no session/thread creation;
- no managed daemon ownership;
- no shell command execution;
- default `codex` resolved from effective PATH;
- missing CLI clearly visible in Settings before Test Connection;
- explicit wrapper works;
- environment values not logged;
- Test Connection leaves no process;
- on-demand leaves no process after refresh;
- persistent closes on disable/remove/replacement/app shutdown;
- notifications cannot confuse response correlation;
- one valid quota window is enough;
- absent optional fields do not become fake zeros;
- existing MiniMax/Z.AI quota behavior does not regress.

## Workflow

1. Inspect current HEAD after S5.
2. Read S6 docs and decision log.
3. Produce concrete implementation plan and wait for approval.
4. Implement only after approval in focused commits.
5. Run:

```sh
gofmt -w .
go test ./...
go vet ./...
go build ./...
```

6. Report commits, lifecycle design, process/RPC tests, missing-binary UX, Test
Connection behavior, verification, deviations and uncertainties.

Do not commit real account data, personal paths, wrapper internals, proxy
credentials, emails, account IDs, reset-credit IDs, or raw auth/backend payloads.
