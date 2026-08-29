# Decision Log

Dated record of implementation-time decisions that change or extend the
authoritative documents (`AGENTS.md`, `docs/DECISIONS.md`,
`docs/S1_IMPLEMENTATION_SPEC.md`, `docs/ARCHITECTURE.md`, `docs/UI_UX.md`,
`docs/INTEGRATIONS.md`).

The authoritative documents are not rewritten retroactively. An entry here
supersedes the affected document from its date onward. Newest entries
first.


## 2026-08-29 — Per-second ticker mutates labels in place; notifications coalesce; keychain calls honor ctx

Follow-ups to the 2026-08-28 memory-leak fix. The visibility gate stopped
rebuilding while hidden; the remaining rebuild work (visible rebuilds once
per poll burst, plus a rebuild every second while visible) is now bounded
in three targeted ways. The full persistent-widget refactor remains a
proposal (`docs/PROPOSAL_PERSISTENT_WIDGET_UI.md`, intentionally
uncommitted) — these fixes deliberately avoid restructuring the widget
tree.

- **Time-only updates no longer rebuild.** `rebuild` collects per-label
  refresher closures (`monitor.tickFns`); the 1-second ticker calls them
  instead of rebuilding the tree. The view models carry the absolute
  `ResetAt` next to the rendered `ResetText` (`QuotaWindowVM`,
  `OverviewDonutVM`), components return a `func(now time.Time)` alongside
  the object (`NewQuotaRow`, `NewQuotaOverviewBody`, `NewSourceCard`), and
  `FreshnessText`/`FormatResetCountdown` re-derive the text from the
  captured instant. A write that would not change the text is skipped, so
  seconds without a visible change do not repaint. Regression coverage:
  `TestUpdateTimeTextsAgesLabelsInPlace` asserts the texts age while every
  canvas object keeps its identity.
- **Poll bursts collapse into one UI dispatch.** Every poller finishing
  within one dispatch window used to schedule its own `fyne.Do` rebuild;
  the app's subscriber now routes notifications through a pending-flag
  coalescer (`uiCoalescer`), so N simultaneous completions cost one
  rebuild, and a notification arriving during the dispatched rebuild
  schedules a fresh one instead of being lost.
- **Keychain operations honor their context.** go-keyring's Secret Service
  binding is synchronous and can block indefinitely (wedged dbus daemon,
  unseen unlock prompt); the library has no context support and its
  prompt-signal channel leaks per `Set` (upstream; v0.2.8 and master). In
  this repo, `KeyringStore` now runs every operation under the caller's
  ctx and abandons it on deadline, reporting an unavailable backend
  (`await` in `internal/credentials/keyring.go`), and the startup probe is
  bounded by `keyringProbeTimeout` (5s) instead of `context.Background()`.
  Abandonment bounds caller latency, not the abandoned goroutine — the
  upstream fix is proposed, not applied.


## 2026-08-28 — Monitor rebuilds are visibility-gated (memory-leak fix)

Running the tray instance for ~24h grew RSS by gigabytes with no window
ever opened. The leak chain: every poll notification re-rendered the whole
monitor (`OnStatesChanged` → full tree rebuild of all three tabs), Fyne
creates widget renderers during layout even when nothing is painted, and a
hidden window never paints, so Fyne's renderer expiry
(`destroyExpiredRenderers`, which only runs on canvas refresh) never
purged them — every hidden rebuild permanently retained its entire widget
tree in Fyne's global renderer cache until quit. Verified with an
instrumented lab run (stub llama-swap sources polling at 5s): ~140 KB/s
anonymous-memory growth while hidden, with the live heap dominated by
`widget.RichText` and `components.NewSourceCard` allocations from the
discarded trees.

Two changes, both in the same direction — rebuild only when somebody can
see it:

- `monitor.OnStatesChanged` skips the rebuild while the window is hidden.
  `Show` still rebuilds from the current states before revealing the
  window, so no freshness is lost while hidden.
