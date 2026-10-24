# S7 Agent Rules — Usage Pace

## 1. Pace is derived, never polled

Pace and projection come from samples the monitor already collected. No
source gains a request, a retry, or a shorter interval because of S7. The
next scheduled poll remains the only retry mechanism.

## 2. In-memory only

Sample history lives in RAM (`internal/pace`), bounded by time and by a fixed
ring capacity. It is never written to disk, preferences, logs, or test
fixtures; it dies with the process. There are no charts, sparklines, or
history visualizations — pace is a numeric label and a progress bar, both
current-value representations (DECISION_LOG 2026-10-24).

## 3. Series identity and lifetime

One series per instance ID and quota window ID; OpenRouter spend is one
series per instance. Adapter rebuild (material settings change) or instance
removal resets the instance's history. Interval-only and pace-window-only
reconfiguration keep it. App restart starts from nothing.

## 4. Sample integrity

- Sample times come from the snapshot's own timestamp, never receive time.
- Failed refreshes record nothing: re-reading the retained last-good
  snapshot would fabricate a flat interval.
- A sample whose timestamp is not newer than the newest stored one is
  ignored.
- Deltas are computed over sample timestamps; sample counts never divide.

## 5. Discontinuities

A usage decrease (count-delta authoritative when counts exist) or a limit
change resets the series; the next read is Unavailable, not Collecting.
ResetAt movement alone never resets a series — MiniMax re-derives its reset
stamp each poll and Codex rolling deadlines move; dropping history on those
jitters would leave the feature permanently Collecting.

## 6. Idle must be immediate

Once a series has adequate in-window coverage (span ≥ MinSpan), a flat
newest pair forces speed to zero on the very next read. A finished burst
must not keep projecting an overage. The same gate keeps a brand-new flat
series in Collecting so "nothing has happened yet" is never reported as
"idle at zero speed".

## 7. Projection discipline

- Projection requires a provider-supplied absolute `ResetAt`. Synthesizing
  one from the local clock remains forbidden (S4 §5 extended).
- The projected value is computed once per rebuild and is constant between
  samples. No per-second recomputation, no UI rebuild for it.
- The projection expires at `ResetAt`: the 1-second ticker hides it in place
  until a fresh valid sample arrives. Last-good quota display and error
  handling are untouched by expiry.
- The projection anchor uses `UsedCount/Limit` when counts are valid; the
  provider's rounded percentage is only a fallback. The ordinary quota
  display is unchanged.
- Projected values are unclamped in text; only the rendered bar segments are
  bounded.

## 8. Precision rules

- Count-bearing series report speed in counts per hour and derive the
  fraction speed from counts (providers quantize percentages).
- Fraction-only series report %/h with a small comparison epsilon; the
  epsilon is a named, tested constant.

## 9. Capability gating

Pace appears only for MiniMax, Z.AI, Codex (speed + projection) and
OpenRouter (spend speed, no projection). Hindsight, llama-swap and
amdgputop-web never show pace UI, and the tracker never records their
snapshots.

## 10. OpenRouter money privacy

The $/h pace row is a monetary value: it renders through the same
sensitive-value handling as the other spend rows and is masked under the
global "hide sensitive info" preference.

## 11. Threading

The tracker takes a mutex on every method; recording happens on source loop
goroutines, reads on the UI thread, resets on the settings-save goroutine.
No Fyne API is touched from the tracker; UI updates go through the existing
notification → coalescer → `fyne.Do` pipeline.

## 12. No new settings beyond the pace window

One enumerated per-instance select ({10m, 20m, 30m, 60m}, default 20m) for
pace-capable kinds. No free-text intervals, no per-provider exceptions.
