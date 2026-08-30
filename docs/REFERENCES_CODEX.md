# S6 references — Codex subscription quota

Research date: **2026-08-29**.

Primary evidence: controlled local research against Codex CLI 0.150.1 plus the
public OpenAI Codex repository: https://github.com/openai/codex

Relevant areas:

```text
codex-rs/app-server/README.md
codex-rs/app-server-protocol/
codex-rs/backend-client/src/client.rs
codex-rs/backend-client/src/client/rate_limit_resets.rs
codex-rs/cli/src/main.rs
```

The documented app-server account surface includes `account/rateLimits/read`,
`account/rateLimits/updated`, reset-credit consume, and usage reads. S6 uses only
`account/rateLimits/read` plus initialize/initialized protocol messages.

Current public backend-client code routes the ChatGPT usage read to a
`/backend-api/wham/usage` path; S6 deliberately does not call that endpoint
itself. The local Codex CLI remains the auth/compatibility boundary.

The public CLI also exposes `codex app-server daemon ...` and `codex app-server
proxy`; S6 intentionally uses only a dedicated `codex app-server --stdio`
process and does not own the shared daemon.

Sanitized local research observations:

```text
spawn -> initialize        ~0.33–0.42 s
rateLimits/read RTT        ~0.94–1.22 s
total on-demand probe      ~1.36–1.60 s
clean EOF shutdown         ~15–32 ms
Codex RSS during probe     ~90–100 MB
```

Persistent idle process stayed around ~100 MB during the short observation.
Account-only probing did not create Codex sessions/threads or start code-mode
host, MCP servers, plugin processes, SessionStart hooks, Hindsight, or herdr.
On-demand startup did cause normal Codex-owned bookkeeping/cache writes, which
motivates the 5-minute default interval.

Unsolicited app-server notifications can arrive after initialize; clients must
match responses by JSON-RPC request id.

S6 requires a compatible Codex CLI executable or wrapper. A separate
`codex-code-mode-host` prerequisite is not required for the account-only path
based on current research. If `codex` is not found in PATH, the Settings UI must
explicitly instruct the user to install Codex CLI or choose its executable/wrapper.
