# S2 Implementation Specification — UI Density and Quota Reset

## 1. Baseline

Implement this task **incrementally on top of the current v0.1 repository**.
The current implementation is known-good and usable. Preserve unrelated
behavior and architecture.

Do not reimplement S1. Do not replace working source manager, settings,
credentials, tray workaround, or detail tabs merely because the historical S1
spec suggests a different implementation.

The approved visual concept is `docs/mockups/overview-s2-concept.png`; exact
behavior is defined by `docs/S2_UI_UX.md`.

Target release after this work: **0.2.0**.

## 2. Required functional changes

### F1 — MiniMax reset timestamps

Normalize MiniMax reset durations into `domain.QuotaWindow.ResetAt`:

- `remains_time` → 5-hour `ResetAt`;
- `weekly_remains_time` → weekly `ResetAt`.

The decoded values are milliseconds from observation time. Use one captured
`observedAt` for the refresh/build operation.

Duration fields take precedence. Existing explicitly named legacy reset fields
may be fallbacks. Stop treating `end_time` as the primary interval reset.

Invalid reset data must not invalidate otherwise valid quota usage data.

### F2 — Relative reset formatter

Add a pure/testable formatter for reset countdowns with the compact forms in
`docs/S2_UI_UX.md`.

The formatter takes absolute `ResetAt` and a supplied `now`; do not call
`time.Now()` inside the pure formatter.

### F3 — Remote detail reset text

Extend the existing quota row VM/component so the left side of the header can
render:

`5-hour · 2h14m`

while the right side remains:

`17% used`

No new vertical reset line.

### F4 — Overview-specific presentation VMs

Separate Overview presentation from detail card bodies.

A reasonable design is an Overview-specific VM with a discriminated
presentation kind:

```go
type OverviewKind string

const (
    OverviewMetrics OverviewKind = "metrics"
    OverviewRuntime OverviewKind = "runtime"
    OverviewQuota   OverviewKind = "quota"
)
```

The exact names are not mandatory. The architectural requirement is:

- Overview has presentation-specific compact data;
- Local/Remote detail can keep the current `CardVM` path;
- adapters/domain remain provider-data oriented, not presentation oriented;
- no Fyne objects in view models.

### F5 — MetricsOverview

Expose enough pure VM data to render:

- GPU usage fraction + `GPU NN%` text + metric level;
- VRAM used fraction + `VRAM X / Y GiB` text + metric level, only when a
  trustworthy total exists;
- temperature text + current metric level.

Add a reusable Overview-only labeled metric usage bar. It is an approved custom
presentation component for S2.

### F6 — RuntimeOverview

Expose:

- running-model count summary with correct singular/plural wording;
- first two model names;
- optional `+N more`.

Do not change llama-swap adapter data requirements.

### F7 — QuotaOverview

Expose at most two headline windows with:

- short label (`5h`, `week`, fallback `month`);
- used fraction;
- remaining fraction for threshold color;
- percentage text without `used` (`14%`);
- compact reset countdown.

Do not expose counts/credits in Overview.

Add a reusable Overview-only donut/ring gauge. It is an approved custom
presentation component for S2. Arc length is used fraction; color comes from
existing remaining-based quota thresholds.

### F8 — 2×2 Overview layout

Render Local and Remote sections with up to two cards per row. For the current
four-source setup this is visually:

```text
Local
[ metrics ] [ runtime ]

Remote
[ quota   ] [ quota   ]
```

One enabled card in a section should not be artificially constrained to a
half-width orphan unless Fyne layout constraints make the alternative
substantially more complex; prefer full width for one card.

Overview cards must remain usable for Loading, Unavailable and Stale states.
Stale cards retain last-good data. Verbose refresh error footer is detail-only
in the new Overview.

### F9 — Tray wording

Rename the tray submenu `Open` to `Web UIs` only. Do not change its entries,
URLs, goroutine model, or direct systray workaround.

### F10 — Version metadata

Bump both `FyneApp.toml` and runtime `appMetadata()` to version `0.2.0` and
increment build consistently (expected build `2` unless repository state has
already advanced). Keep the metadata mirroring test green.

## 3. Suggested code touchpoints

The current repository suggests the following minimal touchpoints. Treat this
as guidance, not a demand to rename working packages:

- `internal/source/minimax/minimax.go`
  - reset-duration semantics and deterministic observation time;
- `internal/source/minimax/minimax_test.go`
  - replace tests asserting countdowns are ignored;
- `internal/ui/viewmodel.go`
  - reset text and Overview-specific VMs/builders;
- `internal/ui/viewmodel_test.go`
  - formatters and Overview model derivation;
- `internal/ui/components/quota_bar.go`
  - keep detail bar; update quota row header to include reset text;
