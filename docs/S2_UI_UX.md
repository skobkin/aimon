# S2 UI / UX Specification — Compact Overview and Reset Countdown

## 1. Goal

S2 does not redesign the whole application. The current v0.1 UI is already
usable and is the baseline.

This iteration has two goals:

1. add quota reset countdown without making Remote cards taller;
2. turn Overview into a denser, genuinely glanceable summary rather than a
   vertical copy of Local + Remote.

The approved concept mockup is stored at:

`docs/mockups/overview-s2-concept.png`

The mockup fixes hierarchy and intent, not exact Fyne pixels. This document wins
where generated artwork differs from the textual contract.

## 2. Stable application structure

Keep the existing monitor structure:

```text
Overview | Local | Remote                         [refresh] [settings]
```

Keep the separate Settings window.

Keep the current direct `fyne.io/systray` integration/workaround. Do not revert
tray handling to Fyne's higher-level system tray implementation during this UI
task.

Local and Remote detail tabs remain substantially as they are today.

## 3. Per-source freshness and graceful degradation

Every Overview card keeps the existing header grammar:

```text
Source name                                      5s ago
```

Possible states remain:

- `5s ago`
- `2m ago · stale`
- `Loading…`
- `Unavailable`

Overview must preserve last-good data for stale sources.

To protect density, Overview does **not** need to repeat the verbose
`Last refresh failed: ...` footer. The status/freshness label is sufficient
there. Detailed Local/Remote cards continue to show concise failure detail as
they do now.

An enabled source with no valid snapshot must still occupy its Overview slot
and show a concise no-data/unavailable body; one failed source must not disturb
the other cards or the grid.

## 4. Overview layout

Overview keeps two sections:

```text
Local
[ amdgputop-web ] [ llama-swap ]

Remote
[ MiniMax        ] [ Z.AI / GLM ]
```

Rules:

- when a section has two enabled sources, render two equal-width cards in one
  row;
- when it has only one enabled source, let that card use the available width;
- disabled sources are omitted;
- if a whole section is disabled, omit that section;
- if no sources are enabled at all, keep the existing global empty state;
- the monitor remains vertically scrollable as a safety net, but the normal
  four-source case should fit comfortably in the existing monitor window.

Do not force Local and Remote detail tabs into a two-column layout. This is an
Overview-specific density optimization.

## 5. Overview presentation kinds

Overview presentation is based on data shape, not provider identity.

The UI/view-model layer should distinguish at least:

```text
OverviewCard
├── MetricsOverview
├── RuntimeOverview
└── QuotaOverview
```

Current mapping:

| Source | Presentation |
| --- | --- |
| amdgputop-web | MetricsOverview |
| llama-swap | RuntimeOverview |
| MiniMax | QuotaOverview |
| Z.AI / GLM | QuotaOverview |

These are presentation concepts only. Do not add `GPUProvider`,
`QuotaProvider`, etc. to adapter/domain architecture.

Detailed Local/Remote cards may continue to use the existing `CardVM` and
body-rendering path. Prefer adding an Overview-specific VM over bending the
detail VM until it serves two incompatible layouts.

## 6. MetricsOverview — amdgputop-web

The compact Overview card contains:

1. one labeled usage bar for GPU utilization;
2. one labeled usage bar for VRAM utilization when both used and total are
   known;
3. one plain temperature row.

Conceptual layout:

```text
┌ amdgputop-web                                  4s ago ┐
│ [██████████████████   GPU 89%   █░░]                   │
│ [██████       VRAM 3.9 / 16.0 GiB       ░░░░░]         │
│ Temp                                            66 °C   │
└─────────────────────────────────────────────────────────┘
```

The ASCII fill is illustrative only.

### 6.1 Labeled metric usage bar

The label/value must be combined in the bar; do **not** add a separate left
label column such as:

```text
GPU:  [bar]
VRAM: [bar]
```

That wastes space and causes alignment problems with labels of different
length.

Use a small neutral semi-transparent pill over the bar for the first revision.
Examples of pill text:

- `GPU 89%`
- `VRAM 3.9 / 16.0 GiB`

Requirements:

- bar fill = current used/load fraction;
- fill color = existing `MetricLevel` color semantics;
- the track is neutral;
- pill background is neutral and translucent enough to separate the text from
  both filled and unfilled portions;
- text remains readable in both dark and light themes;
- the pill must stay inside the bar bounds; do not let it render wider than the
  track;
- do not implement the more complex two-color clipped-text renderer in this
  task unless the simple pill approach proves unusable.

If a fraction cannot be computed reliably (for example VRAM total missing), do
not fake a bar. Fall back to a compact plain textual metric or omit it according
to the existing missing-data conventions.

