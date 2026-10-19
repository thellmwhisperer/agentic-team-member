//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ownSession puts the fake in a session of its own when $FAKE_AGENT_SETSID is set, before it plays its mode:
// macOS has no setsid command.
func ownSession() {
	if os.Getenv("FAKE_AGENT_SETSID") != "" {
		_, _ = syscall.Setsid()
	}
}

// pidAlive says whether process pid runs.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return pid > 0 && (err == nil || errors.Is(err, syscall.EPERM))
}

// detach starts cmd in a session of its own.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
