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

func TestRunKillsTheAgentGroup(t *testing.T) {
	for _, c := range []struct {
		name, why string
		sig       syscall.Signal
	}{
		{name: "timeout", why: "timed out"},
		{name: "SIGINT", why: "interrupted", sig: syscall.SIGINT},
		{name: "SIGTERM", why: "interrupted", sig: syscall.SIGTERM},
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
			t.Setenv("FAKE_AGENT", "hang")
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
			err := Run([]string{issueFile(t, issue)}, &out, io.Discard)
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
