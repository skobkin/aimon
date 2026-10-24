# AGENTS.md

## Project intent

This project is a small, tray-first desktop monitor for:

- local AMD GPU telemetry from `amdgputop-web`;
- local model/runtime state from `llama-swap`;
- MiniMax Token Plan quota;
- Z.AI / GLM Coding Plan quota.

The product remains a **read-only monitor** in the current S2 task, not an administration console.

## Authoritative documents

Read these before implementation. The repository contains historical S1 specs as well as the current S2 / v0.2 UI-density task.

For the current S2 task, precedence is:

1. `docs/DECISION_LOG.md` — dated implementation-time decisions; newest applicable entry wins.
2. `docs/S2_UI_UX.md` — current UI contract for the S2 changes.
3. `docs/S2_IMPLEMENTATION_SPEC.md` — current implementation/acceptance contract.
4. `docs/tasks/S2-ui-density-agent-task.md` — execution plan and scope.
5. `docs/S1_IMPLEMENTATION_SPEC.md`
6. `docs/ARCHITECTURE.md`
7. `docs/UI_UX.md`
8. `docs/INTEGRATIONS.md`
9. `docs/tasks/S1-agent-task.md`

S1 documents remain useful architectural history, but S2 documents and later Decision Log entries explicitly supersede them where they conflict. Do not rewrite old specs retroactively merely to remove historical differences. Do not redesign the product or UI while implementing the current task.

## Decision log

Implementation-time changes that deviate from or extend the authoritative
documents — user-directed or forced by real-world behavior — must be
recorded with a dated entry in `docs/DECISION_LOG.md` in the same commit
(or ask the user whether to log it). Never let the code and the documents
diverge silently.

## Non-negotiable constraints

- Use Go and Fyne.
- Target Fyne 2.8.x APIs; the application is primarily a Linux desktop application.
- Runtime UI is tray-first: a compact borderless monitor window plus a separate normal Settings window.
- The current task remains read-only. Do not add model unload/load, process killing, quota management, log viewing, or other control operations.
- All data sources are optional and independent.
- One failed or disabled source must not break other sources or the application.
- Never discard the last successful snapshot just because a later refresh failed.
- Disabled sources are omitted from Overview. Enabled but unavailable sources remain visible and report their state.
- Do not store API keys/tokens/passwords in Fyne Preferences, config files, source code, logs, test fixtures, or documentation.
- Secrets must use the system keychain through `github.com/zalando/go-keyring`, following the same general store/status pattern as `skobkin/qBtRemoteGo`.
- There is no plaintext secret fallback.
- Provider/source-specific DTOs must stop at the adapter boundary. UI code consumes normalized domain snapshots/view models only.
- Network I/O must never run synchronously in a Fyne event callback.
- Any Fyne API call made from an application-created goroutine must be scheduled with `fyne.Do` or `fyne.DoAndWait` as appropriate.
- Do not add historical persistence, historical charts, or dynamic tray telemetry in the current S2 task. The new Overview donut gauges are current-value visualizations, not history charts.

## UI implementation rules

For the current task, treat `docs/S2_UI_UX.md` as the UI contract; use historical `docs/UI_UX.md` only where S2 does not supersede it.

- Use `container.AppTabs` for the monitor's Overview / Local / Remote navigation.
- Detail Local/Remote source cards continue to use the established standard Fyne card/container grammar. Overview is now allowed to use presentation-specific compact bodies while preserving the common source header/status grammar.
- Use standard Fyne controls for forms: `widget.Form`, `widget.Entry`, `widget.NewPasswordEntry()`, `widget.Select`, `widget.Check`, and normal buttons.
- Do not use free-text inputs for enumerated polling intervals.
- Do not create custom dropdowns, tabs, toggles, forms, card systems, or dialogs.
- Approved custom presentation-only visualizations are narrowly scoped: existing `QuotaBar`; an Overview-only labeled metric usage bar; and an Overview-only quota donut/ring gauge. Do not turn them into a general widget framework.
- Circular gauges are allowed only for quota usage in the Overview as specified by `docs/S2_UI_UX.md`; Remote detail keeps linear quota bars.
- Do not introduce historical charts, sparklines, history storage, or chart dependencies in this task.
- Do not use color as the only status signal; always pair status color with text/iconography.
- High GPU utilization is not an error and must not be colored as a warning merely because it is high. (User-directed exception, 2026-08-23: GPU utilization, VRAM and temperature values are colored by load with agreed thresholds — recorded in `docs/DECISION_LOG.md`.)
- Keep equivalent metrics visually consistent across providers.
- Do not invent source data. If an upstream API does not expose a value reliably, omit it or show `Unavailable` according to the UI spec.

## Background work and polling

- Each enabled source refreshes independently.
- A slow or failing source must not block refreshes for any other source.
- Do not overlap two refreshes of the same source.
- Manual refresh should trigger immediate refresh of enabled sources without creating duplicate in-flight requests.
- Use per-request timeouts.
- Do not add aggressive internal retries. The next scheduled poll is the retry mechanism.
- Respect minimum/default refresh intervals defined in the S1 spec.
- Stop pollers cleanly on application shutdown.

## Credentials

Use an injectable credential-store abstraction around `github.com/zalando/go-keyring`.

The store must distinguish at least:

- available;
- locked;
- unavailable;
- unsupported.

Use separate stable account IDs for distinct secrets, for example:

- `source/llama-swap` (only when a local API key is configured);
- `source/minimax`;
- `source/zai`.

Tests must use an in-memory/fake store. They must never touch the real desktop keychain.

## API integration and fixtures

- Use `httptest.Server` for HTTP adapter tests.
- If fixture JSON is committed, it must be sanitized and contain no real user/account identifiers, API keys, cookies, request IDs, or other private data.
- Parse undocumented/semidocumented APIs defensively.
- Keep decoding DTOs separate from normalized domain types.
- Preserve unknown/missing fields as absence, not guessed values.

## Code quality

Prefer small packages with explicit dependencies over a large global application object.

Suggested package boundaries are documented in `docs/ARCHITECTURE.md`; deviations are acceptable only when they make the code materially simpler without violating the architecture.

Before finishing:

```sh
gofmt -w .
go test ./...
go vet ./...
```

If a linter configuration is added, run it too.

## Repository hygiene

- Do not commit secrets or local runtime data.
- README.md is the user-facing product page. When a monitored source or a
  user-visible feature is added, changed, or removed, update README.md in the
  same change.
- Keep architecture/design docs implementation-independent where practical.
- Do not rewrite the specs to match an implementation shortcut. Fix the implementation instead.
