# S7 UI/UX — Usage Pace

Pace is a quiet addition to the existing quota grammar: one caption line and,
where a projection exists, one thin bar. No new tabs, dialogs, or colors.

## Remote detail quota row

Existing row grammar is unchanged; the pace caption sits between the bar and
the optional raw-counts line, and the projection bar sits directly under the
caption.

```text
5-hour · 2h14m            17% used
[███████░░░░░░░░░░░░░░░░░░░░░░░]      <- QuotaBar (unchanged)
Pace 3.4%/h · ≈68% at reset
[█████████████░░░░░░░░░░░]            <- ProjectionBar
2051 / 12.0k credits                  <- Secondary (unchanged)
```

Caption wording by state:

| State | Caption |
|---|---|
| Active | `Pace 3.4%/h · ≈68% at reset` (projection part only when projectable) |
| Active, counts | `Pace 12.4k credits/h · ≈68% at reset` |
| Idle | `Pace 0%/h · ≈17% at reset` |
| Collecting | `Pace collecting…` |
| Unavailable | `Pace —` |
| Expired (now ≥ ResetAt) | projection part and bar hidden until a fresh sample |

OpenRouter detail card: one `Pace` money row after the usage rows —
`Pace  $1.24/h` — rendered with the same sensitive-value masking as the other
monetary rows. No bar, no Overview line.

## Overview

Under each projectable donut, one compact projection bar plus a centered
caption. No speed text in Overview.

```text
   ( 62% )        ( 17% )
   5h · 2h14m     week · 3d2h
  [██████░░░░]   [████░░░░░░]
  ≈68% at reset  ≈74% at reset
```

Windows without a usable projection (no ResetAt, not Active/Idle) show
nothing extra — the column keeps its current height content. Non-pace
sources render exactly as before.

## ProjectionBar

- Constant widget width; the *sections inside* rescale. Rendered scale =
  `max(projected, 1.0)` capped at 2.0.
- Segments: used-colored fill from 0 to the 100% point (existing quota
  threshold colors, remaining-based), red overflow beyond it, a 1 px divider
  marking the 100% point.
- The overflow segment never draws beyond the widget width; >200% saturates
  the rendered overflow while the caption keeps the true value.
- The numeric percentage always lives in the caption text; color is never
  the only signal.

## Color and text rules

Existing rules apply unchanged: threshold colors remain remaining-based, text
is always present, and pace state is never communicated by color alone. The
pace caption is plain foreground text; no status color is assigned to pace.

## Settings

Pace-capable kinds (MiniMax, Z.AI, Codex, OpenRouter) gain one form item
after "Refresh every":

```text
Pace window   [20m ▾]    10m / 20m / 30m / 60m
```

Unselected falls back to the 20m default. Other kinds show no pace control.

## Expiry behavior

The reset countdown keeps ticking per second as today. When the countdown
reaches the window reset, the projection caption part and bar hide in place
(no recompute, no rebuild) and stay hidden until a fresh successful poll
brings a new sample; the quota row itself keeps its last-good display and
error handling.

## Explicitly out of scope

No charts or sparklines, no pace in the tray menu, no pace for local
sources, no history browsing, no configurable thresholds, no notifications
on projected overage.
