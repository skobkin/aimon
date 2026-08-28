# Task — S5 / v0.5.0: aggregate Hindsight monitoring

Implement Hindsight as a new aggregate-only Local source in AI Monitor.

## Required deliverables

1. `hindsight` compiled source kind + dynamic-instance registry integration.
2. Provider settings:
   - API Base URL;
   - optional Web UI URL.
3. Optional instance-scoped API-key credential.
4. Hindsight adapter using only:
   - `/health`
   - `/health/live`
   - `/metrics`
   - paginated `/v1/default/banks`
5. Adapter-local slow-data cache so ordinary polling does not repeatedly fetch
   bank summaries/liveness.
6. Aggregate bank count/fact count/last-write timestamps.
7. Focused Prometheus exposition parsing for selected metrics.
8. In-memory operation/LLM/token/HTTP rate derivation.
9. Opportunistic optional backlog/queue metrics.
10. DB pool/runtime aggregate metrics.
11. Compact Overview presentation.
12. Local detail presentation.
13. Settings/Test Connection integration.
14. Optional Control Plane Web UI navigation.
15. Unit/integration/request-budget/privacy tests.
16. Version bump to 0.5.0 / Build 5.

## Hard acceptance conditions

- no per-bank endpoint requests;
- no bank IDs/names/missions/content in normalized domain/viewmodel data;
- no `/version` polling;
- no LLM health polling;
- backlog metrics are optional and absence is not rendered as zero/error;
- no API key is required for unauthenticated deployments;
- no Authorization header is sent when key is absent;
- fast refresh uses only `/health` + `/metrics`;
- slow bank/liveness data is refreshed roughly once per minute;
- bank pagination works for >1000 banks without per-bank fan-out;
- first counter sample does not show fake zero rates;
- counter resets do not produce negative rates;
- no local DB/history/charts.

## Workflow

1. Inspect current HEAD after S4.
2. Read S5 docs and current decision log.
3. Produce a concrete implementation plan for approval.
4. Do not code until plan approval.
5. Implement in focused commits.
6. Run:
   - `gofmt -w .`
   - `go test ./...`
   - `go vet ./...`
   - `go build ./...`
7. Report commits, tests, request-budget verification, manual checks and any
   deviations.

## Safe manual acceptance

Use fake Hindsight endpoints for automated acceptance.

Real instance checks left to the user:

- unauthenticated local instance;
- optional authenticated instance if available;
- cards show only aggregate data;
- no project/bank names visible anywhere;
- backlog rows appear only when Hindsight exposes the gauges;
- Web UI button works only when configured.
