//go:build linux || freebsd || netbsd || openbsd

package notify

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// DBus is a Notifier bound to the session bus's
// org.freedesktop.Notifications service.
type DBus struct {
	obj     dbus.BusObject
	appName string
}

// newPlatform connects to the session bus and resolves the notifications
// service. The connection stays open for the Notifier's lifetime.
func newPlatform(appName string) (Notifier, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("notify: session bus: %w", err)
	}

	return &DBus{
		obj:     conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications"),
		appName: appName,
	}, nil
}

// Notify posts one notification: no actions, no icon override (the
// desktop entry provides the theme icon), server-default expiry —
// fyne.App.SendNotification hardcodes expire_timeout 0, which the
// freedesktop spec reads as "never expire", so this service is addressed
// directly (Decision Log 2026-11-01).
func (d *DBus) Notify(summary, body string) error {
	return d.obj.Call("org.freedesktop.Notifications.Notify", 0,
		d.appName,
		uint32(0), // replaces_id: no dedup, every message stands alone
		"",        // app_icon: theme icon via the desktop entry
		summary,
		body,
		[]string{},                // actions: none
		map[string]dbus.Variant{}, // hints: none
		int32(-1),                 // expire_timeout: server default
	).Err
}
