# Decision Log

Dated record of implementation-time decisions that change or extend the
authoritative documents (`AGENTS.md`, `docs/DECISIONS.md`,
`docs/S1_IMPLEMENTATION_SPEC.md`, `docs/ARCHITECTURE.md`, `docs/UI_UX.md`,
`docs/INTEGRATIONS.md`).

The authoritative documents are not rewritten retroactively. An entry here
supersedes the affected document from its date onward. Newest entries
first.

## 2026-08-30 — Codex launcher discovery falls back to the user bin directories

The application is started two ways on one machine: from a terminal, whose
`PATH` carries `~/.local/bin`, and from the desktop (a systemd user
session), whose `PATH` does not. A bare configured launcher such as `cdx`
resolved from the terminal and reported "not found in PATH" from the app,
although the file was exactly where the user's shell found it.

Discovery now searches the per-user executable directories `~/.local/bin`
and then `~/bin` — via `os.UserHomeDir()`, never `$HOME` directly — for a
bare name the effective `PATH` did not resolve. They come after the whole
effective `PATH`, so a `PATH` override keeps its order and keeps winning,
and they are not consulted on Windows, which has no such convention. This
extends `docs/S6_CODEX.md` §Environment and executable resolution
("Executable lookup must use the effective PATH") from this date onward:
the effective `PATH` is still what is searched first, and the user bin
directories only close the gap between a terminal-resolvable and an
app-resolvable launcher. The not-found setup message names both
directories, so the lookup stays cheap and pure-filesystem and the message
promises exactly what is searched.

## 2026-08-30 — The Codex app-server's PATH starts with the launcher's directory

