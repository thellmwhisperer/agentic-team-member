//go:build unix

package run

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestKilledBackgroundKeepsCloneForStartedAgent(t *testing.T) {
	pidFifo, _ := fifos(t)
	ready := filepath.Join(t.TempDir(), "agent-started")
	t.Setenv("FAKE_AGENT", "hang")
	t.Setenv("FAKE_AGENT_PID", pidFifo)
	t.Setenv("FAKE_AGENT_READY", ready)
	t.Setenv("FAKE_AGENT_KILL_PARENT", "1")
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmYAML), "rev-parse", "--show-toplevel")
	t.Cleanup(func() { _ = os.Remove(socket(top)) })
	done := make(chan error, 1)
	go func() {
		_, err := Start(top, []string{issueFile(t, issue)})
		done <- err
	}()
	until(t, "agent start", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
	clone := theClone(t, top)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the background request did not end after its process was killed")
	}
	b, err := os.ReadFile(pidFifo)
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	proc, err := os.FindProcess(child)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Kill() })
	t.Setenv("FAKE_AGENT", "fail")
	t.Setenv("FAKE_AGENT_KILL_PARENT", "")
	nextRun(t, top)
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("the sweep removed the clone while its agent was alive: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	until(t, "the clone lease to be released", func() bool {
		f, locked, err := tryCloneExclusive(clone)
		if f != nil {
			_ = f.Close()
		}
		return err == nil && locked
	})
	nextRun(t, top)
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("the sweep kept the clone after the agent exited: %v", clones(t, top))
	}
}

func TestAgentExitKeepsCloneUntilOrphanChildExits(t *testing.T) {
	pidFifo, _ := fifos(t)
	t.Setenv("FAKE_AGENT", "orphan")
	t.Setenv("FAKE_AGENT_PID", pidFifo)
	top := backgroundRepo(t)
	o, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	if end, err := Attach(top, o.Run, &bytes.Buffer{}); err != nil || end.Outcome != "passed" {
		t.Fatalf("Attach = %+v, %v", end, err)
	}
	clone := theClone(t, top)
	b, err := os.ReadFile(pidFifo)
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	proc, err := os.FindProcess(child)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Kill() })
	t.Setenv("FAKE_AGENT", "fail")
	nextRun(t, top)
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("the sweep removed a clone whose agent child was alive: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	until(t, "the child lease to be released", func() bool {
		f, locked, err := tryCloneExclusive(clone)
		if f != nil {
			_ = f.Close()
		}
		return err == nil && locked
	})
	nextRun(t, top)
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("the sweep kept the clone after its child exited: %v", clones(t, top))
	}
}

// theClone is the one clone under root.
func theClone(t *testing.T, root string) string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(root, ".atm", "clones", "atm-run-*[0-9]"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("clones = %v, %v; want one", dirs, err)
	}
	return dirs[0]
}

// nextRun runs a run in the background, through to its end at its agent.
func nextRun(t *testing.T, root string) {
	t.Helper()
	o, err := Start(root, []string{issueFile(t, issue)})
	if err == nil {
		o, err = Attach(root, o.Run, &bytes.Buffer{})
	}
	if err != nil || o.FailedNode != "agent" {
		t.Fatalf("next run = %+v, %v", o, err)
	}
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// until fails t unless cond holds within 5 s: a killed process is not yet reaped, its new parent reaps it soon
// after.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("not within 5 s: %s", what)
		}
	}
}
