# S3 Implementation Specification — Dynamic Source Instances

## 1. Baseline and target

Implement incrementally on top of the **current repository HEAD after the S2 UI
polishing/fix pass**. Inspect current code before producing an exact code plan;
historical S1/S2 docs do not necessarily describe current line-level details.

Target release: **0.3.0**.

S3 is an architectural/configuration change. Preserve the working S2 monitor UI
and provider parsing unless a change is necessary to support multiple
instances.

## 2. Explicit non-goals

Do not implement in S3:

- OpenRouter;
- third-party provider/plugin loading;
- dynamic Go plugins;
- generic JSON-schema form generation;
- source-kind mutation after instance creation;
- drag-and-drop ordering;
- v0.2 -> v0.3 config migration;
- old static keychain credential migration/cleanup;
- historical charts/storage;
- dynamic tray telemetry;
- model/inference control actions;
- trash/undo for source deletion.

## 3. Functional requirements

### F1 — Dynamic instance configuration

Replace the fixed source config shape with an ordered list of source instances.
Multiple instances with the same kind are valid.

Each instance has stable ID, kind, user name, enabled state, poll interval and
provider-specific non-secret config.

### F2 — Stable instance identity

Generate an opaque random ID for new instances. IDs survive rename/reorder and
are persisted. Reject duplicate IDs on load.

No new third-party UUID dependency is needed merely for this feature.

### F3 — Persisted schema version

Store the dynamic source list as a versioned serialized document in Fyne
Preferences. Secrets are excluded.

If the new document does not exist, load zero sources. Do not read/migrate the
old fixed source keys into instances.

### F4 — Central compiled source registry/factory

Centralize kind metadata/construction enough that Add Source, application
adapter wiring and Web UI capabilities do not each maintain unrelated provider
lists.

The registry remains compile-time and explicit.

### F5 — Instance-keyed runtime manager

Refactor runtime manager identity from source kind to instance ID.

Two MiniMax instances and two amdgputop-web instances must run independently.
A refresh/error/reconfiguration of one cannot mutate the other's state.

### F6 — Instance-scoped credentials

Credential account keys include stable instance ID + credential slot.
Existing security/keychain behavior remains.

No secret appears in serialized source config.

### F7 — Source CRUD

Settings supports:

- Add;
- Edit;
- Rename;
- Enable/disable;
- Delete with confirmation;
- explicit move up/down within category.

Duplicate names allowed. Name required non-empty after trim.

Source kind cannot be changed after creation.

### F8 — Default names

After type selection, a new editor's Name is prefilled with the registered
source default display name. The user may edit it before Save.

Do not auto-generate numeric suffixes.

### F9 — Manual ordering

Ordering is stable, persisted and category-local.

Add appends to category end. Move Up/Down swaps with adjacent source in the
same category.

Order changes must not restart/refresh network sources.

### F10 — Dynamic monitor rendering

All monitor surfaces derive cards from configured instances instead of fixed
source IDs.

- title = instance Name;
- presentation kind remains based on adapter/snapshot semantics;
- disabled omitted from monitor;
- stale/unavailable behavior unchanged;
- Local/Remote grouping based on compiled source kind metadata;
- configured relative order preserved.

### F11 — Overview pair layout

For each category, render cards by pairs using the current two-column S2 layout.
An odd final card uses full width.

### F12 — Dynamic Web UI navigation

Build tray `Web UIs` submenu from configured instances exposing a Web UI.
Use instance names and configured ordering. Include disabled instances.

### F13 — Delete behavior

Delete source after confirmation with no trash.

Delete source config/runtime even if later keychain cleanup fails. Best-effort
remove all known credential slots for the deleted instance and report/log a
sanitized warning on failure.

### F14 — Async reconfiguration

No Settings operation may block Fyne UI on network/cancellation/keychain work
that can take noticeable time.

Fyne mutations from goroutines still use the current `fyne.Do` discipline.

## 4. Suggested domain/config types

Names are guidance, not mandatory.

```go
type InstanceID string

type SourceInstance struct {
    ID           InstanceID
    Kind         domain.SourceID // if SourceID remains the kind type
    Name         string
    Enabled      bool
    PollInterval time.Duration

    AMDGPUWeb *AMDGPUWebConfig
    LlamaSwap *LlamaSwapConfig
    MiniMax   *MiniMaxConfig
    ZAI       *ZAIConfig
}
```

Use a typed union or equivalent. Validate that provider settings correspond to
Kind. Avoid carrying `map[string]any` into adapters/UI.

A separate persistence DTO is acceptable, especially for stable duration JSON
encoding.

## 5. Source factory expectations

The factory/registry should prevent new static singleton wiring from appearing.

At minimum it should support:

```text
Kind -> Category
Kind -> DefaultName
Kind -> DefaultPollInterval
Instance -> adapter construction
Instance -> Web UI URL (when applicable)
Kind -> known credential slots
```

Provider-specific Settings forms may still be explicit code selected by kind.
Do not create a generic schema/form framework.

## 6. Manager behavior details

### Registration identity

Registration and runtime maps use `InstanceID`.

If the existing Source interface still has `ID() domain.SourceID`, treat that as
kind metadata only. Do not use it as uniqueness.

### Diff behavior

Use stable IDs to distinguish:

- added;
- removed;
- enable/disable;
- rename;
- reorder;
- interval change;
- adapter-config change.

Name/order-only updates are presentation/config changes and must not restart a
poller.

### Reconfiguration data safety

If URL/GPU/provider-specific adapter config changes, avoid showing a snapshot
from the old endpoint as current data for the new endpoint. Clear that
instance's snapshot (or otherwise mark it unavailable/loading until the new
adapter succeeds).

