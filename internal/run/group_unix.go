//go:build unix

package run

import (
	"os/exec"
	"syscall"
)

// inOwnGroup starts cmd as the leader of a new process group and makes its cancel kill the whole group.
func inOwnGroup(cmd *exec.Cmd) (func() error, func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return func() error { return nil }, func() {}, nil
}
