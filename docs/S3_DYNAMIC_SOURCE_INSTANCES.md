# S3 Architecture — Dynamic Source Instances

## 1. Goal

The current application has four compiled source adapters and historically
configured at most one instance of each. S3 keeps the compiled adapters but
changes configuration/runtime identity so the user can create any reasonable
number of instances of a source kind.

Primary motivating cases:

```text
Local
  AMD GPU localhost    -> amdgputop-web
  AMD GPU server       -> amdgputop-web

Remote
  MiniMax Personal     -> MiniMax
  MiniMax Secondary    -> MiniMax
  Z.AI / GLM           -> Z.AI
```

This stage deliberately stops before OpenRouter. The dynamic-instance model is
expected to make OpenRouter easier to add later, but S3 must not over-generalize
around hypothetical providers.

## 2. Vocabulary

### Source kind

A compile-time adapter/product kind, such as:

- `amdgputop-web`;
- `llama-swap`;
- `minimax`;
- `zai`.

The current `domain.SourceID` may continue to represent this concept in S3 if a
broad rename would create unnecessary churn. If it is renamed to `SourceKind`,
do so consistently. The important requirement is semantic: kind is **not** a
unique configured source identity anymore.

### Source instance

One user-created configuration of a source kind.

A source instance has at least:

```go
type SourceInstance struct {
    ID           InstanceID
    Kind         SourceKind // or the existing SourceID used as kind
    Name         string
    Enabled      bool
    PollInterval time.Duration
    // provider-specific non-secret settings
}
```

Provider-specific settings remain typed. Do not put arbitrary `map[string]any`
through the whole application merely to make the config look generic.

### Instance ID

Stable opaque identity. It is the key for runtime state and credentials.

### Display name

User-controlled label shown in the monitor, Settings and Web UI navigation.
Names may collide and never form identity.

## 3. Persistence model

### One ordered dynamic source document

Replace the fixed per-source preference schema with one serialized dynamic
source configuration document stored in Fyne Preferences.

Recommended shape:

```json
{
  "schema_version": 1,
  "sources": [
    {
      "id": "opaque-id",
      "kind": "amdgputop-web",
      "name": "AMD GPU localhost",
      "enabled": true,
      "poll_interval": "5s",
      "amdgputop_web": {
        "base_url": "http://127.0.0.1:8080",
        "gpu_id": ""
      }
    }
  ]
}
```

The exact JSON field spelling is not normative. The invariants are:

- schema version is explicit;
- sources are an ordered list;
- secrets are absent;
- each source has exactly one provider-specific configuration matching `Kind`;
- source IDs are unique;
- names are non-empty after trimming;
- duplicate names are valid;
- polling interval still uses supported presets/minimums;
- unknown/malformed kinds/settings fail validation rather than being silently
  converted into another source.

A typed tagged-union config is preferable to globally untyped maps. One viable
shape is pointer fields for provider config with validation that exactly the
matching field is present.

### Ordering invariant

The UI has fixed `Local` and `Remote` sections. Persisted relative order within
each category is authoritative.

Implementation may keep a single ordered slice. If convenient, normalize its
physical order to all Local instances followed by all Remote instances. The
user-visible contract only requires stable order *within* each category.

Adding a source appends to the category. Move up/down swaps with the adjacent
instance in the same category.

### No migration

If the new dynamic document is missing, start with zero configured sources.
Do not synthesize instances from old fixed preference keys.

Old preference keys are ignored. Do not delete them automatically.

## 4. Provider definition / factory registry

There should be one central source of truth for the compiled source kinds.
Avoid four unrelated hard-coded lists in Settings, app wiring, tray navigation
and validation.

The exact package/API is left to the implementation, but the central registry
or factory metadata should be able to answer at least:

- kind identifier;
- category (`Local` / `Remote`);
- default display name;
- default polling interval;
- provider-specific default non-secret config;
- how to validate/build the adapter;
- known credential slot IDs for cleanup/scoping;
- whether/how to derive a provider Web UI URL from an instance.

Conceptually:

```go
type Definition struct {
    Kind            SourceKind
    Category        Category
    DefaultName     string
    DefaultInterval time.Duration
    // builder / validation / WebURL / credential slot hooks
}
```

This is a **compiled registry**, not a plugin API.

Do not build a schema-driven generic Settings form system in S3. Explicit
provider editor builders/switches are acceptable and simpler. The registry
exists to centralize source-kind metadata and construction, not to create a
framework.

