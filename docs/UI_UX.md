# UI / UX Specification

## 1. UX goal

The application should answer the common question "what is happening right now?" in a few seconds without opening a browser or a full monitoring dashboard.

The runtime surface is intentionally compact and systematic.

The user should not have to learn source-specific interaction patterns.

## 2. Window model

S1 has two windows.

### 2.1 Monitor window

- borderless/splash-style Fyne window;
- controlled by the tray icon through `desktop.App.SetSystemTrayWindow`;
- compact, approximately 500–560 device-independent pixels wide;
- height may be around 600–700 DIP and should remain usable with scrolling;
- Overview opens by default;
- `Esc` hides the monitor;
- repeat left-click on tray icon should hide it on platforms where Fyne supports the splash toggle behavior.

Do not create a normal persistent dashboard window.

Do not rely on exact placement next to the tray icon. Fyne/Wayland cannot guarantee requested native coordinates.

Auto-hide on native focus loss is desirable but optional. Do not implement native hooks or private Fyne API to force it.

### 2.2 Settings window

- normal decorated window;
- independent of monitor visibility;
- resizable;
- does not close the whole application when closed;
- re-opening Settings should reuse/show the existing window when practical.

## 3. Tray behavior

### Left click

Show/hide monitor window through Fyne system-tray-window integration.

### Right-click menu

Minimum menu:

```text
LLM Monitor
──────────────
Show Monitor
Refresh
Settings
──────────────
Quit
```

`Refresh` performs an asynchronous refresh of all enabled sources.

Do not fill the tray menu with rapidly changing telemetry in S1. The monitor window is the status surface.

### Tray icon

S1 uses a static, theme-compatible icon.

Dynamic fill/masking, status animation, quota coloring, and other tray-icon visualizations are explicitly deferred.

## 4. Navigation

Use Fyne `container.AppTabs` with three top-level tabs:

```text
Overview | Local | Remote
```

Overview is selected on open.

The tabs remain stable even if a category has no enabled sources.

A category with no enabled sources shows an empty state:

```text
No local sources enabled.
[ Open Settings ]
```

or:

```text
No remote providers enabled.
[ Open Settings ]
```

## 5. Global monitor chrome

Keep it minimal.

Recommended top strip inside the monitor:

```text
LLM Monitor                                      [↻] [⚙]
```

- refresh icon/button: refresh all enabled sources;
- settings icon/button: open Settings window.

There is deliberately no global "Updated N seconds ago" because sources refresh independently.

## 6. Source-card grammar

Every source card uses the same basic composition.

```text
┌────────────────────────────────────────────────────┐
│ Source name                          status / age   │
│                                                    │
│ source-specific compact content                    │
│                                                    │
│ optional concise stale/error note                  │
└────────────────────────────────────────────────────┘
```

Header right-side examples:

- `5s ago`
- `43s ago`
- `2m ago · stale`
- `Loading…`
- `Unavailable`

Use text/icon plus color if color is used. Never rely on color alone.

### Disabled source

Overview: omit the card entirely.

Detailed Local/Remote tab: omit the source card and allow the tab-level empty state if nothing remains.

### Loading, no prior data

```text
MiniMax                              Loading…
Fetching quota…
```

A small infinite progress indicator is acceptable during first load. Do not keep an infinite progress animation on a stale card with valid old data.

### Unavailable, no prior data

```text
MiniMax                           Unavailable
No data available.
Connection failed.
```

Keep error text concise. Full low-level HTTP bodies do not belong in the monitor.

### Stale with last valid data

```text
MiniMax                      7m ago · stale
5-hour     28% remaining  [█████-------------]
Weekly     74% remaining  [██████████████----]
Last refresh failed: HTTP 503
```

The old data remains visible.

## 7. Overview

Overview is optimized for a glance, not completeness.

Suggested layout:

```text
┌────────────────────────────────────────────────────┐
│ LLM Monitor                                  ↻  ⚙  │
├────────────────────────────────────────────────────┤
│ Overview | Local | Remote                          │
├────────────────────────────────────────────────────┤
│ LOCAL                                              │
│ ┌────────────────────────────────────────────────┐ │
│ │ AMD Radeon RX 7800 XT                  3s ago  │ │
│ │ GPU      74%                                  │ │
│ │ VRAM     13.8 / 16.0 GiB                     │ │
│ │ Temp     63 °C                                │ │
│ └────────────────────────────────────────────────┘ │
│                                                    │
│ ┌────────────────────────────────────────────────┐ │
│ │ llama-swap                             4s ago  │ │
│ │ Qwen3.8-27B-IQ4_XS                           │ │
│ │ 1 active · 0 waiting       (only if reliable) │ │
│ └────────────────────────────────────────────────┘ │
│                                                    │
│ REMOTE                                             │
│ ┌────────────────────────────────────────────────┐ │
│ │ MiniMax                                41s ago │ │
│ │ 5-hour   31% remaining  [██████------------]  │ │
│ │ Weekly   82% remaining  [████████████████--]  │ │
│ └────────────────────────────────────────────────┘ │
│                                                    │
│ ┌────────────────────────────────────────────────┐ │
│ │ Z.AI / GLM                            45s ago  │ │
│ │ 5-hour   32% remaining  [██████------------]  │ │
│ │ MCP      88% remaining  [█████████████████-]  │ │
│ └────────────────────────────────────────────────┘ │
└────────────────────────────────────────────────────┘
```

