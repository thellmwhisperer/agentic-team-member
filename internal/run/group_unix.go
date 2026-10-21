//go:build unix

package run

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// ownGroup starts cmd as the leader of a new session and process group, so no terminal controls it, marks its
// environment with a variable of its own, which every process it starts inherits, and makes its cancel kill the
// whole group, every descendant and every process that bears the mark, even one in a session of its own that
// init has adopted.
func ownGroup(cmd *exec.Cmd) (func() error, func(), error) {
	mark := "ATM_RUN_MARK=" + rand.Text()
	cmd.Env = append(cmd.Environ(), mark)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return killTree(cmd.Process.Pid, mark) }
	return func() error { return nil }, func() {}, nil
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

// hangAfter is how long an agent may stay silent while no process under it works before ATM stops it as hung.
// A string, so a build can set it with -ldflags -X.
var hangAfter = "10m"

var errHung = errors.New("hung")
var errActivity = errors.New("observe agent activity")

// watchHang cancels ctx with errHung once, for hangAfter, the agent pid has written no byte to stdout or stderr
// (out) and no descendant of it has started, ended, used CPU time or transferred bytes.
func watchHang(ctx context.Context, cancel context.CancelCauseFunc, pid int, out *heard) {
	limit, _ := time.ParseDuration(hangAfter)
	tick := time.NewTicker(limit / 10)
	defer tick.Stop()
	was, quiet := "", time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		activity, err := working(pid)
		if err != nil {
			cancel(fmt.Errorf("%w: %w", errActivity, err))
			return
		}
		if now := fmt.Sprint(atomic.LoadInt64((*int64)(out)), activity); now != was {
			was, quiet = now, time.Now()
		} else if time.Since(quiet) >= limit {
			cancel(errHung)
			return
		}
	}
}

func working(pid int) (string, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	if err != nil {
		return "", err
	}
	tree, activity := map[int]bool{pid: true}, map[int]string{}
	for grew := true; grew; {
		grew = false
		for line := range strings.Lines(string(out)) {
			var p, parent int
			if _, err := fmt.Sscan(line, &p, &parent); err == nil && tree[parent] && !tree[p] {
				tree[p], grew = true, true
				value, err := processActivity(p)
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
					continue
				}
				if err != nil {
					return "", fmt.Errorf("pid %d: %w", p, err)
				}
				activity[p] = value
			}
		}
	}
	return fmt.Sprint(activity), nil
}
