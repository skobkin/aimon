# Manual packaging

On KDE Plasma (Wayland) the taskbar icon comes from the xdg-toplevel
app-id resolved against a `.desktop` entry, not from the window itself.

```sh
cp packaging/com.skobkin.aimonitor.desktop ~/.local/share/applications/
cp internal/resources/icon.svg \
   ~/.local/share/icons/hicolor/scalable/apps/com.skobkin.aimonitor.svg
update-desktop-database ~/.local/share/applications   # or: kbuildsycoca6
```

`internal/resources/icon.svg` is the single source of truth embedded by
`internal/resources`; no separate icon file is committed here. Restart the
app afterwards so the app-id hint reaches the compositor.
