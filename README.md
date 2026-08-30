# AI Monitor [![ci](https://ci.skobk.in/api/badges/22/status.svg)](https://ci.skobk.in/repos/22)

AI Monitor is a desktop tray monitor for local AI workloads and LLM
subscription plans. It lives in the system tray and keeps an eye on your AMD
GPU, your local model router, and the quotas and spending of the cloud plans
you use — without ever opening a browser tab.

<!-- Screenshots to add:
![Monitor window](docs/screenshots/monitor.png)
![Settings window](docs/screenshots/settings.png)
-->

## Features

- Compact borderless monitor window launched from the tray, with Overview,
  Local and Remote tabs.
- Multiple instances per source: monitor several accounts or endpoints side by
  side, each with its own poll interval.
- Credentials are stored in the system keychain — never in config files or
  logs.
- Optional masking of sensitive values such as spending, for screenshots and
  shared screens.
- Sources are independent: one failing or disabled source never breaks the
  others, and the last good data stays visible while a source retries.

## Monitored sources

| Source | Type | Shows |
|---|---|---|
| AMD GPU via [amdgputop-web](https://github.com/skobkin/amdgputop-web) | Local | GPU and VRAM utilization, temperature; power, GTT and clocks in detail |
| [llama-swap](https://github.com/mostlygeek/llama-swap) | Local | Running models and service health |
| [Hindsight](https://github.com/vectorize-io/hindsight) | Local | Deployment health, memory banks and facts, activity rates |
| MiniMax Token Plan | Remote | 5-hour and weekly remaining quota |
| Z.AI GLM Coding Plan | Remote | 5-hour token quota and monthly MCP usage quota |
| OpenRouter | Remote | Key usage and spend limit, account credits and balance |
| ChatGPT Codex | Remote | Plan, 5-hour rate-limit windows, remaining credits |

## Download

Grab a prebuilt binary from the
[releases page](https://git.skobk.in/skobkin/aimon/releases): `aimon` for Linux
amd64 (`tar.gz`) and Windows amd64 (`zip`).

On KDE Plasma (Wayland), see [packaging/README.md](packaging/README.md) for the
taskbar icon setup.
