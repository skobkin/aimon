# S7 Implementation Specification — Usage Pace

Target release: **v0.7.0**.

## 1. Package layout

New pure package `internal/pace` — imports only `sync`, `time`,
`internal/domain`. No Fyne, no I/O, no persistence. All estimation is a pure
function of retained samples plus the configured window, computed at read
time so a window change takes effect immediately.

```go
const (
    MinSpan      = 3 * time.Minute
    fracEpsilon  = 1e-9
    countEpsilon = 1e-9
    maxSamples   = 240
)
// retention spans 2 × the largest supported pace window, bounded by maxSamples.

type State int // StateCollecting | StateActive | StateIdle | StateUnavailable

type Sample struct {
    At        time.Time
    Used      float64
    UsedCount *float64
    Limit     *float64
    Unit      string
    ResetAt   *time.Time
}

type Estimate struct {
    State      State
    SpeedFrac  float64 // used-fraction per hour
    SpeedCount float64 // count units per hour; valid when HasCount
    HasCount   bool
    Unit       string
    LastUsed   float64 // anchor: UsedCount/Limit when valid, else Used
    LastAt     time.Time
    ResetAt    *time.Time
}

func (e Estimate) Project(at time.Time) (float64, bool)
// anchor + SpeedFrac × (ResetAt − LastAt).Hours(); ok=false when ResetAt is
// nil, ResetAt ≤ LastAt, at ≥ ResetAt (stale), or state Collecting/Unavailable.

type InstancePaces struct {
    Windows map[string]Estimate // keyed by quota window ID
    Spend   *Estimate           // OpenRouter $/h; never projectable
}

type Tracker struct{ /* mu sync.Mutex; series map[domain.InstanceID]map[string]*series; windows map[domain.InstanceID]time.Duration */ }

func NewTracker() *Tracker
func (t *Tracker) ObserveQuota(id domain.InstanceID, snapAt time.Time, windows []domain.QuotaWindow)
func (t *Tracker) ObserveSpend(id domain.InstanceID, snapAt time.Time, usageTotal float64)
func (t *Tracker) SetWindow(id domain.InstanceID, window time.Duration) // zero → default
func (t *Tracker) States() map[domain.InstanceID]InstancePaces          // detached copy
func (t *Tracker) Reset(id domain.InstanceID)
```

Recording performs discontinuity handling, retention pruning, and ring-cap
eviction; `States()` computes estimates with each instance's current window.
Every method takes the mutex.

## 2. Recording rules

Per accepted sample (timestamp newer than the newest stored):

1. Discontinuity vs the previous newest sample (hit ⇒ keep only the new
   sample, set the series `reset` flag):
   - usage decrease — count delta authoritative when both samples carry
     counts (`Δcount < −countEpsilon`), else fraction decrease beyond
     `fracEpsilon`;
   - limit changed (value or nil↔non-nil);
   - ResetAt movement alone is never a discontinuity.
2. Reads return `StateUnavailable` while the `reset` flag is set; it clears
   when the next sample is recorded.
3. Prune samples older than retention; cap at `maxSamples`.

## 3. Estimation rules (read time)

With the instance's window `w`:

1. `reset` flag → Unavailable.
2. <2 samples → Collecting.
3. Endpoint = oldest sample with `At ≥ newest.At − w`; span < MinSpan →
   Collecting (before any idle override).
4. Newest sample flat against the newest sample at least MinSpan older
   (counts exact / fraction ≤ epsilon) → Idle: speeds 0. A sample recorded
   seconds after its predecessor (manual refresh) never reads idle by
   itself.
5. `dt ≤ 0` → Unavailable. Both endpoints with counts and `LimitNewest > 0`:
   `SpeedCount = Δcount/dt.Hours()`, `SpeedFrac = Δcount/LimitNewest/dt.Hours()`,
   `HasCount = true`; otherwise `SpeedFrac = Δfrac/dt.Hours()` (tiny
   negatives clamp to 0).
6. Anchor: `UsedCountNewest/LimitNewest` when valid, else `Used`.
7. Unit: newest sample's, falling back to the last non-empty in the series.

## 4. App wiring

- `App` owns a `*pace.Tracker`. The existing manager subscriber records pace
  on the loop goroutine before scheduling the UI update:
  failure notifications and non-pace kinds are skipped; `QuotaSnapshot` /
  `CodexSnapshot` → `ObserveQuota`, `OpenRouterSnapshot` with `Key != nil` →
  `ObserveSpend(id, CollectedAt, Key.UsageTotal)`.
- `applyInstances`: adapter rebuild (settings change) or instance removal →
  `Tracker.Reset(id)`, dropped only after `Manager.Apply` has waited out the
  replaced loops (their final completion callback can still record against
  the old runtime); pace-window-only change → `SetWindow` + coalesced
  re-render, adapter untouched. Interval-only / rename / enable-disable keep
  history. A changed credential keeps the adapter but still drops the
  instance's history: the app wraps the credential store and resets the
  owning instance on a changed-value write or deletion (same-value re-saves
  keep history).
