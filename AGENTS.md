# AGENTS.md

## Project intent

This project is a small, tray-first desktop monitor for:

- local AMD GPU telemetry from `amdgputop-web`;
- local model/runtime state from `llama-swap`;
- MiniMax Token Plan quota;
- Z.AI / GLM Coding Plan quota.

S1 is a **read-only monitor**, not an administration console.

## Authoritative documents

Read these before implementation:

1. `docs/DECISIONS.md`
2. `docs/S1_IMPLEMENTATION_SPEC.md`
3. `docs/ARCHITECTURE.md`
4. `docs/UI_UX.md`
5. `docs/INTEGRATIONS.md`
6. `docs/tasks/S1-agent-task.md`

When documents overlap, the order above is the precedence order. Do not redesign the product or UI while implementing it.

## Non-negotiable constraints

- Use Go and Fyne.
- Target Fyne 2.8.x APIs; S1 is primarily a Linux desktop application.
- Runtime UI is tray-first: a compact borderless monitor window plus a separate normal Settings window.
- S1 is read-only. Do not add model unload/load, process killing, quota management, log viewing, or other control operations.
- All data sources are optional and independent.
- One failed or disabled source must not break other sources or the application.
- Never discard the last successful snapshot just because a later refresh failed.
- Disabled sources are omitted from Overview. Enabled but unavailable sources remain visible and report their state.
- Do not store API keys/tokens/passwords in Fyne Preferences, config files, source code, logs, test fixtures, or documentation.
- Secrets must use the system keychain through `github.com/zalando/go-keyring`, following the same general store/status pattern as `skobkin/qBtRemoteGo`.
- There is no plaintext secret fallback in S1.
- Provider/source-specific DTOs must stop at the adapter boundary. UI code consumes normalized domain snapshots/view models only.
- Network I/O must never run synchronously in a Fyne event callback.
- Any Fyne API call made from an application-created goroutine must be scheduled with `fyne.Do` or `fyne.DoAndWait` as appropriate.
- Do not add historical persistence or charts in S1.

## UI implementation rules

Treat `docs/UI_UX.md` as a UI contract, not inspiration.

- Use `container.AppTabs` for the monitor's Overview / Local / Remote navigation.
- Use `widget.Card` for source cards.
- Use standard Fyne controls for forms: `widget.Form`, `widget.Entry`, `widget.NewPasswordEntry()`, `widget.Select`, `widget.Check`, and normal buttons.
- Do not use free-text inputs for enumerated polling intervals.
- Do not create custom dropdowns, tabs, toggles, forms, card systems, or dialogs.
- The one explicitly approved custom visualization is the reusable `QuotaBar`, because standard `widget.ProgressBar` has no per-instance fill-color API.
- Do not introduce circular gauges.
- Do not introduce charts.
- Do not use color as the only status signal; always pair status color with text/iconography.
- High GPU utilization is not an error and must not be colored as a warning merely because it is high.
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
- Keep README intentionally minimal unless the user explicitly asks to expand it.
- Keep architecture/design docs implementation-independent where practical.
- Do not rewrite the specs to match an implementation shortcut. Fix the implementation instead.