The actual Fyne rendering should use native spacing/widgets and will not look like ASCII boxes. The wireframe fixes information hierarchy, not pixels.

### Overview density

Keep cards compact:

- AMDGPU: GPU utilization, VRAM, temperature only;
- llama-swap: running model(s), then active/waiting only if reliably available;
- remote providers: quota windows only.

Details such as power, clocks, raw counts, source URL, and error detail belong on Local/Remote tabs.

If there are many running llama-swap models, Overview may show the first few plus `+N more`; the Local tab shows the full list.

## 8. Local tab

The Local tab provides current detail without becoming an `amdgputop-web` clone.

```text
LOCAL

┌────────────────────────────────────────────────────┐
│ AMD Radeon RX 7800 XT                     3s ago  │
│ GPU utilization     74%                           │
│ VRAM                13.8 / 16.0 GiB              │
│ GTT                 2.1 / 64.0 GiB      if avail │
│ Temperature         63 °C                        │
│ Power               172 W               if avail │
│ SCLK                2420 MHz             if avail │
│ MCLK                1249 MHz             if avail │
└────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────┐
│ llama-swap                                4s ago  │
│ Status              Online                        │
│ Running models                                     │
│   Qwen3.8-27B-IQ4_XS                              │
│   bge-m3                                            │
│ Active requests     1                    if avail  │
│ Waiting requests    0                    if avail  │
│                                                    │
│ [ Open llama-swap UI ]                             │
└────────────────────────────────────────────────────┘
```

### Local telemetry rules

- Values unsupported by the source are omitted, not shown as fake zeroes.
- A whole enabled source that is unreachable stays visible as unavailable/stale.
- Individual fields that are absent in an otherwise valid response may show `—` in the detailed view if layout stability benefits from it; Overview should prefer omission.
- Do not add the GPU process list in S1.
- Do not add charts.
- Do not duplicate llama-swap GPU monitoring metrics; GPU data comes from `amdgputop-web`.

## 9. Remote tab

Remote presents provider quota in a consistent normalized form.

```text
REMOTE

┌────────────────────────────────────────────────────┐
│ MiniMax                                   41s ago │
│ 5-hour window                                     │
│ 31% remaining                                     │
│ [██████------------------------------------------] │
│                                                    │
│ Weekly                                            │
│ 82% remaining                                     │
│ [████████████████--------------------------------] │
│                                                    │
│ Raw remaining / limit: show only if meaningful    │
└────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────┐
│ Z.AI / GLM                                45s ago │
│ Token usage · 5 hour                              │
│ 32% remaining                                     │
│ [██████------------------------------------------] │
│                                                    │
│ MCP usage · 1 month                               │
│ 88% remaining                                     │
│ [█████████████████-------------------------------] │
└────────────────────────────────────────────────────┘
```

Do not claim a reset time when the provider API does not expose one reliably.

## 10. QuotaBar component

`QuotaBar` is the only explicitly approved custom visualization in S1.

Purpose:

- render a horizontal remaining-quota bar;
- allow per-instance threshold color;
- keep quota appearance identical across providers.

Conceptual API:

```go
type QuotaBar struct {
    Remaining float64 // 0..1
}
```

It may be implemented as a small custom widget/renderer or a reusable composition of canvas primitives.

### Fill semantics

Fill length = remaining fraction.

Numeric label is rendered outside or alongside the bar as normal accessible text:

```text
31% remaining
```

Do not put critical meaning only inside colored pixels.

### Color thresholds

```text
remaining <= 10%  -> red
remaining <= 20%  -> orange
remaining <= 50%  -> yellow
remaining >  50%  -> green
```

The unfilled track must use a theme-compatible neutral/background color.

Test light and dark Fyne themes.

Do not globally change the application's primary theme color to recolor one quota bar.

## 11. Standard reusable UI components

Keep these small and reusable:

### SourceCard

Backed by `widget.Card`.

Responsibilities:

- source title;
- status/freshness header area;
- provided body content;
- optional concise stale/error footer.

