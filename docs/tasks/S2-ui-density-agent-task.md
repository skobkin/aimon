# S2 Agent Task — Compact Overview + Quota Reset Countdown

## Objective

Implement the approved v0.2 UI-density pass on top of the existing working v0.1
AI Monitor codebase.

The current application is already usable. Make focused incremental changes;
do not re-architect unrelated working subsystems.

Read, in this order:

1. `AGENTS.md`
2. `docs/DECISION_LOG.md`
3. `docs/S2_UI_UX.md`
4. `docs/S2_IMPLEMENTATION_SPEC.md`
5. current code/tests in the touched packages
6. historical S1 docs only for background where they do not conflict

Use `docs/mockups/overview-s2-concept.png` as a visual reference. The textual S2
spec is authoritative when the generated mockup is imperfect.

## Required work

1. Fix MiniMax reset normalization:
   - `remains_time` = ms until 5-hour reset;
   - `weekly_remains_time` = ms until weekly reset;
   - normalize to absolute `ResetAt` from one captured observation time;
   - stop using `end_time` as the primary 5-hour reset source;
   - preserve legacy explicit reset fallback where already verified.
2. Add compact relative reset formatting and show it inline in Remote quota row
   headers (`5-hour · 2h14m`), without adding vertical rows.
3. Split Overview presentation from detail presentation in the UI/view-model
   layer.
4. Redesign Overview into two cards per section for the normal four-source case:
   - amdgputop-web: labeled GPU + VRAM usage bars with translucent text pill,
     plus temperature;
   - llama-swap: compact running-model summary;
   - MiniMax: two used-oriented donut gauges;
   - Z.AI / GLM: two used-oriented donut gauges.
5. Donut color remains remaining-quota threshold color; donut arc/percentage is
   used quota.
6. Show short labels (`5h`, `week`) and reset countdown below each donut. Do not
   show counts/credits in Overview.
7. Keep detail Local/Remote layout and Web UI buttons otherwise unchanged.
8. Rename tray submenu `Open` → `Web UIs` without changing its behavior.
9. Bump app metadata to v0.2.0 consistently.
10. Update/add tests and run the full validation gate.

## Important implementation guidance

- Prefer a new Overview-specific VM/presentation layer rather than forcing the
  existing detail `CardVM` to represent both layouts.
- Presentation kinds are UI concepts (`metrics`, `runtime`, `quota`), not source
  or provider abstractions.
- Keep adapter DTOs/domain snapshots free of Fyne/UI concepts.
- Do not add network calls for countdown updates. Existing visible-window ticker
  is enough to recompute relative text.
- Keep current direct `fyne.io/systray` implementation; it is an intentional
  workaround for a Wayland/Plasma problem.
- Keep current used-oriented quota semantics.
- Do not add history charts or dynamic tray metrics in this task.
- Do not perform unrelated cleanup while touching these files.

## Commit discipline

Make significant changes as separate coherent commits. A sensible sequence is:

1. `feat(minimax): normalize quota reset countdowns`
2. `feat(ui): show quota reset countdowns inline`
3. `feat(ui): add compact overview presentations`
4. `feat(ui): render two-column overview`
5. `chore(ui): rename Web UIs menu and bump version`
6. `test/chore: hardening and final verification` only if genuinely needed

Each commit should keep existing packages buildable/testable where practical.
Do not make a huge final commit containing unrelated fixes.

## Verification

Required before reporting completion:

```sh
gofmt -w .
go test ./...
go vet ./...
go build ./...
```

Then manually inspect the Overview at the normal monitor size in dark and light
themes and verify the acceptance scenarios in `docs/S2_IMPLEMENTATION_SPEC.md`.

If a generated visual detail cannot be reproduced cleanly in Fyne, preserve the
information hierarchy and density, explain the concrete limitation, and choose
the smallest deviation. Do not silently redesign the screen.
