//go:build unix

package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunStopsTheAgentAtItsConfiguredTimeout(t *testing.T) {
	// One run after the other: each keeps its own limit.
	for _, limit := range []time.Duration{2 * time.Second, time.Second} {
		t.Run(limit.String(), func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML+"agent_timeout: "+limit.String()+"\n")
			fifo := filepath.Join(t.TempDir(), "pid")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FAKE_AGENT", "hang")
			t.Setenv("FAKE_AGENT_PID", fifo)
			pids := make(chan int, 1)
			go func() {
				b, _ := os.ReadFile(fifo)
				pid, _ := strconv.Atoi(string(b))
				pids <- pid
			}()
			var out bytes.Buffer
			err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
			want := "timed out after " + Human(limit)
			if err == nil || !strings.HasPrefix(err.Error(), want+";") {
				t.Fatalf("want an error starting %q, got %v", want, err)
			}
			rep := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
			if exit := reportNode(t, rep, "agent")["exit"]; exit != want {
				t.Fatalf("agent exit = %v, want %q", exit, want)
			}
			pid := <-pids
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			until(t, "the agent's grandchild is dead", func() bool { return !alive(pid) })
			if left := clones(t, root); len(left) != 0 {
				t.Fatalf("the run's clone outlived it: %v", left)
			}
		})
	}
}

func TestRunPassesAnAgentWithinItsConfiguredTimeout(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML+"agent_timeout: 1h\n")
	if err := Run("t", []string{issueFile(t, issue)}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestAgentTimeoutDefaultsToThirtyMinutes(t *testing.T) {
	if agentTimeout != 30*time.Minute {
		t.Fatalf("agentTimeout = %v, want 30m", agentTimeout)
	}
}