## 5. Runtime source manager

The manager must move from kind-keyed identity to instance-keyed identity.

Conceptually:

```go
type InstanceID string

type Registration struct {
    InstanceID InstanceID
    Kind       SourceKind
    Source     Source
    Enabled    bool
    Interval   time.Duration
}

func (m *Manager) States() map[InstanceID]domain.Runtime
```

The adapter may still report its kind, but `Source.ID()`/kind must not be used
as the manager map key.

### Required independence

Two instances of the same adapter kind must:

- have separate poll loops;
- refresh concurrently when appropriate;
- never suppress/coalesce each other's refreshes;
- retain separate last-good snapshots/errors/freshness;
- use separate credentials/config;
- be removable/reconfigurable independently.

The existing rule still applies per **instance**: never overlap two refreshes
of the same instance.

### Apply/diff semantics

Configuration changes should be diffed by stable instance ID.

- add enabled instance -> create/start and refresh immediately;
- remove instance -> cancel only that instance and remove its runtime state;
- disable -> stop only that poller; monitor UI omits it as today;
- re-enable -> start and refresh immediately;
- rename only -> update presentation metadata without a network restart;
- reorder only -> no network restart;
- polling interval change -> update/restart only that instance's scheduling,
  preserving valid last-good data;
- adapter-affecting settings change (URL/GPU/etc.) -> rebuild/restart only that
  instance;
- credential change -> refresh only that instance.

When adapter identity/config is materially changed (for example changing an
endpoint to another machine), it is safer to clear the old last-good snapshot
for that instance than briefly present old-host data as if it came from the new
configuration. Do not clear snapshots for rename/order/interval-only changes.

All cancellation/waiting remains off the Fyne UI goroutine.

## 6. Credentials

### Instance-scoped account keys

Keychain keys must incorporate stable instance ID and credential slot, not
source kind or display name.

Conceptually:

```text
service: aimonitor
account: source/<instance-id>/api-key
```

A centralized helper should construct/validate these account IDs.

Current slot needs:

| Kind | Slots |
| --- | --- |
| amdgputop-web | none |
| llama-swap | optional `api-key` |
| MiniMax | `api-key` |
| Z.AI / GLM | `api-key` |

Do not create a full credential-schema UI framework merely because a later
OpenRouter implementation may have two keys. The slot concept is enough for S3.

### Delete semantics

On confirmed source deletion:

1. remove/persist source configuration;
2. stop/remove runtime instance;
3. best-effort delete all known credential slots for that instance;
4. if keychain cleanup fails, keep the source deleted and show/log a sanitized
   warning.

No trash and no undo.

### Rename/reorder

Must never touch keychain entries because instance ID is unchanged.

## 7. Application wiring

The application should construct registrations from the ordered persisted
source instances through the compiled source factory/registry.

Do not keep static fields such as one global `MiniMax`, one global `ZAI`, etc.
as the authoritative application configuration.

It is acceptable for adapter packages themselves to remain unchanged except
for accepting instance-scoped credentials/config in constructors.

### Manual refresh

`Refresh` refreshes all enabled instances. A failure/timeout in one instance
must not block or alter the others.

### Web UI navigation

Build the `Web UIs` submenu dynamically from configured instances that expose a
Web UI URL.

Recommended S3 behavior:

- include configured instances even when monitoring is disabled; Web UI
  navigation remains useful independently of polling;
- label each menu entry with the user-defined instance name;
- preserve configured category/order;
- no duplicate-name disambiguation magic is required in S3 because duplicate
  names are intentionally allowed.

## 8. Presentation/view-model inputs

The S2 presentation kinds remain intact:

- MetricsOverview;
- RuntimeOverview;
- QuotaOverview.

They are not provider identities.

View builders now consume ordered configured instances plus runtime states
keyed by instance ID. Every card title is the instance's user-defined `Name`.
The kind determines category/presentation and provider-specific data mapping.

For example, two amdgputop-web instances both render MetricsOverview cards but
with different titles and independent values/freshness.

## 9. Future extension boundary

S3 should leave a clean insertion point for a future compiled source kind such
as OpenRouter, including more than one credential slot, but must not implement
or speculate on OpenRouter API behavior now.

The success criterion is that adding another compiled kind later should not
require returning to the one-static-field-per-provider configuration model.