It must not contain source-specific API logic.

### MetricRow

```text
Label                         Value
```

Use standard labels/containers. Right-align values where practical.

### QuotaRow

```text
Window name
NN% remaining
QuotaBar
optional raw/count secondary text
```

### EmptyState

Simple label + `Open Settings` button.

Do not create a broad custom component framework.

## 12. Fyne component mapping

| UX concept | Required/preferred Fyne primitive |
|---|---|
| Monitor tab navigation | `container.AppTabs` |
| Source card | `widget.Card` |
| Scrollable tab content | `container.Scroll` / `container.NewVScroll` |
| Static text/value | `widget.Label` |
| Repeated model list if needed | `widget.List` or a small `VBox` for very small counts |
| Form | `widget.Form` |
| URL/text input | `widget.Entry` with validation |
| Secret input | `widget.NewPasswordEntry()` |
| Enumerated polling interval | `widget.Select` |
| Enable/disable | `widget.Check` |
| Buttons | `widget.Button` |
| Initial loading | `widget.ProgressBarInfinite` where useful |
| Quota remaining | custom reusable `QuotaBar` |
| Browser navigation | `widget.Hyperlink` or button calling Fyne URL open support |

Avoid `widget.Table` unless a real matrix/table appears later. It is not needed for S1.

## 13. Settings window

Settings is a normal decorated window and can be larger than the monitor.

Suggested structure:

```text
Settings

Local
────────────────────────────────────────────────────
amdgputop-web
[✓] Enabled
URL             [ http://127.0.0.1:8080          ]
GPU             [ Radeon RX 7800 XT             ▾ ]
Refresh         [ 5s                            ▾ ]
                [ Test connection ]

llama-swap
[✓] Enabled
URL             [ http://127.0.0.1:8081          ]
API key         [ •••••••••••••••••              ]
Refresh         [ 5s                            ▾ ]
                [ Test connection ]

Remote
────────────────────────────────────────────────────
MiniMax
[✓] Enabled
API key         [ •••••••••••••••••              ]
Refresh         [ 1m                            ▾ ]
                [ Test connection ]

Z.AI / GLM
[✓] Enabled
API key         [ •••••••••••••••••              ]
Refresh         [ 1m                            ▾ ]
                [ Test connection ]

                                   [ Cancel ] [ Save ]
```

The exact default local URLs are not normative; use sensible defaults only if the implementation already knows them. Users must be able to edit them.

### Settings behavior

- `Enabled` is a `widget.Check`.
- URL uses `widget.Entry` with URL validation.
- GPU selection is `widget.Select` populated from `GET /api/gpus` after a successful test/discovery.
- Poll interval is a `widget.Select` from the fixed choices in the decision record (now `docs/DECISION_LOG.md`).
- API key is `widget.NewPasswordEntry()`.
- Existing API keys should not be read back and displayed in plaintext. Showing a masked placeholder/state such as `Credential stored` is preferable to unnecessarily retrieving a secret into the widget.
- Saving a changed secret writes it to keychain.
- Clearing/removing a secret requires an explicit UI action or clearly empty-and-save semantics; do not accidentally delete an existing key because a masked field was left untouched.
- Save must be disabled or rejected while required enabled-source fields are invalid.
- `Test connection` is asynchronous and never implicitly saves the form.
- Connection test status appears inline with that source; avoid modal error spam.
- Closing Settings discards unsaved form edits unless Save was used.

### Credential backend status

If keychain is locked/unavailable/unsupported, authenticated settings should show a clear inline status and keep the application functional.

Example:

```text
System keychain: locked
Unlock the desktop keychain before saving credentials.
```

There is no plaintext fallback button.

## 14. Manual refresh feedback

A refresh button press should give lightweight feedback, for example temporarily disabling/spinning the toolbar refresh action or updating source headers to indicate an in-flight refresh.

Do not blank existing data during refresh.

## 15. Keyboard behavior

Monitor:

- `Esc`: hide monitor window;
- normal Fyne Tab/Shift-Tab focus traversal for interactive controls;
- do not invent Vim-like or custom single-letter navigation.

Settings:

- normal Fyne form/tab behavior;
- `Esc` may close/hide Settings only if it does not discard unsaved edits silently. If unsaved-edit tracking is not implemented, leave normal window-close behavior and do not overload Esc.

## 16. Explicit UI prohibitions

S1 must not introduce:

- sidebar navigation;
- custom navigation controls;
- circular gauges;
- charts/sparklines;
- hover-only required information;
- free-text enum fields;
- source-specific visual languages for equivalent quota windows;
- global warning color merely because GPU utilization is high;
- modal dialogs for routine polling failures;
- a persistent dashboard window;
- write/admin controls.