- The manager notifies subscribers once per refresh, on completion, not
  also on the in-flight start. No view model renders
  `Runtime.Refreshing` (it remains internal trigger-coalescing state),
  and the doubled notification rate doubled the per-poll UI rebuild work.

Live countdown and quota-reset freshness keep their 1-second ticker while
the window is visible; this only removes work that happened while nobody
was looking. Regression coverage: a hidden monitor must not rebuild
(`TestHiddenMonitorSkipsRebuilds`), and a refresh must notify exactly one
completion event (`TestManagerSubscribe`).


## 2026-08-25 — S2 Overview becomes a compact 2×2 presentation surface

Overview is no longer a vertical reuse of the Local/Remote detail card bodies.
It remains split into Local and Remote sections, but when both sources in a
section are enabled they are rendered as two cards side by side. A single card
in a section may use the full available width rather than leaving a blank
second column.

Overview rendering is presentation-oriented rather than provider-oriented:

- `MetricsOverview` for metric/telemetry summaries (`amdgputop-web`);
- `RuntimeOverview` for running-model/runtime summaries (`llama-swap`);
- `QuotaOverview` for quota-only providers (MiniMax and Z.AI / GLM).

Provider-specific API semantics still end at the adapter/domain boundary.
These presentation kinds belong to the UI/view-model layer and must not become
provider base classes or leak back into source adapters.

Why: after real use, Overview was useful but too similar to the detail tabs.
The 2×2 layout makes it a genuine glance surface while Local and Remote keep
full detail.

Updates/supersedes: `docs/UI_UX.md` §7 and the old assumption that Overview and
detail tabs share one card body renderer.

## 2026-08-25 — Quota reset time is normalized as absolute `ResetAt`

Both remote quota providers expose enough information to show time until each
quota window resets. The normalized domain representation remains an absolute
`QuotaWindow.ResetAt`; the UI derives a relative countdown locally so the text
can update without polling the provider more often.

Verified source semantics used for S2:

- MiniMax `model_remains[model_name == "general"].remains_time` is the remaining
  milliseconds until the current 5-hour window reset;
- MiniMax `weekly_remains_time` is the remaining milliseconds until the weekly
  window reset;
- Z.AI `nextResetTime` is an absolute epoch-millisecond reset time and is
  already normalized by the current adapter.

For MiniMax, `remains_time` / `weekly_remains_time` now take precedence for the
corresponding reset countdown. Do not interpret `start_time` / `end_time` as
quota-window reset times in S2 merely because the old adapter did so. Legacy
explicit reset fields may remain defensive fallbacks when their semantics are
already covered by tests.

Why: the same APIs are already used successfully by the user's Claude Code
statusline integrations, and reset countdown is high-value information that
costs no extra vertical space when placed inline.

Updates/supersedes: MiniMax adapter comments/tests that intentionally ignored
`remains_time` / `weekly_remains_time`, and any use of `end_time` as the primary
5-hour reset source.

## 2026-08-25 — Reset countdown is inline and quota UI stays used-oriented

Quota display remains **used-oriented**: percentage text and visualization fill
show the consumed fraction; threshold color continues to represent quota
pressure using remaining quota.

In the Remote detail tab, reset time is added to the existing quota header line
without adding another vertical row:

`5-hour · 2h14m                         17% used`

No literal `reset` word is shown. When reset time is unavailable, the suffix is
simply omitted. Raw counts/credits remain on their existing secondary line.

In Overview, quota counts are omitted. Each quota donut shows the used
percentage and a short window label (`5h`, `week`) in/around the ring, with the
relative reset countdown immediately below it.

Why: users of subscription LLM plans understand the time as the window reset;
spelling out `reset` adds visual noise and no useful information.

Updates/supersedes: quota wireframes in `docs/UI_UX.md` and the 2026-08-23
compact quota-row entry by adding inline reset text.

## 2026-08-25 — S2 Overview custom visualizations are narrowly approved

The S1 restriction against circular gauges/custom visualizations is relaxed
only for the new compact Overview:

