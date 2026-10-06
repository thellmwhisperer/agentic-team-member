//go:build windows

package run

import (
	"os/exec"
	"strconv"
)

// ownGroup makes cancellation terminate cmd and its child processes.
func ownGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
