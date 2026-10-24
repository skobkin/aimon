# Task — S7 / v0.7.0: Usage pace and end-of-window projection

Implement derived pace metrics for the quota sources: an in-memory sample
history per instance/quota-window, a usage-speed estimate ("Pace 3.4%/h",
"12.4k credits/h", OpenRouter "$1.24/h") and an end-of-reset projection
("≈68% at reset") with a projection bar that can exceed 100%.

## Required deliverables

1. Pure `internal/pace` package: sample ring per series, discontinuity
   handling, read-time estimation, per-instance window, reset semantics —
   per `docs/S7_IMPLEMENTATION_SPEC.md` §1–§3.
2. App wiring: tracker fed from the existing manager subscription (failures
   and non-pace kinds skipped), `applyInstances` reset/keep matrix,
   `PaceStates()` on `MonitorController`.
3. Config: per-instance pace window {10m, 20m, 30m, 60m}, default 20m,
   validation + load-time snapping, additive `pace_window` record field,
   schema stays 1.
4. View models: `PaceVM`, per-hour formatters, attach points on quota
   window / overview donut / spend VMs, `paces` threaded through builders.
5. `ProjectionBar` widget (section-rescaling, ≤2.0 render scale, red
   overflow, 100% divider) + row/overview/spend call sites.
6. Projection expiry at `ResetAt` handled by the 1-second ticker in place.
7. Settings: "Pace window" select for pace-capable kinds only.
8. Version 0.7.0 / Build 7 (buildinfo).
9. Thorough pace/VM/widget/wiring/config tests per S7_IMPLEMENTATION_SPEC
   §9.

## Hard acceptance conditions

- manager and adapters unchanged; no new network requests, retries, or
  polling-cadence changes;
- samples in memory only — nothing persisted, logged, or committed;
- failed refreshes never record samples; a failed source cannot affect
  others' pace;
- brand-new flat series reads Collecting, not Idle; established flat series
  drops speed to 0 immediately;
- usage decrease (count-authoritative) and limit change are the only
  discontinuities; ResetAt jitter never discards history;
- projection constant between samples, hidden once now ≥ ResetAt, restored
  by the next fresh sample; quota display and error handling untouched;
- projection anchor uses counts when valid; ordinary quota display
  unchanged;
- OpenRouter shows $/h only, never a projection, respecting the privacy
  preference;
- pace-window change applies immediately without adapter rebuild or history
  loss;
- local sources (amdgputop-web, llama-swap, Hindsight) show no pace UI;
- existing quota behavior does not regress when pace data is absent.

## Workflow

1. Inspect current HEAD (branch from fresh `origin/master`).
2. Read S7 docs and the decision log.
3. Produce a concrete implementation plan and wait for approval.
4. Implement only after approval in focused commits.
5. Run:

```sh
gofmt -w .
go test ./...
go vet ./...
go build ./...
```

6. Report commits, engine design notes, test coverage, verification
   (including a manual smoke run: Collecting → Active → Idle → expiry →
   restore), deviations and uncertainties.

Do not commit real account data, tokens, personal paths, or raw provider
payloads.