- `internal/ui/components/`
  - add narrowly scoped metric-bar and donut components plus pure/render tests
    where practical;
- `internal/ui/monitor/monitor.go`
  - 2-column Overview rendering only; keep Local/Remote detail path stable;
- `internal/app/app.go`
  - tray label and metadata version only;
- `FyneApp.toml`
  - version/build mirror.

Avoid broad churn in config, credentials, source manager, settings, links, or
adapters other than MiniMax reset normalization.

## 4. Custom component constraints

S2 allows three narrowly scoped custom visualizations total:

1. existing `QuotaBar` for Remote detail;
2. Overview labeled metric usage bar;
3. Overview quota donut/ring.

This is not permission to create a custom component framework.

### Metric usage bar

- fill fraction clamped to `[0,1]`;
- fill color uses existing metric-level colors;
- neutral track;
- combined label/value displayed on a neutral semi-transparent pill over the
  bar;
- pill stays within bar bounds;
- readable light/dark theme;
- no split-color clipping renderer in this iteration unless required to fix a
  demonstrated readability defect.

### Quota donut

- track is neutral;
- arc/fill is used fraction;
- arc color is `ThresholdColor(remaining)` or the same threshold function
  factored into a shared color helper;
- center contains percentage and short window label, or those labels are
  overlaid using standard Fyne objects if that materially simplifies the
  renderer;
- reset countdown is outside/below the ring;
- no third-party chart dependency merely to draw a two-value ring.

## 5. Countdown formatting contract

Use deterministic tests for at least:

| Delta | Expected |
| --- | --- |
| `42s` | `42s` |
| `59m` | `59m` |
| `2h14m` | `2h14m` |
| `4d7h` | `4d7h` |
| zero/past | `now` |
| nil ResetAt | empty |

Keep output compact. Do not add spaces inside compound durations (`2h 14m` is
not the target style).

## 6. Test requirements

At minimum add/update tests for:

### MiniMax

- `remains_time` becomes interval `ResetAt = observedAt + duration`;
- `weekly_remains_time` becomes weekly `ResetAt`;
- both windows use the same captured observation time;
- duration fields override an unrelated `end_time`;
- absent duration allows existing verified legacy reset fallback;
- malformed/negative reset duration does not drop valid quota usage;
- existing remaining/used/count semantics stay unchanged.

### View models / formatters

- reset countdown formatting boundaries;
- Remote quota row VM combines window name + reset text without changing used
  percentage semantics;
- Overview metrics derive correct fractions/text/levels;
- Runtime summary singular/plural and `+N more` behavior;
- quota short-label mapping and preferred 5h/weekly ordering;
- Overview quota percentage is `NN%`, not `NN% used`;
- Overview omits raw counts;
- disabled sources omitted; unavailable/stale behavior retained.

### Custom visual components

Pure logic should be testable without a display where possible:

- metric bar fraction clamp and color mapping;
- quota donut fraction clamp and threshold color boundaries;
- any short-label/geometry helpers that do not require a canvas.

Do not introduce fragile screenshot tests for exact Fyne pixels.

### Existing regression suite

All existing tests must remain green unless an old test directly asserts a
superseded decision (notably MiniMax ignoring reset countdowns). Update those
tests to the new decision instead of weakening coverage.

## 7. Manual acceptance scenarios

Before finishing, run the app and verify:

1. four enabled/healthy sources produce the 2×2 Overview from the concept;
2. GPU and VRAM bar pills are readable and do not exceed bar bounds;
3. donut center text and reset times are readable at the existing 540-DIP
   monitor width;
4. dark theme and light theme both remain legible;
5. Remote detail shows `5-hour · <countdown>` on the existing header line and
   no extra reset row;
6. raw Z.AI credits remain visible in Remote detail but not Overview;
7. stale data remains visible and its header says `... · stale`;
8. one unavailable source does not affect other Overview cards;
9. one enabled source in a section produces a sensible single-card layout;
10. tray submenu reads `Web UIs` and opens the same destinations as before;
11. Local/Remote Web UI buttons and all Settings behavior remain unchanged;
12. monitor ticker updates reset countdown locally without increasing provider
    polling cadence.

## 8. Explicit non-goals

Do not implement:

- history charts;
- GPU/VRAM ring buffers;
- dynamic tray metrics;
- provider additions;
- detail-tab redesign unrelated to reset text;
- moving Web UI actions into card headers;
- generic theming/custom-widget infrastructure;
- API polling cadence changes;
- a new persistence format.

## 9. Completion gate

Run:

```sh
gofmt -w .
go test ./...
go vet ./...
```

Also build the application and perform the manual UI checks above.

Do not finish with tests skipped because they were inconvenient to adapt to the
new reset semantics.