Interval-only changes preserve last-good snapshot.

Credential replacement should trigger an immediate refresh for that instance.
It may clear account-specific quota snapshot first if current architecture can
do so cleanly; correctness is preferable to briefly showing the old account's
quota under the same instance.

## 7. Settings implementation contract

### Main source list

Main Settings source list shows each instance with:

- Enabled;
- Name;
- Kind as secondary text;
- move up/down;
- Edit;
- Delete.

Group into Local / Remote.

### Add

Add is two-stage:

1. choose compiled source kind;
2. open editor initialized from that kind defaults.

This makes default-name behavior deterministic.

### Edit

Kind is displayed but immutable. Edit common + provider-specific fields.

### Per-operation persistence

- editor Save persists one source;
- editor Cancel discards;
- enable toggle persists immediately;
- reorder persists immediately;
- delete persists after confirmation.

Do not retain the old one-global-Save semantics for dynamic source CRUD if it
creates a second transactional state layer.

### Tests / connection

Retain current async `Test connection` behavior. It uses the editor's unsaved
field values/typed secret and never persists them.

## 8. Credential contract

Introduce a centralized helper for instance credential account IDs. Tests must
prove:

- two instances of the same kind yield different account IDs;
- rename does not change account ID;
- reorder does not change account ID;
- `api-key` slot IDs are stable;
- invalid/empty instance ID cannot create a valid account key if that case is
  representable by the API.

Adapters receive/use the account ref for their instance, not a static per-kind
account.

## 9. Persistence/error behavior

- Config parse/validation failure must be surfaced; do not silently replace a
  malformed document with empty sources and then overwrite it.
- Unknown schema version must not be silently rewritten.
- Unknown kind must not be silently dropped.
- Save must serialize no credential material.
- Reordering should rewrite only the non-secret config document.

No migration logic is required for schema version 0/static v0.2 keys.

## 10. Test requirements

At minimum cover the following.

### Config/persistence

- zero-source default when dynamic document absent;
- multiple instances of same kind round-trip;
- source list order round-trip;
- duplicate names allowed;
- empty name rejected;
- duplicate instance IDs rejected;
- mismatched kind/provider config rejected;
- unsupported schema version rejected without destructive rewrite;
- serialized JSON contains no known fake secret value.

### ID generation

- generated ID non-empty and valid for credential/config key use;
- injectable/deterministic seam where useful for tests;
- no name/kind dependency.

### Credentials

- two same-kind instances use separate fake-store account IDs;
- rename/reorder do not move credentials;
- delete attempts only the deleted instance's known slots;
- cleanup failure is non-fatal to config deletion.

### Registry/factory

- all four current source kinds registered exactly once;
- correct categories/default names;
- correct known credential slots;
- factory builds two independent adapters for two instances of same kind;
- Web UI URL derived correctly from instance config where dynamic.

### Manager

Using fake sources:

- two same-kind registrations run independently;
- same-instance refresh does not overlap;
- different instances may refresh concurrently;
- failure/stale state isolated;
- removing one cancels only that one;
- disabling one leaves another untouched;
- rename/order change causes no source restart;
- interval change affects only one schedule and preserves snapshot;
- adapter-config replacement affects only one instance and does not leak old
  endpoint snapshot as new data.

### View models

- card title comes from instance Name;
- two same-kind instances both appear;
- order follows config;
- disabled omitted;
- Local/Remote grouping correct;
- S2 presentation kind for each source remains correct;
- stale/unavailable last-good semantics remain.

### Settings/controller logic

Prefer pure controller/model tests where practical:

- Add defaults Name from selected kind;
- rename persists;
- duplicate name accepted;
- move up/down only within category;
- category-edge move disabled/no-op;
- add appends to category;
- source kind immutable on Edit;
- delete removes exactly one instance.

### Tray/Web UI list

- dynamic entries use instance names;
- multiple same-kind links are retained;
- configured order preserved;
- disabled configured instances included;
- source without valid Web UI capability omitted.

## 11. Manual acceptance scenarios

### A — Empty first startup

With no new dynamic config document, app runs with zero sources and Settings can
add the first source.

### B — Two GPUs

Create:

- `AMD GPU localhost` -> amdgputop-web endpoint A;
- `AMD GPU server` -> amdgputop-web endpoint B.

Both refresh independently and display separate metrics/freshness/errors.

### C — Two quota accounts

Create two MiniMax instances with separate fake/real credentials. Both appear
as independent Remote cards and do not share quota state.

### D — Rename

Rename an enabled source. Card/menu text changes without resetting freshness or
network polling and credential remains usable.

### E — Reorder

Move a source up/down. UI order changes immediately without network refresh.
Restart app and verify order persists.

### F — Disable/re-enable

Disable one of two same-kind sources. Only that source disappears/stops.
Re-enable and it refreshes independently.

### G — Delete

Delete one of two same-kind sources. The other remains running and its
credential untouched. Deleted source does not return after restart.

### H — Stale isolation

Make endpoint B fail while endpoint A remains healthy. A stays fresh; B shows
its own stale/unavailable state only.

### I — Web UI submenu

Menu contains user names for all configured Web-capable instances in configured
order, including disabled ones.

## 12. Version / repository hygiene

Target `0.3.0`. Update runtime/Fyne metadata consistently with current
repository conventions; increment build coherently from current HEAD rather
than assuming an outdated number.

Do not expand README unless explicitly requested.

Do not commit real URLs beyond neutral examples such as localhost/example
hostnames, real credentials, account names, machine names, private LAN domains,
or other local-infrastructure details.
