# Decision Log

Dated record of implementation-time decisions that change or extend the
authoritative documents (`AGENTS.md`, `docs/DECISIONS.md`,
`docs/S1_IMPLEMENTATION_SPEC.md`, `docs/ARCHITECTURE.md`, `docs/UI_UX.md`,
`docs/INTEGRATIONS.md`).

The authoritative documents are not rewritten retroactively. An entry here
supersedes the affected document from its date onward. Newest entries
first.

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
