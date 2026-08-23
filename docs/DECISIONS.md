# Product and S1 Decisions

This file records decisions that should not be silently revisited during S1 implementation.

## D1 — Product scope

The application is a compact desktop monitor for local LLM-related load and remote subscription quota.

It is not a general monitoring platform, Grafana replacement, log viewer, inference launcher, or provider administration console.

## D2 — Tray-first, no persistent dashboard window

Normal runtime interaction is through a tray icon and a compact borderless monitor window.

A separate decorated window is used for Settings.

There is no normal persistent dashboard window in S1.

## D3 — Monitor window behavior

Use Fyne's desktop tray integration and a splash/borderless window where supported.

Required S1 behavior:

- left-click tray icon shows/hides the monitor window through `desktop.App.SetSystemTrayWindow`;
- pressing `Esc` hides the monitor window;
- the tray menu contains an explicit Show Monitor action for Linux implementations where tray behavior is incomplete;
- Settings opens a normal decorated window.

Desired but not required in S1:

- automatically hide the monitor window when the native top-level window loses focus.

Fyne does not expose a portable top-level window-focus-lost callback. Do not add platform-specific/native hacks merely to implement focus-loss auto-hide. If it cannot be implemented cleanly through public APIs, leave it out and document the limitation.

Do not rely on exact programmatic positioning next to the tray icon. On Wayland a compositor may ignore requested window positions.

## D4 — Navigation

The compact monitor has three stable tabs:

1. Overview
2. Local
3. Remote

Overview is the default tab.

This keeps one-glance status available without sacrificing detailed local/remote views.

Settings is not a fourth monitor tab; it is a separate window.

## D5 — Optional independent sources

S1 sources:

- `amdgputop-web`;
- `llama-swap`;
- MiniMax Token Plan;
- Z.AI / GLM Coding Plan.

Every source can be enabled/disabled independently.

Disabled sources:

- are not polled;
- do not appear as cards on Overview;
- do not cause errors or degraded application status.

An entire Local or Remote tab remains present even if no sources in that category are enabled; it shows a simple empty state with a Settings action.

## D6 — Graceful degradation

An enabled source may fail without affecting other sources.

If a source has never produced valid data and is unreachable, show it as `Unavailable`.

If it previously produced valid data and a later refresh fails:

- retain and display the last successful data;
- mark the source `Stale`;
- show the age of the last successful snapshot;
- expose a concise error message in the detailed view.

Never replace valid stale data with an empty card solely because a refresh failed.

## D7 — Per-source freshness

There is no global `Updated N seconds ago` label.

Each source header shows its own freshness/status on the right, for example:

- `5s ago`
- `1m ago · stale`
- `Unavailable`
- `Loading…`

The relative-age labels may update locally without performing network requests.

## D8 — Polling cadence

Default polling:

- `amdgputop-web`: 5 seconds;
- `llama-swap`: 5 seconds;
- MiniMax: 60 seconds;
- Z.AI: 60 seconds.

S1 minimum configurable intervals:

- local sources: 5 seconds;
- remote sources: 30 seconds.

Use enumerated choices in Settings rather than arbitrary free-text duration input.

Recommended choices:

Local: `5s`, `10s`, `15s`, `30s`, `60s`.

Remote: `30s`, `1m`, `2m`, `5m`, `10m`.

Manual Refresh refreshes all enabled sources immediately, while preserving the one-in-flight-request-per-source rule.

## D9 — Quota semantics

Remote quota is displayed primarily as **remaining quota**, not consumed quota.

For each quota window:

- numeric text says `NN% remaining`;
- the bar fill represents the remaining fraction;
- if trustworthy raw counts are provided, they may appear as secondary detail;
- provider-specific semantics are normalized in adapters before reaching UI.

Quota bar color thresholds by remaining percentage:

- `<= 10%`: red;
- `> 10% and <= 20%`: orange;
- `> 20% and <= 50%`: yellow;
- `> 50%`: green.

The domain may retain both `UsedFraction` and `RemainingFraction` where useful, but the S1 quota UI uses remaining semantics consistently.

A separate "used percentage" color rule may be implemented as a reusable helper for future metrics, but S1 quota bars must not mix used/remaining conventions.

## D10 — Local telemetry coloring

High GPU utilization is not intrinsically bad.

Do not apply quota warning colors to GPU utilization, VRAM use, clocks, or other local utilization metrics.

S1 may use neutral progress bars for utilization. Temperature is shown numerically without generic warning thresholds because safe/meaningful thresholds can be device-specific.

## D11 — One approved custom UI component

Fyne's standard `widget.ProgressBar` uses theme colors and has no per-instance fill-color API.

S1 may implement exactly one reusable custom quota visualization, `QuotaBar`, to support green/yellow/orange/red fills per quota state.

`QuotaBar` must remain small and presentation-only. It must not grow into a general widget framework.

## D12 — Secret storage

S1 uses the system keychain via `github.com/zalando/go-keyring`.

Follow the proven credential-store/status approach already used in `skobkin/qBtRemoteGo`.

There is no ENV-file/plaintext-config fallback in S1.

Non-secret settings may be stored with Fyne Preferences.

If the keychain is unavailable or locked, the application keeps running and affected authenticated sources degrade to an unavailable/stale state.

## D13 — Read-only S1

S1 must not invoke llama-swap unload/load APIs or add other write operations.

Opening an upstream web UI in the browser is acceptable as a navigation action, but no embedded administration controls are required.

## D14 — No charts/history in S1

S1 contains current-value indicators and progress bars only.

No historical database, ring buffer, plots, sparklines, or chart widgets.

## D15 — Local source ownership

Use `amdgputop-web` for AMD GPU telemetry.

Use `llama-swap` for model/runtime state and request/activity information that its API exposes reliably.

Do not duplicate GPU telemetry from llama-swap in S1 merely because `/metrics` may also contain hardware metrics.

## D16 — Implementation technology

Use Go 1.26 and current stable Fyne 2.8.x APIs unless the repository already pins a compatible version for a documented reason.

Use Fyne's post-2.6 threading model correctly: background goroutines perform I/O; UI updates are scheduled through `fyne.Do`/`fyne.DoAndWait`.