- `amdgputop-web` uses compact current-value usage bars for GPU and VRAM. The
  label and value are combined inside a neutral semi-transparent pill over the
  bar so no separate left-side label column consumes width/height. Temperature
  remains a plain colored metric row.
- quota providers use two small donut/ring gauges side by side (normally 5-hour
  and weekly). Arc length is **used** fraction. Ring color uses the existing
  quota threshold color derived from remaining quota.
- Remote detail continues to use the existing linear `QuotaBar`; Local detail
  keeps its existing metric rows.

The first implementation deliberately uses a neutral translucent pill on the
metric bars instead of a more complex split-color/clipped text renderer. A
future iteration may improve the text rendering if necessary.

No historical charts, sparklines, persistence, or charting dependency are part
of this task.

Why: these forms improve glanceability and density in the constrained 2×2
Overview, while detail tabs already have a good textual/linear representation.

Updates/supersedes: `AGENTS.md` old single-custom-widget/circular-gauge rules
and `docs/DECISIONS.md` D11/D14 for the current S2 task only.

## 2026-08-25 — Tray submenu label and deferred scope

The tray submenu currently labelled `Open` is renamed to `Web UIs` while
keeping the same enabled-source links and behavior.

Dynamic tray telemetry (one/two configurable bars or other selected metrics) is
explicitly deferred to a later iteration. Historical GPU/VRAM charts are also
deferred. Detail-tab Web UI buttons are not redesigned in this task.

Why: `Web UIs` states what the submenu contains; the deferred features should
not be mixed into the focused Overview-density pass.

## 2026-08-23 — Rebrand: LLM Monitor → AI Monitor (`aimon`)

The product is renamed AI Monitor. The binary is `aimon` (`cmd/aimon`),
the Go module is `git.skobk.in/skobkin/aimonitor` (self-hosted Forgejo at
`https://git.skobk.in`, not GitHub), the Fyne app ID is
`com.skobkin.aimonitor`, the keychain service is `aimonitor`, and the
in-window "LLM Monitor" chrome label is removed (the window title and
the tray tooltip already name the app).

Consequences of the full rename (user-approved): API keys stored under
the old keychain service (`llm-monitor`) are not migrated and must be
re-entered once in Settings, and the new app ID starts from fresh Fyne
preferences (enabled flags and intervals reset to defaults).

Why: user decision after first real-world use — the tool monitors more
than LLM quotas (local GPU and runtime telemetry).

