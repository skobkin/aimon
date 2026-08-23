# Decision Log

Dated record of implementation-time decisions that change or extend the
authoritative documents (`AGENTS.md`, `docs/DECISIONS.md`,
`docs/S1_IMPLEMENTATION_SPEC.md`, `docs/ARCHITECTURE.md`, `docs/UI_UX.md`,
`docs/INTEGRATIONS.md`).

The authoritative documents are not rewritten retroactively. An entry here
supersedes the affected document from its date onward. Newest entries
first.

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
