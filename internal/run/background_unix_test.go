//go:build unix

package run

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A background process killed in the middle of a run leaves the run's clone to what the run started, which goes
// on: the next background process's sweep removes it only once nothing of the run is alive, delivered or not, as
// its delivery never ended. It lists the run failed at the node it was in, for as long as it ran.
func TestBackgroundKilledMidRunLeavesTheCloneToWhatTheRunStarted(t *testing.T) {
	for _, c := range []struct{ node, delivery string }{
		{node: "agent"},
		{node: "delivery", delivery: `cat "$ATM_TEST_GATE" & echo $! > "$ATM_TEST_PID"; wait $!`},
	} {
		t.Run(c.node, func(t *testing.T) {
			top, clone, pid, group, killed := killMidRun(t, c.node, c.delivery)
			nextRun(t, top)
			if _, err := os.Stat(clone); err != nil {
				t.Fatalf("the sweep removed the clone of a run whose %s still runs: %v", c.node, err)
			}
			runs, err := Runs(top)
			if err != nil || len(runs) != 2 || runs[0].Outcome != "failed" || runs[0].FailedNode != c.node ||
				!runs[0].Started.Before(runs[0].Ended) || runs[0].Ended.After(killed) {
				t.Fatalf("Runs = %+v, %v; want the first failed at %s, for as long as it ran", runs, err, c.node)
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			until(t, "the run's "+c.node+" died", func() bool {
				return errors.Is(syscall.Kill(-group, 0), syscall.ESRCH)
			})
			nextRun(t, top)
			if _, err := os.Stat(clone); !os.IsNotExist(err) {
				t.Fatalf("the sweep kept the clone of a run nothing of which runs: %v", clones(t, top))
			}
		})
	}
}

// killMidRun starts a run in a background process of its own, with delivery, and kills that process once the
// run's node runs. It returns the repository, the run's clone, the pid and group of what the run started, and
// when it killed the process. The next runs end at their agent.
func killMidRun(t *testing.T, node, delivery string) (top, clone string, pid, group int, killed time.Time) {
	t.Helper()
	// What the run started writes its pid to a fifo, then waits on another, which no one opens.
	pidFifo, _ := fifos(t)
	t.Setenv("FAKE_AGENT_PID", pidFifo)
	if node == "agent" {
		t.Setenv("FAKE_AGENT", "hang")
	}
	// A local origin, which no fetch reaches, so a delivered clone is kept.
	top = gitT(t, repo(t, t.TempDir(), atmSet(atmYAML, "delivery", delivery)), "rev-parse", "--show-toplevel")
	t.Cleanup(func() { _ = os.Remove(socket(top)) }) // the background process ends at its next tick
	if _, err := Start(top, []string{issueFile(t, issue)}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(pidFifo)
	pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	group, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	clone = theClone(t, top)
	if b, err = os.ReadFile(clone + ".pid"); err != nil {
		t.Fatal(err)
	}
	server, _ := strconv.Atoi(strings.Fields(string(b))[0])
	if err := syscall.Kill(server, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	until(t, "the background process died", func() bool { return !alive(server) })
	t.Setenv("FAKE_AGENT", "fail")
	return top, clone, pid, group, time.Now()
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
