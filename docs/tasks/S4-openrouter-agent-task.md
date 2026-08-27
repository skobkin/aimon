# Task — S4 / v0.4.0: OpenRouter monitoring

Implement OpenRouter as a new Remote source kind in the existing AI Monitor
dynamic-instance architecture.

## Required deliverables

1. OpenRouter registry/source-kind integration.
2. Instance-scoped credential slots:
   - `api-key`
   - `management-key`
3. OpenRouter adapter:
   - API-key `/api/v1/key`
   - Management-key `/api/v1/credits`
   - strict request gating by credential presence
4. OpenRouter domain snapshot.
5. Spend-oriented Overview presentation.
6. OpenRouter Remote detail presentation.
7. Optional key-limit progress visualization.
8. Generic global `Hide sensitive info` preference.
9. Generic hover-to-reveal sensitive-value component.
10. Dynamic-source Settings editor support for both OpenRouter credentials.
11. Test Connection with independent capability results and no missing-key
    probes.
12. Dynamic tray/Web UI integration.
13. Unit/integration tests from `S4_IMPLEMENTATION_SPEC.md`.
14. Version bump to `0.4.0`.

## Hard acceptance conditions

- API key absent => **zero `/api/v1/key` requests**.
- Management key absent => **zero `/api/v1/credits` requests**.
- no credential substitution/fallback between slots.
- neither key => zero network requests.
- API-only, Management-only, and both-key instances all work.
- multiple OpenRouter instances work independently.
- account values are concealed by default and reveal on hover.
- per-key Today/Week/Month values remain visible.
- no fake three-period quota graphs.
- key limit uses one bar only when a real limit exists.
- no Activity/Analytics implementation.
- no local database/history.
- no secrets or real account data in tests/docs/logs.

## Workflow

1. Inspect current HEAD and S4 docs.
2. Produce a concrete plan for review.
3. After approval, implement in focused commits.
4. Keep every significant commit build/test green where practical.
5. Run:
   - `gofmt -w .`
   - `go test ./...`
   - `go vet ./...`
   - `go build ./...`
6. Report commits, verification, manual scenarios, and deviations.
