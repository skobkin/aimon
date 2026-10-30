//go:build !unix

package app

import "os"

// lockSingleInstance is the no-op guard for platforms without the flock
// implementation: every launch is allowed, nothing is held.
func lockSingleInstance(path string) (*os.File, error) {
	return nil, nil
}
