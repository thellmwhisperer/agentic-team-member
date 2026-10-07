//go:build windows

package run

import (
	"os/exec"
	"strconv"
	"syscall"
)

// ownGroup makes cancellation terminate cmd and its child processes.
func ownGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}

// detach starts cmd without a console and in a group of its own, so the terminal that started it can close.
func detach(cmd *exec.Cmd) {
	const detachedProcess = 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess}
}

// groupAlive says whether process pid runs. ponytail: the process alone, not what it started; a job object is
// the upgrade.
func groupAlive(pid int) bool { return alive(pid) }
