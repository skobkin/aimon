# S1 Implementation Specification

## 1. S1 outcome

Deliver a working Linux desktop application that lives in the system tray and provides a compact current-status monitor for any enabled subset of four sources:

- amdgputop-web;
- llama-swap;
- MiniMax Token Plan;
- Z.AI / GLM Coding Plan.

The application must remain useful when sources are missing, disabled, stale, or temporarily unreachable.

## 2. Technology baseline

- Go: 1.26
- GUI: Fyne 2.8.x (current stable 2.8.0 is an acceptable starting point)
- Secret storage: `github.com/zalando/go-keyring`
- Logging: standard library `log/slog`
- HTTP: standard library `net/http`
- Tests: standard Go testing + `httptest`; small helper libraries are optional but not required

Prefer a small dependency footprint.

## 3. Application identity

Working product name: **LLM Monitor**.

If implementation starts in a repository that already has a module path/application ID, preserve it.

If the repository is completely empty and a module path is needed, use a clearly centralized provisional identity such as:

```text
github.com/skobkin/llm-monitor
```

Do not scatter the product name/module ID as unrelated literals throughout the code.

Add `FyneApp.toml` and enable the `fyneDo` migration because background refreshes will update the UI through Fyne's current threading model.

## 4. S1 functional requirements

### FR-1 Tray lifecycle

- App starts into the tray without requiring a persistent normal window.
- Tray has a static icon.
- Tray left-click controls the compact monitor through Fyne `SetSystemTrayWindow` where supported.
- Tray context menu contains Show Monitor, Refresh, Settings, Quit.
- Closing/hiding monitor does not quit the app.
- Quit stops all pollers and exits cleanly.

### FR-2 Monitor tabs

Monitor contains:

- Overview;
- Local;
- Remote.

Overview is default.

Implement the structure and information density defined in `UI_UX.md`.

### FR-3 Optional sources

Each source can be independently enabled and configured.

The app must start with zero enabled sources.

Disabled source cards are omitted from Overview.

### FR-4 Source freshness/status

Each enabled source displays independent state/freshness.

Track:

- refreshing;
- last attempt time;
- last successful time;
- last error;
- last valid snapshot.

Support derived states Loading, Healthy, Stale, Unavailable.

### FR-5 Independent polling

Implement independent source refresh loops.

Default intervals:

- amdgputop-web 5s;
- llama-swap 5s;
- MiniMax 60s;
- Z.AI 60s.

Enforce S1 minimums of 5s local and 30s remote in config validation/UI choices.

No overlapping refresh of the same source.

### FR-6 Manual refresh

Refresh action triggers all enabled sources asynchronously.

If a source is already refreshing, coalesce/skip duplicate work.

Existing visible data stays on screen during refresh.

### FR-7 AMD GPU source

Use amdgputop-web REST polling.

Implement:

- GPU discovery;
- selected GPU preference;
- current metrics;
- Overview metrics: GPU utilization, VRAM, temperature;
- Local details: available current metrics listed in integration/UI specs.

No process list and no charts.

### FR-8 llama-swap source

Implement:

- health/connectivity;
- running model list;
- Overview running-model summary;
- Local detailed running-model list;
- active/waiting requests only if current public API/metrics expose stable semantics;
- optional keychain-stored API key if deployment requires it;
- Open llama-swap UI navigation action.

No llama-swap write API calls.

### FR-9 MiniMax source

Implement authenticated polling of the documented Token Plan remains endpoint.

Normalize at least:

- 5-hour/current interval remaining percentage;
- weekly remaining percentage.

Use sanitized parsing fixtures/tests.

### FR-10 Z.AI source

Implement authenticated polling of the current official-plugin quota endpoint.

Normalize at least:

- token 5-hour quota remaining;
- MCP monthly quota remaining when present.

Treat API schema as semidocumented and isolate parsing.

Do not invent reset time.

### FR-11 Quota rendering

All remote quota rows display:

- quota window name;
- numeric `NN% remaining`;
- custom `QuotaBar` filled to the remaining fraction;
- threshold color based on remaining quota.

Thresholds:

```text
<=10% red
<=20% orange
<=50% yellow
>50%  green
```

Use one reusable implementation for all providers.

### FR-12 Settings

Implement separate normal Settings window.

Persist non-secret settings using Fyne Preferences.

Support:

- source enabled flags;
- source/base URLs where relevant;
- GPU selection;
- poll interval selections;
- credential set/update/delete via system keychain;
- asynchronous Test connection per source;
- inline validation/status.

Do not store secrets in Preferences.

### FR-13 Keychain

Implement an injectable keychain abstraction based on `go-keyring`.

Expose backend state at least as available/locked/unavailable/unsupported.

Failure to access keychain must not crash app.

Authenticated source with inaccessible credentials becomes unavailable/stale while unrelated sources continue.

### FR-14 Graceful degradation

If refresh fails after prior success:

- keep last snapshot;
- mark stale;
- show age of last successful data;
- show concise failure detail in Local/Remote detail.

If no successful snapshot exists:

- show unavailable state;
- do not fabricate metric values.

### FR-15 Fyne threading

