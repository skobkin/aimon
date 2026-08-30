//go:build unix

package codex

import (
	"os"
	"syscall"
)

// setProcessGroup puts the child into its own process group so a force kill
// can take down a launcher wrapper together with what it spawned. A wrapper
// (for example a graftcp/proxychains front-end) may exec the real CLI or
// leave grandchildren holding the pipes; killing only the leader would leave
// the group running.
func setProcessGroup(attr *syscall.SysProcAttr) {
	attr.Setpgid = true
}

// killProcess force-terminates the child's whole process group. Killing an
// already-exited group is not an error here: the caller treats any kill
// failure as a log-only detail, never as the refresh's error.
func killProcess(p *os.Process) error {
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}
