//go:build unix

package run

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd as the leader of a new process group and makes its cancel kill the whole group.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