All network/keychain/background work runs outside UI callbacks.

All Fyne mutations from application goroutines use `fyne.Do`/`fyne.DoAndWait`.

## 5. Non-functional requirements

### NFR-1 Idle overhead

The app should be cheap when idle.

- no sub-5-second local polling;
- no sub-30-second remote polling;
- no chart/history sampling;
- no WebSocket connection needed for S1;
- avoid logging each successful local poll at INFO.

### NFR-2 Failure isolation

One source failure must not panic, block UI, or cancel unrelated source pollers.

### NFR-3 Secret safety

No secrets in:

- repository;
- Preferences;
- logs;
- screenshots/fixtures generated for tests;
- error messages shown in UI.

### NFR-4 Testability

Adapters, credential store, polling manager, normalization, and view-model formatting must be testable without real services.

### NFR-5 Theme compatibility

Monitor/Settings and custom QuotaBar must remain legible under Fyne light and dark themes.

## 6. Implementation phases

These phases are sequencing guidance. Complete S1 end-to-end rather than stopping after scaffolding.

### Phase 1 — Foundation

- initialize/preserve Go module;
- add Fyne metadata;
- app lifecycle/tray shell;
- non-secret config repository;
- credential store abstraction;
- domain types;
- source manager skeleton.

Tests:

- config validation;
- keychain classification;
- source-state derivation.

### Phase 2 — Local adapters

Implement:

- amdgputop-web;
- llama-swap.

Use `httptest` fixtures.

Do not build source-specific UI inside adapter packages.

### Phase 3 — Remote adapters

Implement:

- MiniMax;
- Z.AI.

Build defensive parsers and sanitized tests.

### Phase 4 — Polling/runtime state

Implement:

- independent poll loops;
- refresh coalescing;
- cancellation;
- last-good snapshot retention;
- recovery from failures;
- manual refresh.

### Phase 5 — Monitor UI

Implement UI exactly from `UI_UX.md`:

- tray splash window;
- tabs;
- source cards;
- overview;
- local detail;
- remote detail;
- QuotaBar;
- Esc hide;
- Refresh/Settings actions.

### Phase 6 — Settings UI

Implement:

- forms;
- validation;
- source enable/disable;
- URLs;
- polling selects;
- GPU discovery/selection;
- keychain credential editing;
- connection tests;
- save/cancel semantics.

### Phase 7 — Integration hardening

- wire application startup/shutdown;
- verify no source blocks another;
- verify zero-source startup;
- verify stale states;
- verify light/dark theme layout;
- ensure no secret logging;
- gofmt/test/vet.

## 7. Acceptance scenarios

S1 is not complete until these scenarios work.

### A — Nothing configured

Start app with no prior preferences.

Expected:

- process stays alive in tray;
- monitor opens;
- Overview has no source cards and explains nothing is enabled;
- Local/Remote show empty states;
- Settings is accessible.

### B — Only GPU enabled

Expected:

- AMDGPU card appears;
- remote section/cards absent from Overview;
- Remote tab shows empty state;
- GPU refresh continues independently.

### C — Only remote provider enabled

Expected:

- app does not require any local service;
- quota appears with numeric remaining percentage and colored bar.

### D — One source fails at startup

Expected:

- failed source shows unavailable;
- other sources keep refreshing;
- UI remains responsive.

### E — Source fails after success

Expected:

- old values remain visible;
- state becomes stale;
- age continues increasing;
- later successful refresh returns state to healthy.

### F — Keychain locked/unavailable

Expected:

- app stays alive;
- unauthenticated/local sources still work;
- affected remote source explains credential/keychain problem;
- Settings exposes keychain status;
- no plaintext fallback offered.

### G — Remote API rate-limits

Expected:

- 429 is shown as rate-limited/stale or unavailable;
- no immediate retry storm;
- next configured poll/manual refresh is the next attempt.

### H — Manual Refresh while local source already refreshing

Expected:

- no second concurrent request to that same source;
- other idle enabled sources refresh immediately.

### I — Monitor keyboard dismissal

Expected:

- Esc hides monitor;
- app remains in tray;
- re-opening shows current state.

### J — Settings secret update

Expected:

- secret stored in system keychain;
- Preferences contain no token;
- UI does not log or redisplay token in plaintext;
- source can use new credential after save.

## 8. Explicit S1 exclusions

Do not implement even if easy:

- dynamic tray-icon fill/status art;
- notifications;
- charts;
- historical persistence;
- GPU process table;
- remote account history;
- quota prediction;
- llama-swap load/unload;
- log viewer;
- source-specific custom dashboards;
- native focus hooks for auto-hide;
- automatic retry/backoff framework beyond normal polling;
- plugin system/dynamic Go loading.

"Modular sources" means clean internal adapters/configuration, not runtime shared-library plugins.

## 9. Definition of done

- all required sources implemented;
- UI follows wireframes/component contract;
- keychain integration works through abstraction;
- graceful degradation acceptance scenarios covered manually and/or by tests;
- no known secret leakage;
- `gofmt` clean;
- `go test ./...` passes;
- `go vet ./...` passes;
- known platform limitations documented in implementation summary rather than hidden behind hacks.