- `MonitorController` gains `PaceStates() map[domain.InstanceID]pace.InstancePaces`;
  `rebuild()` threads the (read-only) map through `BuildOverview` /
  `BuildLocalTab` / `BuildRemoteTab` into the card builders. View models stay
  pure data.

## 5. Config

- `config.PaceWindows = {10m, 20m, 30m, 60m}`, `config.DefaultPaceWindow = 20m`;
  labels reuse `IntervalLabel`.
- `SourceInstance.PaceWindow time.Duration`; `config.PaceCapable(kind)` is
  true for MiniMax, Z.AI, Codex, OpenRouter.
- Validation rejects off-list values for capable kinds; load-time
  normalization snaps zero/off-list to the nearest preset (ties toward the
  longer window), mirroring `normalizeInterval`.
- Persistence: additive optional `pace_window` (`Duration.String()`) on the
  source record; schema stays version 1. Older builds ignore the field.

## 6. View models (`internal/ui`)

```go
type PaceVM struct {
    State          pace.State
    SpeedText      string // "3.4%/h", "12.4k credits/h", "$1.24/h"; "" until Active/Idle
    CanProject     bool
    ProjectedText  string // "≈68% at reset", unclamped "≈137% at reset"
    SpeedFrac, LastUsed float64
    LastAt         time.Time
    ResetAt        *time.Time
}
func (vm PaceVM) ProjectionExpired(now time.Time) bool // now ≥ ResetAt
```

New formatters: `FormatPctPerHour`, `FormatCountPerHour(v, unit)`,
`FormatMoneyPerHour` (siblings of `FormatRatePerMin`; `FormatCount` /
`FormatMoney` reused). Attach points: `QuotaWindowVM.Pace`,
`OverviewDonutVM.Pace` (headline windows only), `SpendVM.Pace` (detail).
Builders gain a `paces map[domain.InstanceID]pace.InstancePaces` parameter
and attach per window ID; absent estimates leave `Pace` nil.

## 7. ProjectionBar (`internal/ui/components`)

`widget.BaseWidget` + canvas rectangles after `QuotaBar`. Height 6, min
width 120. Objects: track, base fill `0 → min(projected,1)/scale` in
`ThresholdColor(max(0, 1−projected))`, red overflow when projected > 1,
1 px divider at `width/scale`. `scale = min(max(projected, 1.0), 2.0)`;
the overflow spans `(min(projected, scale) − 1)/scale × width` — never past
the widget edge. `SetProjected` refresh-gates on 0.1% changes. Caption text
lives outside the bar, as in every quota visualization.

Call sites: `NewQuotaRow` (caption + bar; tick closure also performs the
expiry hide/restore), `NewQuotaOverviewBody` (compact bar + caption per
projectable donut; per-column tick), OpenRouter detail body (Pace money row
with sensitive-value masking).

## 8. Ticker discipline

The 1-second ticker keeps rewriting only time-relative text. New duty: when
`Pace.ProjectionExpired(now)` flips, hide/restore the projection caption
part and bar in place. No projection recomputation, no object rebuilding, no
full re-render.

## 9. Test matrix

- `internal/pace`: every S7_PACE.md edge row; 30-minute count-delta exactness
  (both speeds + projection); count decrease hidden by fraction quantization;
  Unavailable held exactly one read after a discontinuity; span gate before
  idle (flat sparse pair stays Collecting); ResetAt jitter and rolling
  deadlines keep history; genuine resets re-collect; `Project` expiry
  boundaries; rounded-percent/precise-count anchor; window switch
  10m→60m→20m immediate; unit fallback; retention/ring; `States()` copy
  detachment; `-race`.
- `internal/ui`: pace VM mapping, `ProjectedText` boundaries, expiry
  boundary, formatter tables, per-window attach, nil without estimates,
  spend pace.
- `internal/ui/components`: ProjectionBar scale/divider/overflow/cap/
  clamp/refresh-gate; row and overview caption text, expiry flip, nil-Pace
  regression; spend pace row masked/unmasked per privacy flag.
- `internal/app`: recordPace skip rules; applyInstances reset/keep matrix
  (provider-settings change resets; pace-window-only and interval-only keep
  and recompute immediately; removal resets).
- `internal/config`, `internal/ui/settings`: presets, validation, snapping
  (45m → 60m), persistence roundtrip, old documents load, schema stays 1,
  select presence per kind.
- Manager and adapters: unchanged (acceptance condition).

## 10. Acceptance conditions

- No new network requests anywhere; manager and adapters untouched.
- No persistence of samples; nothing about pace reaches logs or fixtures.
- One failed source cannot corrupt another's pace; failures record no
  samples.
- Pace survives interval-only and pace-window-only reconfiguration; resets
  on adapter rebuild, removal, and restart.
- ResetAt jitter never blanks a working pace.
- Projection never renders past its ResetAt; quota display is independent.
- OpenRouter pace row respects the privacy preference.
- Existing MiniMax/Z.AI/Codex/OpenRouter/OpenRouter-overview behavior does
  not regress when pace data is absent.

## 11. Non-goals

Persistence, charts/sparklines, tray telemetry, pace for local sources,
configurable thresholds, notifications, multi-window aggregation.
