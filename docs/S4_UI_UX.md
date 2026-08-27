# S4 UI/UX — OpenRouter

S4 adds OpenRouter without redesigning the existing AI Monitor visual language.

## 1. Source editor

OpenRouter is a Remote source kind.

New instance defaults:

```text
Type          OpenRouter
Name          OpenRouter
Enabled       [x]
Refresh every [1m v]
```

No Base URL field is exposed.

Credential area contains two independent password entries:

```text
API key
[ ................................ ]
Credential stored / No credential stored
[ Remove ]

Management key
[ ................................ ]
Credential stored / No credential stored
[ Remove ]
```

Use concise explanatory text:

```text
API key: per-key usage and spending limit
Management key: account credit balance
```

Blank credential input on Edit means "keep stored value", consistent with the
existing credential UX.

Remove affects only that slot.

The source may temporarily exist without credentials. In that state it makes no
network requests and reports a local credential/configuration problem when
enabled.

### Test connection

Test Connection tests only capabilities for which a typed or stored credential
is available.

Example result:

```text
API key: OK
Management key: not configured (skipped)
```

or:

```text
API key: OK
Management key: authentication failed
```

Do not call missing-capability endpoints.

Test Connection never saves typed credentials.

## 2. Global privacy setting

Add to the main Settings window, outside any source editor:

```text
Privacy
[x] Hide sensitive info
```

Default: enabled.

The toggle persists immediately as a global application preference and causes
visible sensitive values to refresh.

It does not affect API requests, source configuration, credentials, or logging.

## 3. Sensitive-value behavior

When privacy is enabled:

```text
Balance              $•••••
Purchased credits    $•••••
Account usage        $•••••
```

Hovering one value reveals that value while the pointer remains over it:

```text
Balance              $73.42
```

Mouse out immediately masks it again.

When privacy is disabled, the real value is always shown.

The implementation should be generic, e.g. a reusable `SensitiveValue` widget
or equivalent. It may use masking instead of a true graphical blur.

Do not mask:

- Today usage
- Week usage
- Month usage
- Total per-key usage
- per-key spending limit

These are the monitoring data the user opened AI Monitor to see.

## 4. Overview — API key only, no limit

A half-width Overview card should remain compact:

```text
┌ OpenRouter · Claude Code ───── 12s ago ┐
│ Today                           $0.42   │
│ Week                            $3.81   │
│ Month                          $12.47   │
└────────────────────────────────────────┘
```

The actual title is the user-controlled source instance name. `OpenRouter ·
Claude Code` is only an illustrative custom name.

## 5. Overview — Management key only

```text
┌ OpenRouter Account ──────────── 12s ago ┐
│ Balance                         $•••••   │
└─────────────────────────────────────────┘
```

Do not add empty Today/Week/Month rows.

## 6. Overview — both credentials, no key limit

```text
┌ OpenRouter ──────────────────── 12s ago ┐
│ Today                           $0.42   │
│ Week                            $3.81   │
│ Month                          $12.47   │
│ Balance                         $•••••   │
└─────────────────────────────────────────┘
```

## 7. Overview — key limit present

Use one compact horizontal limit bar rather than three fake quota donuts.

```text
┌ OpenRouter ──────────────────── 12s ago ┐
│ Today                           $0.42   │
│ Week                            $3.81   │
│ Month                          $12.47   │
│                                        │
│ Monthly limit                50% used  │
│ ███████████░░░░░░░░░░░░░░░░           │
│                                        │
│ Balance                         $•••••   │
└─────────────────────────────────────────┘
```

Reasons for the horizontal bar in S4:

- Overview cards can be half-width in the existing two-column grid;
- there is only one actual key limit;
- it reuses the existing quota visual language and threshold colors;
- it avoids a new OpenRouter-specific donut layout.

No raw `used / limit` amount line in Overview.

If the key has no limit, omit the whole limit block.

## 8. Remote detail — API key data

```text
OpenRouter                              12s ago

Usage
Today                                   $0.42
Week                                    $3.81
Month                                  $12.47
Total                                  $41.08

Monthly limit                          50% used
████████████░░░░░░░░░░░░░░░░
$12.47 / $25.00
```

The limit section appears only when a usable limit exists.

If `limit_reset` is null/unknown, use `Key limit`.

Do not display a reset countdown.

## 9. Remote detail — account data

When Management key data is available, append:

```text
Account
Balance                                 $•••••
Purchased credits                       $•••••
Account usage                           $•••••
```

All account values follow the global privacy setting.

When Management key is not configured, omit the Account section entirely.

## 10. Remote detail — combined example

```text
OpenRouter                              12s ago

Usage
Today                                   $0.42
Week                                    $3.81
Month                                  $12.47
Total                                  $41.08

Monthly limit                          50% used
████████████░░░░░░░░░░░░░░░░
$12.47 / $25.00

Account
Balance                                 $•••••
Purchased credits                       $•••••
Account usage                           $•••••

[ Open OpenRouter activity ]
```

Keep this card consistent with existing source cards; do not introduce a
completely different dashboard style.

## 11. Stale/unavailable behavior

Use existing source state behavior.

If OpenRouter had a previous valid combined snapshot and a configured request
later fails:

- keep last good snapshot;
- mark card stale;
- keep existing source-level failure footer in Remote detail.

Missing optional credential capability is not a stale/error state by itself.
The corresponding section is simply absent.

## 12. Dynamic instances

All S3 behavior applies unchanged:

- multiple OpenRouter instances may coexist;
- names are user-controlled;
- order is user-controlled;
- Overview pairs cards using the existing N-instance layout;
- Remote detail uses configured order;
- tray Web UIs submenu uses instance names.
