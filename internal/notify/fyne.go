//go:build !linux && !freebsd && !netbsd && !openbsd

package notify

import (
	"errors"

	"fyne.io/fyne/v2"
)

// Fyne posts through the running Fyne app's SendNotification — the
// default everywhere Fyne's own desktop integration behaves correctly
// (auto-hiding included).
type Fyne struct{}

// newPlatform returns the Fyne-backed sender. It fails only when no Fyne
// app has been created yet.
func newPlatform(string) (Notifier, error) {
	if fyne.CurrentApp() == nil {
		return nil, errors.New("notify: no running Fyne app")
	}

	return Fyne{}, nil
}

// Notify implements Notifier.
func (Fyne) Notify(summary, body string) error {
	fyne.CurrentApp().SendNotification(fyne.NewNotification(summary, body))

	return nil
}
