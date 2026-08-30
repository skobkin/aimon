//go:build !unix

package codex

import (
	"os"
	"syscall"
)

// setProcessGroup has no portable non-Unix equivalent; the child is killed
// directly instead of as a group.
func setProcessGroup(attr *syscall.SysProcAttr) {}

// killProcess force-terminates the child leader.
func killProcess(p *os.Process) error {
	return p.Kill()
}
