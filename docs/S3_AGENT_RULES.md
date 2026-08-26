# S3 Task Rules — Dynamic Source Instances

These rules supplement the repository `AGENTS.md` for the S3 / v0.3 task.
Where this file explicitly conflicts with an older S1/S2 task restriction,
this file and the S3 specifications win for this task. Existing product-wide
security, graceful-degradation, read-only, Fyne-threading, testing and repository
hygiene rules remain in force.

## Scope

S3 converts the current fixed one-instance-per-source configuration into a
runtime-configured list of **source instances**.

The source kinds remain compiled into the program:

- `amdgputop-web`;
- `llama-swap`;
- `MiniMax`;
- `Z.AI / GLM`.

S3 is **not** a plugin system. Do not add dynamic Go plugins, shared objects,
reflection-driven provider loading, JSON-schema-driven forms, external plugin
manifests, scripting runtimes, or an extension API.

OpenRouter is intentionally deferred to a later task. Do not implement it in
S3 and do not add speculative OpenRouter API code.

## Core identity rule

A source kind and a source instance are different concepts.

- Source kind identifies the compiled adapter implementation.
- Source instance ID identifies one configured runtime source.
- User-visible name is presentation only and is never identity.

Multiple instances of the same kind must be fully supported. For example:

- `AMD GPU localhost` + `AMD GPU server`, both using `amdgputop-web`;
- `MiniMax Personal` + `MiniMax Secondary`, both using the MiniMax adapter.

Do not use source kind or display name as a manager map key, credential key,
configuration key, or other unique identity.

## Source instance IDs

Instance IDs must be:

- generated once when a new source is created;
- stable across rename, reorder, enable/disable and application restarts;
- opaque to the user;
- independent of provider kind and user-visible name;
- sufficiently collision-resistant.

Prefer a small stdlib-only random 128-bit identifier generator (`crypto/rand`)
over adding a UUID dependency solely for this purpose.

## Names

- New source editor is prefilled with the source kind's default display name.
- User may edit it before saving and rename it later.
- Persisted names must be non-empty after trimming.
- Duplicate names are allowed.
- Never silently derive identity from the name.

## Ordering

Manual ordering is part of S3.

- No drag and drop.
- Use explicit move-up / move-down controls.
- Ordering is within the source category (`Local` or `Remote`).
- The order must be stable and persisted.
- The same order drives Overview, Local/Remote detail lists, Settings, and
  dynamic Web UI menu entries where applicable.
- Adding a source appends it to the end of its category.

## Settings transaction model

The Settings source list becomes a CRUD manager rather than one large
all-or-nothing source form.

- Add/Edit uses its own editor and commits on that editor's Save.
- Cancel closes/discards that editor's changes.
- Delete commits after confirmation.
- Enable/disable toggles persist immediately.
- Move-up/move-down persists immediately.
- `Test connection` never saves anything.

Do not build a complex transactional/undo framework for S3.

## Source kind changes

A source instance's kind is immutable after creation. To change kind, delete
and create a new instance. This avoids ambiguous conversion of provider config
and credentials.

## Credentials

Secrets remain in the system keychain only.

Credential identity must be instance-scoped, for example conceptually:

`source/<instance-id>/<slot>`

The exact delimiter/string helper is implementation-defined, but it must be
centralized and tested.

- Rename must not move or duplicate credentials.
- Reorder must not touch credentials.
- Enable/disable must not touch credentials.
- Delete removes the source config and then best-effort removes all known
  credential slots for that instance.
- Credential cleanup failure does not resurrect the deleted source; report/log
  a sanitized warning.
- No plaintext fallback.

Do not migrate old static credential account IDs in S3.

## No S2-to-S3 migration

There is intentionally **no automatic migration** from the fixed v0.2 source
configuration to dynamic instances.

The sole current user accepts resetting/recreating configuration manually.
Avoid migration code and associated complexity in S3.

It is acceptable for old Fyne preference keys and old static keychain entries
to remain unused. Do not delete old keychain entries automatically.

Design the new persisted source document with a schema/version field so a real
migration mechanism can be added later, but do not implement migration now.

## Preserve working S2 behavior

Do not regress the current UI and source semantics:

- tray-first monitor;
- 2-column compact Overview;
- Metrics/Runtime/Quota Overview presentations;
- Local and Remote detail tabs;
- inline quota reset countdowns;
- used-oriented quota bars/donuts;
- colored GPU/VRAM/temp metrics;
- stale last-good snapshots;
- source-local failures;
- manual Refresh;
- Web UI navigation;
- read-only monitoring scope.

The S3 task changes identity/configuration/card multiplicity, not the visual
language established in S2.

## Implementation discipline

- Inspect the current repository before planning exact code touchpoints. The
  repository has advanced beyond the historical S1/S2 documents.
- Prefer focused commits that remain buildable/testable.
- Do not rewrite old specifications retroactively to erase history.
- Record implementation-time decisions that materially alter S3 behavior in
  the existing `docs/DECISION_LOG.md`.
- Before finishing run at least:

```sh
gofmt -w .
go test ./...
go vet ./...
go build ./...
```
