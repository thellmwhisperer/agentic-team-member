//go:build unix

package run

import (
	"errors"
	"os/exec"
	"syscall"
)

// ownGroup starts cmd as the leader of a new process group and makes its cancel kill the whole group.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// detach starts cmd in a session of its own, so the terminal that started it can close.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// groupAlive says whether a process of group pgid runs.
func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
