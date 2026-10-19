//go:build unix

package main

import (
	"errors"
	"os/exec"
	"syscall"
)

// pidAlive says whether process pid runs.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return pid > 0 && (err == nil || errors.Is(err, syscall.EPERM))
}

// detach starts cmd in a session of its own.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
