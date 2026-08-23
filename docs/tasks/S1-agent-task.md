# S1 Agent Task — Implement LLM Monitor

Implement S1 end-to-end from the repository specifications.

## Read first

Read, in order:

1. `AGENTS.md`
2. `docs/DECISIONS.md`
3. `docs/S1_IMPLEMENTATION_SPEC.md`
4. `docs/ARCHITECTURE.md`
5. `docs/UI_UX.md`
6. `docs/INTEGRATIONS.md`
7. `docs/REFERENCES.md`

Do not redesign the application while implementing it.

## Deliverable

A runnable Go/Fyne tray application implementing all four optional sources:

- amdgputop-web;
- llama-swap;
- MiniMax Token Plan;
- Z.AI / GLM Coding Plan.

It must include:

- tray lifecycle;
- borderless compact monitor with Overview / Local / Remote tabs;
- separate Settings window;
- independent configurable polling;
- manual refresh;
- normalized source/domain layer;
- graceful degradation and last-good-data retention;
- per-source freshness (`5s ago`, stale, unavailable, etc.);
- colored remaining-quota bars using the specified thresholds;
- system-keychain credential storage through `go-keyring`;
- tests for adapters, normalization, keychain abstraction, and source-manager behavior.

## Important implementation constraints

- S1 is read-only.
- No charts/history/process list.
- No plaintext secret storage or fallback.
- No source is mandatory.
- No same-source overlapping requests.
- Remote providers default to 60-second polling; local sources default to 5 seconds.
- Do not add a persistent normal dashboard window.
- Do not use platform-native/private hacks just to auto-hide the monitor on focus loss.
- Do not invent values absent from APIs.
- Do not use llama-swap GPU metrics in place of amdgputop-web.
- Do not mix quota used/remaining semantics in UI: S1 shows `NN% remaining` consistently.
- Use the standard Fyne widgets specified by `UI_UX.md`; the only approved custom visualization is `QuotaBar`.
- Keep all Fyne updates from background goroutines behind `fyne.Do`/`fyne.DoAndWait`.

## API work

Before locking response DTOs, inspect the current upstream APIs/source references listed in `docs/REFERENCES.md`.

MiniMax and Z.AI quota schemas require defensive parsing. Use sanitized/synthetic fixtures and tests. Never put a real API token or private captured identifier in the repository.

For llama-swap, implement active/waiting request counts only if current public API/metrics expose stable semantics. If not, omit those optional fields and state this in the completion report.

## Validation before completion

Run at least:

```sh
gofmt -w .
go test ./...
go vet ./...
```

Also manually sanity-check:

- zero-source startup;
- one failing source while another works;
- stale last-known-good data;
- tray open/hide and Esc behavior;
- Settings without keychain availability;
- light and dark theme readability of QuotaBar.

## Final report

Report:

- what was implemented;
- package/file structure;
- test/vet results;
- any current upstream API fields intentionally omitted because semantics were not reliable;
- any Fyne/Linux tray limitations observed;
- no speculative "future improvements" unless they are actual blockers or bugs discovered during implementation.
