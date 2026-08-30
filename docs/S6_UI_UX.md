# S6 UI/UX — ChatGPT Codex subscription quotas

## Settings

Suggested editor:

```text
Type             ChatGPT Codex
Name             ChatGPT Codex
Enabled          [x]
Refresh every    [5m v]

Mode             [On demand v]

Codex executable [ codex                         ]
                  Codex CLI found: /usr/bin/codex

Environment overrides (advanced)
                  [ Add override ]

                  Values are stored in app settings.
                  Do not put secrets here; use a wrapper
                  for secret-bearing proxy configuration.
```

If `codex` cannot be resolved, show directly in the editor:

```text
Codex CLI not found in PATH.
Install Codex CLI or choose its executable/wrapper below.
```

For explicit launchers show `Executable found` or `Executable not found or not
executable.` Do not require a code-mode-host field.

Launcher help:

```text
AI Monitor runs:
<executable> app-server --stdio
Executable wrappers are supported.
```

Mode help:

```text
On demand: Starts Codex only while refreshing. Uses no memory between refreshes.
Persistent: Keeps a private Codex app-server running. Avoids repeated startup
work but uses about 100 MB of memory.
```

Do not auto-change an explicitly chosen refresh interval when mode changes.
Recommend 5m on-demand and 1m as reasonable for persistent.

## Environment overrides

Prefer repeatable key/value rows with strict variable-name validation, no
duplicates, literal values, no shell or `${...}` expansion, and no logging of
values. A strict multiline KEY=VALUE editor is acceptable only if repeatable rows
would greatly expand S6; preserve everything after the first `=`.

## Test Connection

Always one-shot. Example:

```text
Codex CLI: OK
ChatGPT: OK · Plus
Rate limits: OK · 2 windows
```

Missing default executable:

```text
Codex CLI: not found in PATH
Install Codex CLI or choose its executable/wrapper.
```

Incompatible version:

```text
Codex CLI: incompatible
This version does not support account/rateLimits/read.
```

Do not show account email/ID/token.

## Overview

Reuse the current quota donuts:

```text
┌ ChatGPT Codex ─────────────────── 32s ago ┐
│ Plus                                      │
│                                           │
│       0%                  30%              │
│       5h                  week             │
│      4h52m                5d7h             │
└───────────────────────────────────────────┘
```

One usable window should render cleanly without a fake empty second donut.
Absent reset timestamp means no countdown.

## Detail

Reuse current quota detail row layout. Add an optional compact Account section:

```text
Account
Plan                                        Plus
Reset credits                                  1

Limits
5-hour · 4h52m                         0% used
Weekly · 5d7h                          30% used
```

If `rateLimitReachedType` is present, show a compact status/warning. No reset
buttons. Do not expose reset-credit IDs/details.

Missing optional provider fields are omitted, not replaced by fake zeros.
Quota percentage, plan name, and reset-credit count need not be hidden by the
global money privacy setting. Environment override values stay inside Settings.
