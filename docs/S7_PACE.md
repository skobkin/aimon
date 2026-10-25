# S7 Usage Pace feature contract

Derived metrics, not new data: usage speed and an end-of-window projection
computed from quota measurements the monitor already polls. No new network
requests, no persistence.

## Definitions

- **Pace** — the estimated consumption speed of one quota window (or of the
  OpenRouter key spend), for example `3.4%/h`, `12.4k credits/h`, `$1.24/h`.
- **Projection** — where the used fraction lands at the window reset:
  `≈68% at reset`. May exceed 100% (`≈137% at reset`); the overflow is drawn
  red inside the projection bar and always spelled out in text.
- **Sample** — one successful poll's reading for one series (quota window or
  spend counter), stamped with the snapshot's own `CollectedAt`. Sample times
  always come from snapshots, never from receive time.

## Scope

| Source | Pace | Projection | Series input |
|---|---|---|---|
| MiniMax | yes | yes (per window) | quota windows (counts preferred) |
| Z.AI | yes | yes (per window) | quota windows (counts preferred) |
| ChatGPT Codex | yes | yes (per window) | quota windows (fraction only) |
| OpenRouter | yes ($/h) | never | `Key.UsageTotal` (cumulative spend) |
| Hindsight, llama-swap, amdgputop-web | no | no | — |

Projection requires a provider-supplied absolute reset time
(`QuotaWindow.ResetAt`). OpenRouter has none by S4 policy and is never given
one; local sources are out of scope entirely.

## Estimation window

Per-source trailing window, Settings-selectable from {10m, 20m, 30m, 60m},
default **20m**. The rate is the delta between the newest sample and the
oldest sample inside the window, divided by their timestamp distance — gaps
(failed polls, suspend, disable) contribute their real elapsed time, never a
per-sample average. An estimate (including Idle) requires the in-window
endpoint span to reach **MinSpan = 3 minutes**; below that the state is
Collecting.

Changing the estimation window recomputes immediately from retained samples —
no poll wait, no adapter rebuild, no history loss. Retention spans the
largest supported window (2×60m), bounded by a 240-sample ring.

## State model

- **Collecting** — fewer than two samples, or in-window span below MinSpan.
  Even a flat pair reads Collecting until coverage is adequate.
- **Active** — positive measured consumption.
- **Idle** — the newest sample pair shows no consumption while coverage is
  adequate: speed drops to 0 immediately (a finished burst must not keep
  projecting a fake overage). Counts compare exactly; fractions compare with
  a small epsilon because several providers quantize to integer percent.
- **Unavailable** — the first read after a discontinuity (see below).

## Discontinuity rules

Checked per accepted sample against the previous newest sample of the series.
A hit drops older samples and keeps only the new one; reads return
Unavailable until the next sample arrives (S5 rule: "rate unavailable for
that interval").

- **Usage decreased** — genuine window rollover or provider correction. When
  both samples carry counts, the count delta is authoritative and is checked
  even when the quantized fraction is unchanged; never a negative burn rate.
- **Limit changed** (value or nil↔non-nil) — series reset.
- **Credential changed** — a swapped or removed keychain secret re-points the
  running adapter at a different upstream counter (spend pace is per-key
  usage; quota series are per account), so the instance's history drops even
  though its provider settings — and therefore its adapter — are unchanged.
  Only the `api-key` slot qualifies: every pace feed is measured with that
  credential, so rotating OpenRouter's Management key (account credit
  balance, which pace never reads) keeps the history. Re-saving the same
  secret keeps the history. Detected at the credential store, at save time
  (Decision Log, 2026-10-25).
- **`ResetAt` movement alone is never a discontinuity.** MiniMax derives the
  reset stamp from a relative duration and re-derives it every poll; Codex
  rolling-window deadlines move between requests. History is kept and the
  latest valid `ResetAt` is used for projection.

## Projection

```
projected = anchor + speedPerHour × (ResetAt − LastAt).Hours()
anchor    = UsedCount/Limit when counts are valid and Limit > 0,
            else the reported used fraction
```

Unclamped (may exceed 1.0). Constant between samples — recomputed only when a
new sample or a settings change triggers a rebuild, never per tick.

Validity is time-bound: once the current time reaches `ResetAt` the
projection is stale and the UI hides the projected percentage and bar until a
fresh successful poll provides a new projection. The last-good quota display
and error handling are unaffected.

## Edge cases

| Situation | Behavior |
|---|---|
| First sample of a series | Collecting |
| In-window span < MinSpan (incl. flat pair) | Collecting |
| Newest sample flat against the newest sample ≥ MinSpan older | Idle, speed 0 |
| Burst then flat | Idle within MinSpan of the last increase |
| Sample seconds after its predecessor (manual refresh) | Never idle by itself; measured normally |
| Count decrease, fraction unchanged | Discontinuity, Unavailable |
| Fraction decrease (no counts) | Discontinuity, Unavailable |
| Limit changed | Series reset, Collecting |
| Credential changed / removed | History dropped, Collecting from the new credential |
| ResetAt jitter / rolling deadline moves | History kept, latest ResetAt used |
| Gap (failures, suspend, disable) | True average over endpoint timestamps |
| Duplicate/stale notification | Not recorded (timestamp guard) |
| Elapsed ≤ 0 between endpoints | Unavailable |
| Both endpoints carry counts | unit/h + count-derived fraction speed |
| Rounded percent, precise counts | Anchor from counts |
| now ≥ ResetAt | Projection hidden until a fresh sample |
| Projected > 100% | Text unclamped; rendered overflow capped |

## Known limitations

- Fraction-only series (Codex; MiniMax/Z.AI without counts) quantize to
  integer percent, so sub-1%-per-poll consumption mostly reads as Idle.
  Counts-bearing series are immune.
- A pace window shorter than 2–3 poll intervals yields Collecting between
  samples; use a window ≥ 3× the poll interval.