The same report's second half: the launcher was a wrapper script (`exec
graftcp ~/.local/bin/codex`), and its own bare-name lookups happen in the
child's `PATH` — the GUI session's, without the directory the wrapper and
its tools live in. The shell exited 127 and no app-server ever started.

The child environment is now built by `childEnv`, which prepends the
resolved launcher's directory to the effective `PATH`, and forms a `PATH`
out of it when the effective environment has none at all. A directory
already on the child's `PATH` is left exactly where the user put it — no
duplicate entry, no silent reordering of an override — and everything
behind it stays the user's `PATH`, override included. Discovery and exec
share one environment builder, so what discovery resolved is what the
wrapper can reach by name. This extends §Environment and executable
resolution ("Effective env = parent env + overrides") from this date
onward.

## 2026-08-30 — A Codex CLI that exits during startup is no longer reported as incompatible

`classifyStageError` mapped every transport-shaped initialize failure onto
the authored incompatible-CLI message, so the wrapper's exit 127 surfaced
as "This Codex CLI does not support the app-server rate-limit interface"
and the connection test said "Codex CLI: incompatible / This version does
not support account/rateLimits/read" — an environment failure sold as a
missing feature.

The initialize failure is now classified only after the process has been
reaped, and the process's own exit status decides the wording. A status the
CLI ended with is reported as "The Codex CLI exited during startup (exit
status N). Its executable may lack app-server support, or a wrapper script
failed to start its tools." — both causes named, because that exit is
equally the signature of an old release and of a wrapper whose tool is
missing. A stream that died under a still-running process (no status to
learn from; AI Monitor's own kill included, since a signal is not the CLI's
doing) keeps the authored incompatible-CLI message, as does a genuine
method-not-found rejection. `TestOutcome.CLIIncompatible` is therefore true
only for those two cases; the observed-exit case reports
`CLIState: found` with the new `source.Error`, and the settings test report
names the launcher stage ("Codex CLI: failed to start") for it. Exit
statuses are safe to show and stderr still is not. `docs/S6_CODEX.md`
§Errors and timeouts already listed "EOF/child exit" as its own failure
class, so the specification stands and only the implementation follows it
now.

## 2026-08-30 — Version identity from buildinfo; FyneApp.toml removed

The application version is now link-time build information instead of a
hand-edited pair of files. `internal/buildinfo` declares `AppName`,
`Version` (default `"dev"`) and `BuildDate`; GoReleaser stamps
`Version = {{ .Version }}` and `BuildDate = {{ .Date }}` via `-X`
ldflags on tagged releases, exactly like the author's other Go
applications. `appMetadata()` reads those values, so the startup log,
the systray title source and the Codex `clientInfo.version` all report
the tag on release builds and `"dev"` otherwise. `BuildDate` also joins
the startup log line (empty for development builds).

`FyneApp.toml` is deleted. Nothing in this repository's build or
runtime path consumed it: Fyne only reads the toml when the app is
created without an ID and the file sits next to the executable, while
AI Monitor uses `NewWithID(appID)` and then overwrites the metadata
with `SetMetadata` in every run mode; the `fyne` packaging tool is not
part of the build (GoReleaser produces plain binaries; the desktop
entry and icon are installed manually per `packaging/README.md`). The
`Build` integer leaves `appMetadata()` with it: fyne 2.8 never reads
`AppMetadata.Build` at runtime, and its only consumer would have been
the unused packaging tool.

This supersedes the 2026-08-23 entry "App metadata set at startup"
from this date onward: the metadata is still set at startup, but the
mirroring of `FyneApp.toml` and its guard test are gone, and a release
no longer needs a version-bump commit — tagging
(`git tag 0.7.0 && git push --tags`) is the whole release step. A test
now guards the opposite direction: `appMetadata()` must carry
`buildinfo`'s name and version rather than hardcode them.

## 2026-08-30 — The Codex plan name joins the overview card title; the plan line is gone

First real use of the S6 overview card showed the plan line ("Plus" above
the donuts) breaking the shape every other usage-based provider shares:
one titled card with nothing between the title and its gauges. The plan
name now joins the card title — "ChatGPT Codex (Plus)" — and the line is
removed. This supersedes the `QuotaOverviewVM.Subtitle` half of item 5 of
the same-day S6 entry, from this date onward:

1. `QuotaOverviewVM.Subtitle` is removed, together with its renderer
   branch; the overview body is again only the headline donuts, identical
   in shape for MiniMax, Z.AI and Codex.
2. `BuildOverviewCard` appends the plan in parentheses to the card title
   when the landed snapshot carries one (labeled by `CodexPlanLabel` as
   before, unknown names included verbatim, absent plan or no snapshot yet
   leaves the title bare). The detail tab is unchanged: its title stays
   the source name and the Account section keeps the Plan row.

## 2026-08-30 — Short Codex editor labels; the editor dialog is sized to the window

First real use of the S6 editor showed the form's label column eating the
dialog: a Fyne form sizes its label column to the widest label, and the
modal dialog sizes itself to its content's minimum, so "Environment
overrides (advanced)" and "Codex executable" starved the field column and
wrapped the help paragraphs into a tall narrow strip. Two changes, from
this date onward:

1. The labels are shortened ("Executable", "ENV"), narrowing the label
   column for every row. Only these two labels differ from the
   `docs/S6_UI_UX.md` §Settings wireframe; behavior is unchanged.
2. The editor dialog is explicitly resized to 90% of the Settings
   window's size (never below a 480pt width on a small parent) instead of
   taking its content's minimum — with short labels that minimum
   collapses to a thin strip. Flooring the *form's* width inside a
   natural-size dialog was tried first and rejected: the widened dialog
   simply filled the whole window anyway, so the size is now chosen
   deliberately at the dialog level. `dialog.Resize` clamps to the
   dialog's own minimum, so tall kinds still grow beyond their 90%
   allotment when their content needs it.
3. The form scrolls inside the dialog (`container.NewVScroll(form)`),
   with the separator and the Save/Cancel row pinned below it: the Codex
   editor is taller than the 90% allotment, and its Test connection row —
   the form's last one — was out of reach without resizing the parent.
   The scroll viewport takes the dialog's whole remaining height (a
   Border layout pins the action row); Test connection is reached by
   scrolling the form, like any other row.

## 2026-08-30 — S6 review hardening: launcher exec, teardown, and error surfaces

Post-implementation review of the Codex integration surfaced seven defects;
all were confirmed and fixed. These choices amend decisions 2 and 8 of the
same date.

1. **The launcher is exec'd exactly as resolved.** A PATH override entry
   like `.` loses its prefix in `filepath.Join`'s cleaning, leaving a bare
   name that `exec.Command` would look up a second time — against the
   inherited PATH instead of the effective one discovery just searched.
   Discovery returns an absolute path for bare-name resolutions and
   `spawn` builds the `exec.Cmd` with `Path`/`Args` set directly, as
   `docs/S6_CODEX.md` §Environment already prescribed.

2. **JSON-RPC error messages are never rendered.** `rpcError.Error()` is
   code-only ("json-rpc error -32000"); the provider's message drives the
   auth classification alone and can carry account data into neither the
   manager's log line nor a UI string. An unsupported id spelling is
   reported without echoing the raw bytes.

3. **The process group is signaled after a graceful reap too.** A launcher
   wrapper that exits right after spawning the app-server leaves the
   leader already reaped when stdin is closed, so the graceful path never
   signaled the group and a grandchild ignoring stdin EOF survived.
   `shutdown` signals the group unconditionally once the wait completes
   (a no-op on an empty group), making the teardown guarantee
   unconditional (amends decision 8).

4. **A caller cancellation preserves the persistent child.** The manager
   detaches the loop on every reconfiguration and cancels the in-flight
   request with it, an interval-only change included; treating that
   cancellation as a broken stream killed and respawned a healthy child on
   a pure schedule change. The adapter now tears the connection down on
   timeouts, transport and protocol failures only (amends decision 2 in
   spirit — the schedule-change guarantee now holds at process level).

5. **A pre-initialize exit is incompatible setup.** A CLI that exits
   before answering the handshake (an old release without the app-server
   command) produced "Codex CLI: OK" plus a connection error; a
   transport-shaped failure during initialize now yields the same authored
   incompatible-CLI message as a method-not-found rejection. Read-stage
   failures keep the connection classification.

6. **`source.CatBackend` for non-HTTP rejections.** A JSON-RPC error is
   not an HTTP 5xx, but `CatProvider`'s fixed message says exactly that.
   The new category renders "Provider rejected the request"; `CatProvider`
   keeps the HTTP wording for the HTTP-based kinds.

7. **Shared adapters are disposed once.** One `Closer` pointer registered
   under two instance IDs was closed once per stopped entry (and collected
   twice by `Wait`). Both close phases deduplicate by pointer; Close
   remains contractually idempotent.

## 2026-08-30 — S6: ChatGPT Codex source, implementation-time decisions

Implementation of S6 / v0.6.0 per `docs/S6_CODEX.md`, `docs/S6_AGENT_RULES.md`,
`docs/S6_UI_UX.md`, and `docs/S6_IMPLEMENTATION_SPEC.md`. The following
choices were made while coding and extend the documents from this date.

1. **Request timeout 30s with a 2s reap grace.** One Codex refresh spans a
   process spawn, the JSON-RPC initialize, the live
   `account/rateLimits/read`, and the reap of the process, budgeted as
   10s initialize + 15s read + 2s grace + margin. The remote convention of
   10s cannot cover it, so `KindCodex` carries its own `RequestTimeout`.
   Consequently `source.Manager` documentation now states that `Apply` may
   block for up to one in-flight request timeout plus a `Closer`'s
   self-bounded cleanup; per-request timeouts are per-kind values, and
   Codex is the first kind that both spawns a subprocess and can own a
   long-lived child.

2. **`source.Closer` lifecycle.** A stopped loop's source is closed when no
   registration of the same `Apply` keeps that pointer enabled — removal,
   adapter replacement, and disable all discard the source (a disabled
   instance stays registered with `Enabled: false` and has no loop, so a
   later shutdown could no longer reach it). An interval-only restart keeps
   the source, so a persistent app-server survives a schedule change. Close
   is idempotent, bounded, and restartable: the next `Refresh` lazily starts
   a fresh process, which is what disable-then-re-enable relies on.
   `Cancel` stays non-blocking; `Wait` closes surviving sources after the
   loops join, guarded by a `sync.Once`; Apply-after-Wait is unsupported.

3. **The Settings caller owns the Codex test budget (~35s).** A child
   context can never outlive its parent's deadline, so the editor's Codex
   branch derives its test context with `codexTestTimeout` (35s) instead of
   the shared 10s `testTimeout`; `codex.Test` fully honors the context it
   receives and its internal stage timeouts only shorten it. The shared 10s
   budget is unchanged for every other kind.

4. **`Deps.AppVersion`.** The registry hands the running application
   version to `Build`/`Test`; the Codex adapter reports it as the JSON-RPC
   `clientInfo.version` during initialize. Other kinds ignore it.

5. **Generic, optional quota-view extensions.** `QuotaVM.Account`
   ([]MetricRowVM) and `QuotaOverviewVM.Subtitle` extend the shared quota
   presentation for the Codex Account section (Plan, Reset credits, an
   optional reached-limit warning) and the overview plan line. Both stay
   empty for MiniMax/Z.AI, whose rendering is unchanged (regression-tested).
   Codex window IDs (`codex-primary`/`codex-secondary`) join the short-label
   and headline-priority tables; plan names render Plus/Pro/Go/Free where
   known via `registry.CodexPlanLabel`, with unknown names passed through
   unchanged. None of it is affected by the money-privacy setting.

6. **No web UI for Codex.** The definition carries an explicit
   `noWebURL` (the registry never leaves `WebURL` nil) and no `WebLabel`:
   the ChatGPT subscription has no quota dashboard AI Monitor could link.

7. **Environment overrides replace, not append.** `effectiveEnv` replaces
   every inherited entry of the same name instead of appending the
   override: environment readers take the first match, so an appended
   override would silently lose to the inherited value — including the
   `PATH` that bare launcher names resolve against.

8. **Process teardown details.** The child is spawned in its own process
   group (`Setpgid`); force-kill signals the group, so a wrapper's
   grandchild cannot outlive a disable or quit. Raw `os.Pipe` triples
   replace `StdoutPipe`/`StdinPipe`: `cmd.Wait` is called exactly once, by
   a single reaper goroutine started at spawn, and the parent's pipe ends
   are closed after the wait so a launcher grandchild holding the write end
   cannot wedge the reader. The child's stderr is drained to EOF for its
   whole lifetime (a stalled reader would fill the pipe and deadlock the
   child), and only its byte count is ever logged — content, environment
   values, and account payloads never reach errors or logs.




## 2026-08-29 — S5: a rate row renders only for a counter family the deployment has exported

The S5 wireframe gives the Activity block a fixed seven rows, "—" marking a
rate that is not derivable (S5_UI_UX). A real deployment broke the
assumption that every family eventually appears: recall served synchronously
is never recorded in the operations counter family at all — the
`operation="recall"` series does not exist in its `/metrics` — so the Recall
row rendered as a permanent dash that reads as a fault. The same
first-activity materialization means any family can be genuinely absent on
one deployment and present on another.

From this date onward the adapter latches, per activity counter family,
whether any successful scrape has exposed its series. The latch is sticky
for the adapter's lifetime — a seen family stays seen when a later scrape
lacks the series again (an upstream restart drops counters until first
activity), so a row degrades to "—" instead of vanishing; rows can appear
when a family first materializes but never disappear mid-session. The
snapshot carries the latch, and the UI omits a never-seen family's row both
in the detail Activity block and in the overview's headline rate pair — a
block whose families were all never exported is omitted entirely. "—" keeps
one meaning: the family is exported but the interval's rate is not derivable
(first sample, counter reset, missing side).

The HTTP 5xx row keeps its own rule: a missing 5xx series means "no errors
observed", not "not exported", so it never participates in the latch. The
Queue and Runtime sections' render-only-what-was-observed behavior is
unchanged; this entry brings the Activity block in line with them.


## 2026-08-29 — S5: accept the OTel spellings of the deviating metric families

The family names in S5_HINDSIGHT §4 come from the upstream snapshot
researched at commit dc1664d0 (OpenAPI 0.9.2). A real OTel-based
deployment renames three of them with an extra segment, so their series
never reached the aggregates and the corresponding rows stayed
permanently unavailable while the rest of the card worked:

| documented name | observed OTel name |
| --- | --- |
| `hindsight_operation_total` | `hindsight_operation_operations_total` |
| `hindsight_llm_tokens_{input,output}_total` | `hindsight_llm_tokens_{input,output}_tokens_total` |
| `hindsight_http_requests_in_progress` | `hindsight_http_requests_in_progress_requests` |

The adapter accepts both spellings: both are in the closed selected set
and both accumulate into the same counter or gauge, so a deployment
exposing each is summed. Classification labels (`operation`,
`status_class`, `status`) are unchanged — the observed deployment keeps
them. Derivation, rate, and availability rules are otherwise untouched.

## 2026-08-29 — S5: a bank list without pagination metadata is one complete page

S5 models the bank list as always paginated by its own `total`
(S5_HINDSIGHT §8, S5_IMPLEMENTATION_SPEC §6, from the upstream response
model researched at commit dc1664d0 / OpenAPI 0.9.2). A real deployment
returns `{"banks":[...]}` with no `total` at all — and ignores `limit`
and `offset` — so the adapter read `total` as its zero value and reported
"Banks 0" while the fact aggregate still summed correctly, and any walk
past the first page would have re-read the same rows and double-counted
their facts.

From this date: `total` is optional. Present, it stays authoritative and
the walk covers it exactly as specified; absent, the returned rows are
the complete list — the count is the row count and the walk stops after
that one page. A negative `total` remains invalid. The spec's
request-budget and "never trust the echoed limit" rules are unchanged.

## 2026-08-29 — S5: the /health/live liveness probe degrades instead of failing, and reports on its own line

The S5 spec makes the liveness fetch a required step of the slow path
(S5_IMPLEMENTATION_SPEC §5) and suggests a three-line test report folding
liveness into the Health line (S5_UI_UX §2). Real deployments break both
assumptions in a specific way: a reverse proxy with an exact-match
`Path(/health)` rule routes only `/health` to the API and strands
`/health/live` on the web UI, whose HTML answer fails JSON parsing. Under
the strict reading one cosmetic endpoint then blanks the entire source —
every Refresh errored, so no snapshot ever published and the deployment
read as Unavailable everywhere — while the test report blamed `/health`,
the one surface that worked.

Two changes, from that date onward:

1. Refresh downgrades the liveness fetch to best-effort. A failed
   `/health/live` logs a warning and publishes the snapshot without
   version/uptime (the UI already omits absent rows); the bank walk stays
   required, and the slow cache still commits only on overall success.
   The warning is edge-triggered: it fires when liveness transitions into
   the degraded state (first failure, or a success followed by a failure)
   and while the failure persists it repeats at debug level only — a
   permanently misrouted `/health/live` would otherwise warn on every
   ~60s slow cycle.
2. The Settings report renders four lines — Health, Liveness, Metrics,
   Banks — so the stranded endpoint is named instead of masquerading as a
   `/health` failure. `TestOutcome.OK` still requires all four probes.

The spec's request budget, cache atomicity, and "suggested" report shape
are otherwise unchanged.

## 2026-08-29 — S5: Hindsight gets its own poll-interval ladder and a load-time nearest-preset migration

Poll presets stop being a per-category property and become a per-kind one.
`config.KindIntervals` answers `domain.KindHindsight` first and only then
falls through to the category lists, and the Hindsight registry `Definition`
carries its own values. `docs/S5_UI_UX.md` §1 and
`docs/S5_IMPLEMENTATION_SPEC.md` §1 still say "existing Local presets /
Local polling preset architecture"; that historical wording is not
rewritten, this entry supersedes it.

1. The new ladder is `10s, 15s, 30s, 1m, 5m` with a `10s`
   (`config.DefaultHindsightInterval`) default, replacing the inherited
   local `5s … 1m` and its 5s default. Hindsight is a memory-service
   aggregate, not telemetry: its values move only when a memory bank or the
   process does, so a 5s poll spends requests on answers that cannot change
   and every Hindsight refresh already chains several endpoints. The other
   local kinds keep the unchanged local ladder and 5s default.

2. Load-time migration for stored intervals that the current list no longer
   offers (in practice a Hindsight instance saved with the old 5s default,
   which `ValidateInstances` would otherwise reject and `LoadInstances`
   would turn into a failed load — an empty source list at startup).
   `recordToInstance` — the one place every loaded record passes through —
   snaps an off-list value to the nearest offered preset, with an exact tie
   resolved toward the longer interval (never silently speed a source up);
   values below the ladder and corrupt/absurd ones snap to the boundary
   presets. Normalization is in memory only: a load never rewrites the
   stored document, and `SaveInstances` still refuses off-list values, so
   the snapped value reaches storage only through an explicit save.

## 2026-08-29 — Wayland taskbar icon: GLFW WaylandAppID hint plus a packaged desktop entry

The monitor window showed up in the Plasma taskbar as an empty entry.
Wayland has no per-window icon: the compositor resolves a toplevel's
taskbar entry from the xdg-toplevel `app_id`, matched against a `.desktop`
file and its `Icon=`. Two Fyne 2.8 gaps break that on this platform.

1. Fyne's glfw driver never sets a Wayland app-id, so GLFW's `wl.appId`
   stays empty and `xdg_toplevel_set_app_id` is never sent; Plasma cannot
   match the window to any desktop entry.
2. `Window.SetIcon` returns early on Wayland, so neither the app-level
   nor the window-level icon reaches the compositor.

`App.New` therefore sets `glfw.WindowHintString(glfw.WaylandAppID, appID)`
directly, and `packaging/` ships the matching
`com.skobkin.aimonitor.desktop` plus install instructions (the entry and
the icon under `~/.local/share` are user-installed; nothing is
auto-installed from Go code, and the repository keeps a single embedded
`internal/resources/icon.svg` as the icon source).

This adds a direct dependency on `github.com/go-gl/glfw/v3.4/glfw`
(previously indirect, and already the GLFW build Fyne links) and reaches
past the Fyne API for a window-hint call. The call site is constrained:
GLFW string hints assert that `glfw.Init()` has run, and Fyne initializes
GLFW lazily inside the first window construction — so the hint is set
after the monitor and settings windows are built and before any `Show()`
creates the first GLFW window. `New` runs on the main goroutine, which is
the thread GLFW requires. Fyne never calls `DefaultWindowHints` nor resets
string hints, so every window created afterwards inherits the app-id. The
hint is silently unused on X11 and the other platforms, where the existing
icon paths keep working. Window code is unchanged.


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
