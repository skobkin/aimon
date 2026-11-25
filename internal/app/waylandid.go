//go:build !ci

package app

import "github.com/go-gl/glfw/v3.4/glfw"

// setWaylandAppID sets the GLFW Wayland app-id hint.
//
// Wayland has no per-window icon: the compositor draws the taskbar
// entry from the xdg-toplevel app-id, resolved to a .desktop entry and
// its Icon=. Fyne 2.8's glfw driver never sets an app-id (no
// WindowHintString call anywhere in the driver), so GLFW passes an
// empty appId to xdg_toplevel_set_app_id — which it then skips — and
// Plasma cannot match the window to any desktop entry, leaving a blank
// taskbar entry. Window.SetIcon is also a Wayland no-op, so there is no
// fallback there. GLFW only forwards this hint to the created toplevel,
// and it asserts that glfw.Init() has already run: the hint must be set
// after Fyne's lazy init (the first window construction in New triggers
// it) and before the first Show() actually creates the GLFW window.
// Fyne never calls DefaultWindowHints nor resets string hints, so both
// windows New builds afterwards inherit it. The hint is silently ignored on X11
// and the other platforms.
func setWaylandAppID(id string) {
	glfw.WindowHintString(glfw.WaylandAppID, id)
}
