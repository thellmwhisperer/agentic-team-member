//go:build unix

package run

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunKillsTheAgentGroup(t *testing.T) {
	for _, c := range []struct {
		name, why, mode string
		sig             syscall.Signal
	}{
		{name: "timeout", why: "timed out", mode: "hang"},
		{name: "SIGINT", why: "interrupted", mode: "hang", sig: syscall.SIGINT},
		{name: "SIGTERM", why: "interrupted", mode: "hang", sig: syscall.SIGTERM},
		{name: "timeout, own session", why: "timed out", mode: "hang-session"},
		{name: "SIGTERM, own session", why: "interrupted", mode: "hang-session", sig: syscall.SIGTERM},
		{name: "timeout, orphan", why: "timed out", mode: "hang-orphan"},
		{name: "SIGTERM, orphan", why: "interrupted", mode: "hang-orphan", sig: syscall.SIGTERM},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			if c.sig == 0 {
				defer func(d time.Duration) { agentTimeout = d }(agentTimeout)
				agentTimeout = 3 * time.Second
			}
			// The grandchild writes its pid to a fifo: reading it blocks until the agent's group is up.
			fifo := filepath.Join(t.TempDir(), "pid")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FAKE_AGENT", c.mode)
			t.Setenv("FAKE_AGENT_PID", fifo)
			pids := make(chan int, 1)
			go func() {
				b, _ := os.ReadFile(fifo)
				pid, _ := strconv.Atoi(string(b))
				if c.sig != 0 {
					_ = syscall.Kill(os.Getpid(), c.sig)
				}
				pids <- pid
			}()
			var out bytes.Buffer
			err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			}
			if end, _, _ := agentStep(t, &out); end["state"] != "failed" || end["error"] != err.Error() {
				t.Fatalf("end event: %v", end)
			}
			pid := <-pids
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			// Killed is not yet reaped: its new parent, init, reaps it soon after.
			for deadline := time.Now().Add(5 * time.Second); alive(pid); {
				if time.Now().After(deadline) {
					t.Fatalf("the agent's grandchild %d outlived the run", pid)
				}
			}
			if left := clones(t, root); len(left) != 0 {
				t.Fatalf("the run's clone outlived it: %v", left)
			}
		})
	}
}

func TestRunKeepsTheCauseOfTheAgentsDeath(t *testing.T) {
	defer func(d time.Duration) { agentSilence = d }(agentSilence)
	agentSilence = time.Second
	for _, c := range []struct{ mode, exit, why, tail string }{
		{mode: "silent", exit: "silent for 1.0 s", why: "the agent said nothing for 1.0 s; ATM stopped it",
			tail: "fake stderr"},
		{mode: "fail", exit: "exit code 3", why: "exit code 3", tail: "boom"},
		{mode: "empty-fail", exit: "exit code 3", why: "exit code 3"},
		{mode: "killed", exit: "signal killed", why: "signal killed", tail: "fake stderr"},
	} {
		t.Run(c.mode, func(t *testing.T) { assertAgentDeath(t, c) })
	}
}

func assertAgentDeath(t *testing.T, c struct{ mode, exit, why, tail string }) {
	t.Helper()
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("FAKE_AGENT", c.mode)
	t.Setenv("FAKE_AGENT_PID", pidFile)
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
	end, _, log := agentStep(t, &out)
	want := c.why + "; log: " + log
	if err == nil || err.Error() != want || end["error"] != want {
		t.Fatalf("want %q, got %v, end event %v", want, err, end)
	}
	rep := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
	n := reportNode(t, rep, "agent")
	tail, _ := n["tail"].(string)
	if rep["reason"] != want || n["exit"] != c.exit ||
		c.tail != "" && (!strings.Contains(tail, c.tail) ||
			!strings.Contains(tail, `{"type": "system", "subtype": "init"}`)) {
		t.Fatalf("want reason %q, exit %q and a tail holding %q, got %v", want, c.exit, c.tail, rep)
	}
	if _, ok := n["tail"]; !ok {
		t.Fatalf("report.json omitted empty tail: %v", n)
	}
	assertAgentStderr(t, c.mode, log)
	if c.mode == "silent" {
		b, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(string(b))
		until(t, "the silent agent is dead", func() bool { return pid > 0 && !alive(pid) })
	}
}

func assertAgentStderr(t *testing.T, mode, log string) {
	t.Helper()
	stderr, _ := os.ReadFile(strings.TrimSuffix(log, ".jsonl") + ".stderr.txt")
	if mode != "empty-fail" && !strings.HasPrefix(string(stderr), "fake stderr\n") ||
		mode == "fail" && string(stderr) != "fake stderr\nboom\n" {
		t.Fatalf("worker.stderr.txt: %q", stderr)
	}
}

func TestRunKeepsTheLastTwentyLinesInCombinedOrder(t *testing.T) {
	defer func(d time.Duration) { agentSilence = d }(agentSilence)
	agentSilence = time.Second
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("FAKE_AGENT", "tail-sequence")
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "exit code 3") {
		t.Fatalf("the sequence agent should fail with exit code 3: %v", err)
	}
	rep := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
	tail, _ := reportNode(t, rep, "agent")["tail"].(string)
	var want []string
	for i := 3; i <= 22; i++ {
		want = append(want, fmt.Sprintf("line-%02d", i))
	}
	if tail != strings.Join(want, "\n") {
		t.Fatalf("tail = %q, want last 20 lines %q", tail, strings.Join(want, "\n"))
	}
	for _, name := range []string{"issue", "clone", "contract", "checks", "ponytail", "delivery"} {
		if _, ok := reportNode(t, rep, name)["tail"]; ok {
			t.Fatalf("non-agent node %q has a tail", name)
		}
	}
}

func TestRunCountsBlankAgentLinesAsActivity(t *testing.T) {
	defer func(d time.Duration) { agentSilence = d }(agentSilence)
	agentSilence = time.Second
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("FAKE_AGENT", "blank-lines")
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
	if err == nil || !strings.HasPrefix(err.Error(), "exit code 0; agent left no report") {
		t.Fatalf("blank lines should prevent the silence stop: %v", err)
	}
	rep := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
	tail, _ := reportNode(t, rep, "agent")["tail"].(string)
	if !strings.Contains(tail, "\n\n") || strings.Contains(tail, "silent for") {
		t.Fatalf("blank lines are missing from the tail or were treated as silence: %q", tail)
	}
}
