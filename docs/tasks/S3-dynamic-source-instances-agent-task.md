# S3 Agent Task — Dynamic Source Instances

## Mission

Convert AI Monitor from four fixed singleton source configurations to dynamic,
user-managed source instances while preserving the current working S2 monitor
experience.

Read before planning/implementation:

1. current repository `AGENTS.md`;
2. `docs/S3_AGENT_RULES.md`;
3. `docs/S3_DYNAMIC_SOURCE_INSTANCES.md`;
4. `docs/S3_UI_UX.md`;
5. `docs/S3_IMPLEMENTATION_SPEC.md`;
6. existing `docs/DECISION_LOG.md`;
7. current source/tests.

S3 documents explicitly supersede older one-instance-per-kind assumptions.

## Required approach

First inspect the current implementation. Do not assume historical S1/S2 file
paths or types are unchanged. Produce a concrete implementation plan grounded
in current HEAD before coding.

The planned implementation should use focused commits and keep tests green at
reasonable boundaries.

A sensible phase structure is:

1. dynamic config/instance identity + persistence;
2. instance-scoped credential keys;
3. compiled source registry/factory and adapter construction;
4. instance-keyed source manager/runtime state;
5. application wiring and dynamic monitor/tray derivation;
6. Settings CRUD + add/edit/delete/enable/reorder;
7. hardening/regression tests/version metadata.

You may adjust phase boundaries to match current code, but do not collapse the
whole task into one large unreviewable commit.

## Non-negotiable behavior

- multiple instances of the same source kind;
- stable opaque instance IDs;
- user-controlled renameable names;
- default name prefilled on Add;
- duplicate names allowed;
- kind immutable after creation;
- manual Up/Down ordering, no drag/drop;
- no migration from static v0.2 config;
- instance-scoped keychain credentials;
- delete confirmation + best-effort credential cleanup, no trash;
- independent polling/stale state per instance;
- no restart on rename/reorder;
- preserve S2 Overview/Local/Remote visual presentations;
- dynamic Web UIs submenu using instance names;
- no OpenRouter in this task.

## Avoid these implementation traps

- Do not key runtime/credentials by provider kind.
- Do not key anything persistent by user-visible name.
- Do not reintroduce one global struct field per configured source instance.
- Do not build a plugin system.
- Do not build a generic form-schema engine.
- Do not serialize secrets into the dynamic source document.
- Do not make Settings list operations wait synchronously for network shutdown
  or HTTP tests on the Fyne UI goroutine.
- Do not silently discard unknown/malformed config and overwrite it with an
  empty list.
- Do not change current provider parsing/usage semantics merely as part of the
  instance refactor.

## Final verification

Run:

```sh
gofmt -w .
go test ./...
go vet ./...
go build ./...
```

Then manually validate at least:

- two amdgputop-web instances with different endpoints;
- two instances of one credentialed quota provider;
- rename with no poller restart;
- reorder + restart persistence;
- disable one same-kind instance without affecting another;
- delete one same-kind instance without deleting the other's credential;
- dynamic Web UIs entries;
- zero-source startup;
- one source stale while another same-kind source remains healthy.

Report commit list, tests run, any behavior deviations, and any new entries
appended to `docs/DECISION_LOG.md`.
