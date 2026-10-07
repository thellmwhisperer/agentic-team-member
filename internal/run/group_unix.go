//go:build unix

package run

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ownGroup starts cmd as the leader of a new process group, marks its environment with a variable of its own,
// which every process it starts inherits, and makes its cancel kill the whole group, every descendant and every
// process that bears the mark, even one in a session of its own that init has adopted.
func ownGroup(cmd *exec.Cmd) {
	mark := "ATM_RUN_MARK=" + rand.Text()
	cmd.Env = append(cmd.Environ(), mark)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killTree(cmd.Process.Pid, mark) }
}

// killTree kills the group led by pid, every descendant of pid and every process whose environment holds mark.
// It stops them first, so none forks away or is reparented while ps walks the tree.
func killTree(pid int, mark string) error {
	tree := map[int]bool{pid: true}
	_ = syscall.Kill(-pid, syscall.SIGSTOP)
	for grew := true; grew; {
		grew = false
		out, _ := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
		marked := bearing(mark)
		for line := range strings.Lines(string(out)) {
			var p, parent int
			if _, err := fmt.Sscan(line, &p, &parent); err == nil && (tree[parent] || marked[p]) && !tree[p] {
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

// bearing is the processes of this user whose environment holds mark: from /proc on Linux, from ps -E elsewhere.
func bearing(mark string) map[int]bool {
	pids := map[int]bool{}
	if environs, _ := filepath.Glob("/proc/[0-9]*/environ"); environs != nil {
		for _, path := range environs {
			if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(mark)) {
				p, _ := strconv.Atoi(filepath.Base(filepath.Dir(path)))
				pids[p] = true
			}
		}
		return pids
	}
	out, _ := exec.Command("ps", "-A", "-E", "-ww", "-o", "pid=,command=").Output()
	for line := range strings.Lines(string(out)) {
		var p int
		if _, err := fmt.Sscan(line, &p); err == nil && strings.Contains(line, mark) {
			pids[p] = true
		}
	}
	return pids
}

// detach starts cmd in a session of its own, so the terminal that started it can close.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
