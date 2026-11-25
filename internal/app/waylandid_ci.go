//go:build ci

package app

// setWaylandAppID is a no-op under the ci build tag: Fyne then runs its
// headless driver, there is no GLFW (and no cgo), and so no app-id to set.
func setWaylandAppID(string) {}
