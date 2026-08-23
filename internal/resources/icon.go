// Package resources embeds static application assets. The icon is the
// only asset in S1: tray and window art is fixed, never dynamic.
package resources

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed icon.svg
var iconData []byte

// icon is lazily wrapped so importing the package stays side-effect free.
var icon = &fyne.StaticResource{
	StaticName:    "icon.svg",
	StaticContent: iconData,
}

// Icon returns the application icon resource.
func Icon() fyne.Resource { return icon }
