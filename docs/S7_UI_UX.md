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

When the current pace would exhaust the window before its reset, the
title's countdown gains the burn-out estimate, rendered in the theme's
error color: `5-hour · ≈1h35m / 2h14m` — an estimated `1h35m` of quota
left against `2h14m` until the window resets. Without an exhausting pace
the title renders exactly as above.

Caption wording by state:

| State | Caption |
|---|---|
| Active | `Pace 3.4%/h · ≈68% at reset` (projection part only when projectable) |
| Active, counts | `Pace 12.4k credits/h · ≈68% at reset` |
| Active, over 100% | `Pace 20%/h · ≈146% at reset` (projection part in the error color) |
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

When a window's pace would exhaust it before the reset, its countdown
line gains the error-colored burn-out estimate: `5h · ≈1h35m / 2h14m`.

Windows without a usable projection (no ResetAt, not Active/Idle) show
nothing extra — the column simply ends shorter. Columns are top-aligned,
so every ring sits level with its neighbor regardless of which optional
lines (countdown, bar, caption) a window shows (DECISION_LOG 2026-10-29).
Non-pace sources render exactly as before.

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
pace caption is otherwise plain foreground text; the only status color on
pace is the theme error color, reserved for genuine overage — the
projection caption past 100% and the burn-out estimate in the countdown
lines (DECISION_LOG 2026-11-03). The `≈` marker keeps the estimate legible
without color.

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
brings a new sample; the burn-out estimate drops together with them — the
countdown line falls back to the plain reset countdown (`5-hour · now`).
The quota row itself keeps its last-good display and error handling.

## Explicitly out of scope

No charts or sparklines, no pace in the tray menu, no pace for local
sources, no history browsing, no configurable thresholds, no notifications
on projected overage.
