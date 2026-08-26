# S3 UI/UX — Dynamic Source Management

## 1. Monitor surfaces

S3 keeps the current S2 visual design. The monitor changes only to support an
arbitrary number of configured source instances.

### Card title

The card title becomes the **instance name**, not the source-kind display name.

Example:

```text
Local

┌ AMD GPU localhost ───────────────── 4s ago ┐
│ GPU ...                                     │
│ VRAM ...                                    │
└─────────────────────────────────────────────┘

┌ AMD GPU server ──────────────────── 2s ago ┐
│ GPU ...                                     │
│ VRAM ...                                    │
└─────────────────────────────────────────────┘
```

The source kind does not need to be repeated in monitor cards. Settings shows
it as secondary information.

## 2. Overview with N instances

Keep the S2 two-column grammar.

Within each section (`Local`, `Remote`):

- 0 enabled instances -> omit the section;
- 1 -> full-width card;
- 2 -> one two-column row;
- 3 -> first two in a two-column row, final card full-width;
- 4 -> two two-column rows;
- continue by pairs for larger counts, with an odd final card full-width.

Preserve the user-configured order within the category.

Do not shrink cards to three/four columns to fit more sources. Vertical
scrolling is expected for larger configurations.

Current MetricsOverview / RuntimeOverview / QuotaOverview bodies stay visually
as implemented after the S2 polishing pass.

## 3. Local / Remote detail tabs

Render all enabled instances for the category as full-width detail cards in the
configured order.

No visual redesign is required. Existing Web UI buttons, quota counts, stale
footer, local metric rows and reset countdown behavior stay intact.

## 4. Settings source manager

Replace the fixed four-source settings layout with a source-instance list.

Suggested structure:

```text
Sources

Local
┌────────────────────────────────────────────────────┐
│ [✓] AMD GPU localhost                              │
│     amdgputop-web                  ↑  ↓  Edit  🗑  │
├────────────────────────────────────────────────────┤
│ [✓] AMD GPU server                                 │
│     amdgputop-web                  ↑  ↓  Edit  🗑  │
└────────────────────────────────────────────────────┘

Remote
┌────────────────────────────────────────────────────┐
│ [✓] MiniMax Personal                               │
│     MiniMax                        ↑  ↓  Edit  🗑  │
├────────────────────────────────────────────────────┤
│ [ ] MiniMax Secondary                              │
│     MiniMax                        ↑  ↓  Edit  🗑  │
└────────────────────────────────────────────────────┘

[ + Add source ]
```

Exact icons are not normative. Use normal Fyne buttons/toolbar icons rather
than custom controls.

### Row behavior

- checkbox/toggle changes Enabled and persists immediately;
- Edit opens source editor;
- Delete asks for confirmation;
- Up/Down moves within the same category and persists immediately;
- Up disabled on first item in category;
- Down disabled on last item in category.

Do not add drag-and-drop in S3.

## 5. Add source flow

Keep the flow simple and deterministic:

1. click `+ Add source`;
2. choose source type from compiled source kinds;
3. open the provider-specific editor for a new instance;
4. Name is already prefilled with that source kind's default display name;
5. user may change it;
6. Save creates the instance; Cancel creates nothing.

Separating type choice from the editor avoids complex "did the user already
edit the default name?" behavior when the source type changes.

Suggested type chooser:

```text
Add source

Type
[ amdgputop-web ▼ ]

[ Cancel ] [ Continue ]
```

Source kind is immutable after creation.

## 6. Source editor

The editor reuses current provider-specific fields and validation.

Common fields:

```text
Name        [ AMD GPU server                    ]
Enabled     [✓]
Refresh     [ 5 seconds                       ▼ ]
```

Then provider-specific fields, for example:

```text
amdgputop-web

URL         [ http://server.example:8080         ]
GPU         [ Radeon ...                       ▼ ]

[ Test connection ]
```

or:

```text
MiniMax

API key     [                              ]
             Credential stored
             [ Remove credential ]

[ Test connection ]
```

### Name validation

- prefilled on Add;
- required non-empty after trim;
- duplicates allowed;
- no automatic suffixes such as `(2)`;
- no name-based credential/config keys.

### Credential behavior

Retain current semantics:

- secret field is empty when opening an existing source;
- label says whether a credential is stored when keychain state allows it;
- empty secret on Save means "leave existing secret unchanged";
- non-empty secret means replace/set;
- explicit Remove removes the credential;
- Test can use the currently typed secret without persisting it;
- no plaintext fallback.

If keychain is unavailable, non-secret source configuration remains editable.
Do not crash or disable the whole Settings window.

## 7. Persistence behavior in Settings

There is no global source-list Save/Cancel transaction in S3.

- Add/Edit editor Save persists that one source.
- Add/Edit editor Cancel discards that editor's changes.
- Delete persists after confirmation.
- Enabled and order controls persist immediately.

This is intentional. It avoids complicated interaction between list CRUD,
keychain writes and a second global Save button.

Network/runtime reconfiguration happens asynchronously after persistence and
must not block the Fyne UI thread.

## 8. Delete confirmation

No trash/undo.

Example:

```text
Delete source?

Delete “MiniMax Secondary”?
Its stored credentials will also be removed.
This cannot be undone.

[ Cancel ] [ Delete ]
```

Credential cleanup is best-effort. If cleanup fails, source deletion remains
committed; report a sanitized warning.

## 9. Manual ordering semantics

Ordering changes presentation only. They must not:

- restart adapters;
- refresh sources;
- alter freshness;
- alter credentials;
- alter instance IDs.

Ordering applies consistently to:

- Overview cards;
- Local/Remote detail cards;
- Settings rows;
- Web UI submenu entries.

## 10. Dynamic Web UIs submenu

Keep the existing tray wording `Web UIs`.

Generate entries from configured instances that have a Web UI capability:

```text
Web UIs >
    AMD GPU localhost
    AMD GPU server
    llama-swap server
    MiniMax Personal
    Z.AI / GLM
```

Use user-defined names. Include disabled configured instances as well; disabling
monitor polling should not remove a useful navigation shortcut.

## 11. Empty state

With zero configured sources, the app remains tray-resident and functional.
Overview shows the existing/appropriate empty state with a Settings action.
Local and Remote tabs show their category empty states.

This will be the expected first startup after S3 because v0.2 fixed-source
configuration is intentionally not migrated.