Updates: the product naming across `AGENTS.md` and `docs/` ("LLM
Monitor", `cmd/llm-monitor`, module `github.com/skobkin/llm-monitor`) is
superseded from this date; those documents are historical and are not
rewritten.

## 2026-08-23 — Plain quota window names, matching MiniMax

Z.AI window names drop the quota-kind prefixes ("Token usage · 5 hour" →
"5-hour", "MCP usage · 1 month" → "Monthly", "Credit usage · Weekly" →
"Weekly"): every card is per-source, so the kind added no information
and made rows harder to read. The kind is not lost — credit windows
label their counts line "xx / yy credits" under the bar (the payload's
numeric unit code carries no display meaning).

Why: user request after first real-world use — label consistency
between the MiniMax and Z.AI cards.

Updates: the Z.AI window naming in `docs/INTEGRATIONS.md` §5 and the
quota-row wireframes in `docs/UI_UX.md`.

## 2026-08-23 — Local metric values colored by load

GPU utilization, VRAM and temperature values are colored green → amber →
orange → red by load, in both the Overview and Local tabs. Thresholds
(user-agreed):

- utilization and VRAM, as a fraction of capacity: green ≤50%, amber
  50–80%, orange 80–90%, red >90% — the quota-bar scheme mirrored onto
  usage;
- temperature: green <60 °C, amber 60–70, orange 70–80, red ≥80.

Other rows (GTT, power, clocks) and "—" placeholders stay uncolored. The
value text itself always remains, so color is never the only signal.

Why: user request after first real-world use. This is a recorded
exception to the AGENTS.md rule that high GPU utilization must not be
colored as a warning merely because it is high; the rule's intent
(no false "error" styling) is kept by using informational load bands
rather than error semantics.

Updates: the AGENTS.md "UI implementation rules" bullet noted above.

One quota window renders as a single header line — bold window name on
the left, "NN% used" right-aligned — with the bar directly beneath and
the optional raw-counts line after it. The name/percent/bar stack of
three lines is gone. The layout is shared by every tab (Overview and
Remote render quota rows through the same component).

Why: user request after first real-world use — one line less per window
makes the overview noticeably more compact and readable.

Updates: the quota-row wireframe in `docs/UI_UX.md`.

## 2026-08-23 — Quota display is used-oriented

Quota progress bars fill with the **used** fraction and the label reads
"NN% used" (was: fill = remaining, "NN% remaining"). The threshold color
is unchanged and still reflects how much quota is left (red = almost
exhausted, now a nearly full bar). The raw-counts line under the bar (now
that real payloads provide counts) is used-oriented too: "used / limit
[unit]".

Why: user request after first real-world use — usage progress reads more
naturally than remaining progress.

Updates: `docs/INTEGRATIONS.md` §5 — "The UI must still say `NN%
remaining`, just like MiniMax" is superseded for both providers; the
`docs/UI_UX.md` "NN% remaining" wording likewise.

## 2026-08-23 — Open-web-UI buttons on every source card

Every source card carries an "open the provider's own UI" navigation
button: llama-swap UI (`{base}/ui`), amdgputop-web (base URL root),
MiniMax (`https://platform.minimax.io/console/usage`) and Z.ai
(`https://z.ai/manage-apikey/coding-plan/personal/usage`).

Why: UI parity requested after first real-world use — the llama-swap card
had such a button, the other three did not. Navigation only; the read-only
constraint is intact (no control APIs).

Updates: `docs/UI_UX.md` (the button was specified for llama-swap only).
Commit: `cb9a256`.

## 2026-08-23 — App metadata set at startup

The application sets its Fyne metadata (ID, name, version, build, fyneDo
migration flag) explicitly at startup, mirroring `FyneApp.toml` (a test
guards the duplication).

Why: Fyne reads `FyneApp.toml` at runtime only when it sits next to the
executable or in the working directory. `go install` and any launch from
outside the repository left the metadata empty, so Fyne logged the
not-migrated-to-fyne.Do warning on every start and the startup log had an
empty version.

Updates: packaging/runtime assumption in `docs/ARCHITECTURE.md`.
Commit: `bd88b0a`.

## 2026-08-23 — Z.AI: dual credit windows, counts, and reset times from real payloads

The quota endpoint serves newer Coding Plans two `CREDIT_LIMIT` entries —
one per window — distinguishable only by reset time: the entry resetting
sooner is the 5-hour window ("Credit usage · 5 hour", `credits-5h`), the
later one the weekly window ("Credit usage · Weekly", `credits-weekly`).
Verified count semantics: `usage` is the limit, `remaining` what is left,
`currentValue` consumed; counts are preferred over the scale-ambiguous
percentage (which stays as fallback). `nextResetTime` is an absolute epoch
and becomes the window reset time. `unit` can be numeric and is decoded
loosely.

Why: real payloads broke the previous single-credit-window, string-unit
parsing ("malformed JSON: cannot unmarshal number into … unit"), and the
working statusline integrations verified the field semantics above.

Updates: `docs/INTEGRATIONS.md` §5 — including its claims that the
endpoint "does not provide the desired quota reset time" (it does, per
window) and that only one credit window exists.
Commit: `df516d8`.

## 2026-08-23 — MiniMax: the plan bucket is literally named "general"

Entry selection among `model_remains[]` matches the exact `model_name`
"general" first — the text/chat plan lane real payloads expose and the
bucket known-good statusline integrations read. The coding/text name
heuristics remain as fallbacks; the ambiguity error lists the entry names.

Why: real Token Plan payloads list the general bucket alongside lanes
whose names carry no coding/text hint, so the heuristics never matched
and refresh failed with "cannot identify plan entry among 2 models".

Updates: `docs/INTEGRATIONS.md` §4 entry-selection guidance (naming-based
selection, now anchored to the verified literal name).
Commit: `e54ec31`.

## 2026-08-27 — S4: SensitiveValue joins the approved custom widgets; inverted privacy preference storage; per-capability Test Connection

Three S4 decisions extend established app-wide patterns.

1. `components.SensitiveValue` is a new custom widget and the first since
the S2 list in AGENTS.md ("standard widgets only" with narrowly scoped
exceptions). Like `QuotaBar`/`MetricBar` it is presentation-only, has no
configuration surface, and exists because no standard widget can mask
text and reveal it on hover. It holds only display state (the real text,
the privacy predicate, hover); masking never mutates the value. Its
renderer measures both the real and masked strings and reserves whichever
is wider, so conceal/reveal cannot reflow rows or shift them under a
stationary pointer (hover flicker).

2. The global "Hide sensitive info" preference defaults to *hidden*, but
Fyne's `Preferences.Bool(key)` cannot distinguish an absent key from
false. Storing it as inverted `privacy.show-sensitive`
(`HideSensitiveInfo() = !Bool(key)`, absent ⇒ false ⇒ hidden) keeps the
repository's narrow preference abstraction intact instead of widening it
with key-existence checks. A source-config migration would have been the
alternative; none is warranted for one UI flag.

3. Test Connection becomes per-capability: `registry.Definition.Test`
returns `TestReport{Lines, OK}` where `OK = tested > 0 && failed == 0`.
Kinds with several independent credentials (OpenRouter) report each slot
separately — including "not configured (skipped)" — and make zero HTTP
requests for absent credentials; with nothing configured the test is not
presented as success because nothing was verified. Single-credential
kinds wrap their historical single-line wording unchanged.

## 2026-08-29 — S5: Hindsight refresh timeout is 10s; db_pool_waiting is sourced from /health first

Two sourcing decisions for the new Hindsight adapter, both about trusting
the right upstream surface.

1. `RequestTimeout` for the Hindsight source is the 10s remote convention,
   not the 3s local-daemon value, even though Hindsight is categorized
   Local (S5: Local is a product grouping, not a latency promise). One
   refresh chains `/health` + `/metrics` and, when the adapter-local slow
   cache has expired, `/health/live` plus every page of the bank list
   sequentially under a single context; on a LAN with a few thousand banks
   the 3s budget would spuriously fail otherwise-healthy deployments.

2. The DB Waiting row prefers the optional `db_pool_waiting` field of
   `/health` and falls back to the Prometheus `hindsight_db_pool_waiting`
   gauge only when the health value is absent. Upstream documents
   `db_pool_waiting` as a readiness/database-pressure diagnostic of the
   health endpoint, while the Prometheus DB-pool set (size/idle/min/max)
   does not guarantee a waiting gauge; neither source is ever required.
   This replaces the earlier S5 draft stance that /health pool diagnostics
   are ignored — `db_acquire_ms` and `db_pool_waiting` are now both parsed
   from `/health`, and `/metrics` remains the source for pool
   size/idle/max.

## 2026-08-29 — S5 follow-ups: adapter rebuild on Web UI URL edits, Test Connection line folding, /metrics body cap

Three smaller S5 notes recorded at implementation time.

1. `SameSettings` compares both Hindsight URLs, so editing the optional
   Web UI URL rebuilds the adapter like any settings change. This resets
   the counter baseline, so the next interval reports rates as
   unavailable (first sample) before recovering — accepted as the cost of
   keeping the adapter immutable; it only happens on an explicit edit.

2. The Test Connection report folds `/health/live` into the "Health:"
   line (the spec wants three lines for four probes) and prints the bank
   aggregate as "Banks: OK (N visible)" — a count only, never identifiers.

3. `/metrics` responses larger than the shared 1 MiB `GetText` cap fail
   the refresh as an invalid response rather than being parsed partially.
   A real Hindsight deployment exposing the selected families stays far
   below the cap; a blown cap almost certainly means a proxy error page,
   which should not be interpreted as metrics.
