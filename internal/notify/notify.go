// Package notify sends desktop notifications. The Linux/BSD builds talk
// to the session bus's org.freedesktop.Notifications service directly —
// fyne.App.SendNotification hardcodes expire_timeout 0 there, which the
// spec reads as "never expire" (Decision Log 2026-11-01) — while every
// other platform keeps the default Fyne API.
package notify

// Notifier posts one desktop notification.
type Notifier interface {
	Notify(summary, body string) error
}

// New picks the platform sender. An error means no usable notification
// backend was found; callers degrade to Nop.
func New(appName string) (Notifier, error) {
	return newPlatform(appName)
}

// Nop discards notifications; it backs the "notifications unavailable"
// fallback so callers need no nil checks.
type Nop struct{}

// Notify implements Notifier, discarding the message.
func (Nop) Notify(string, string) error { return nil }