Temperature stays a plain label/value row and keeps the current load color.

## 7. RuntimeOverview — llama-swap

Keep this card deliberately textual. No chart/gauge is useful for the current
llama-swap data.

Concept:

```text
┌ llama-swap                                     4s ago ┐
│ 2 models running                                      │
│ BGE-M3 Embedding 8k                                   │
│ Gemma 4 E4B LS 32k                                    │
└────────────────────────────────────────────────────────┘
```

Rules:

- include a concise summary (`No models running`, `1 model running`,
  `N models running`);
- show at most two model names in the compact Overview;
- when more are running, append a concise `+N more` line if it fits the chosen
  card height without making the grid awkward;
- Local detail continues to show the full list;
- do not invent activity/queue information the API does not expose reliably.

## 8. QuotaOverview — MiniMax and Z.AI / GLM

Each quota provider gets two small donut/ring gauges side by side for its
headline windows.

Concept:

```text
┌ MiniMax                                       32s ago ┐
│        ◯                         ◯                    │
│       14%                        5%                   │
│        5h                       week                  │
│      2h14m                     4d7h                   │
└───────────────────────────────────────────────────────┘
```

And similarly for Z.AI / GLM.

### 8.1 Donut semantics

- arc length/fill = **used fraction**;
- central percentage = used percentage, e.g. `62%` (do not add the word
  `used` inside the donut);
- short window label = UI-owned compact label such as `5h`, `week`, `month`;
- ring color = existing quota threshold color derived from **remaining** quota;
- reset countdown appears immediately below the donut;
- raw counts/credits are omitted from Overview;
- no provider-specific explanatory text is shown in Overview.

The existing Remote detail linear bars remain unchanged apart from adding the
inline reset countdown described below.

### 8.2 Window selection

Overview should show at most two headline quota windows. Prefer semantic/stable
window IDs rather than provider-name conditionals:

- 5-hour family: `interval`, `tokens-5h`, `credits-5h` → short label `5h`;
- weekly family: `weekly`, `credits-weekly` → `week`;
- monthly family such as `mcp-monthly` → `month` as a fallback when a weekly
  window is absent.

Prefer 5-hour + weekly when both exist. Keep provider-specific parsing in the
adapter; this selection is purely presentation ordering.

## 9. Reset countdown

The domain stores absolute `ResetAt`. UI code computes relative text from
`ResetAt` and the current local `now`.

Do not trigger extra network requests to update countdowns. The monitor already
re-renders relative time while visible.

Compact formatting:

| Remaining time | Example |
| --- | --- |
| under 1 minute | `42s` |
| under 1 hour | `37m` |
| under 24 hours | `2h14m` |
| 1 day or more | `4d7h` |
| reset reached/passed before provider refresh | `now` |
| unknown | empty string / omit |

Do not spell out `reset` in the normal UI.

## 10. Remote detail quota row

Add reset countdown to the existing first line and **do not add vertical
height** merely for reset information.

With reset:

```text
5-hour · 2h14m                                 17% used
[████████---------------------------------------------]
2051 / 12.0k credits
```

Without reset:

```text
5-hour                                         17% used
[████████---------------------------------------------]
2051 / 12.0k credits
```

Raw count lines remain exactly as detail information; Overview omits them.

## 11. MiniMax reset normalization

The current adapter already decodes:

- `remains_time`;
- `weekly_remains_time`;
- `start_time` / `end_time`.

For S2:

- `remains_time` is a duration in milliseconds until the 5-hour quota window
  resets;
- `weekly_remains_time` is a duration in milliseconds until the weekly quota
  window resets;
- convert each valid duration to absolute `ResetAt = observedAt + duration`;
- do not use `end_time` as the primary 5-hour quota reset;
- explicit legacy reset fields already covered by tests may remain as defensive
  fallbacks when the duration fields are absent;
- malformed/negative/overflowing durations are ignored rather than breaking the
  otherwise valid quota snapshot.

Use a single captured observation time for building the snapshot so the two
windows are internally consistent and tests can be deterministic.

Z.AI already normalizes `nextResetTime` to `ResetAt`; preserve that behavior.

## 12. Tray menu

Rename:

```text
Open >
```

to:

```text
Web UIs >
```

Keep the same enabled-source submenu entries and URLs.

No dynamic tray telemetry in S2.

## 13. Explicitly out of scope

Do not add in this task:

- historical GPU/VRAM charts or ring buffers;
- persistence/history databases;
- dynamic/configurable tray bars or masked tray-icon telemetry;
- load/unload controls;
- provider management actions;
- a generic charting framework;
- relocation/redesign of the existing Web UI buttons on Local/Remote cards;
- new provider integrations.
