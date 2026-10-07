//go:build unix

package run

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// ownGroup starts cmd as the leader of a new process group and makes its cancel kill the whole group and every
// descendant, even one in a session or group of its own.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killTree(cmd.Process.Pid) }
}

// killTree kills the group led by pid and every descendant of pid. It stops them first, so none forks away or
// is reparented while ps walks the tree.
// ponytail: a descendant already orphaned to init before the kill escapes; a cgroup or subreaper would catch it.
func killTree(pid int) error {
	tree := map[int]bool{pid: true}
	_ = syscall.Kill(-pid, syscall.SIGSTOP)
	for grew := true; grew; {
		grew = false
		out, _ := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
		for line := range strings.Lines(string(out)) {
			var p, parent int
			if _, err := fmt.Sscan(line, &p, &parent); err == nil && tree[parent] && !tree[p] {
				tree[p], grew = true, true
				_ = syscall.Kill(p, syscall.SIGSTOP)
			}
		}
	}
	for p := range tree {
		if p != pid {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
	}
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// detach starts cmd in a session of its own, so the terminal that started it can close.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
