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

// The delivery command has no ceiling: it outlasts the commands' and passes. SIGINT and SIGTERM kill its group.
func TestDeliveryOutlastsTheCeilingAndDiesOnInterrupt(t *testing.T) {
	for _, c := range []struct {
		name, why string
		sig       syscall.Signal
	}{
		{name: "past the ceiling"},
		{name: "SIGINT", why: "interrupted", sig: syscall.SIGINT},
		{name: "SIGTERM", why: "interrupted", sig: syscall.SIGTERM},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The delivery's grandchild writes its pid to a fifo, then waits on another until the test opens it.
			pidFifo, gate := fifos(t)
			repo(t,"https://example.com/owner/repo.git", atmSet(atmYAML, "delivery",
				`cat "$ATM_TEST_GATE" & echo $! > "$ATM_TEST_PID"; wait $!`))
			defer func(d time.Duration) { timeout = d }(timeout)
			timeout = 2 * time.Second
			pids := make(chan int, 1)
			go func() {
				b, _ := os.ReadFile(pidFifo)
				pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
				pids <- pid
				if c.sig != 0 {
					_ = syscall.Kill(os.Getpid(), c.sig)
					return
				}
				<-time.After(2 * timeout)
				_ = os.WriteFile(gate, nil, 0) // blocks until cat reads: forever once it is dead
			}()
			var out bytes.Buffer
			err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
			end := events(t, &out)
			last := end[len(end)-1]
			if c.sig == 0 {
				if err != nil || ExitCode(err) != 0 || last["step"] != "delivery" || last["state"] != "passed" {
					t.Fatalf("want the delivery passed past the ceiling, got %v, %v", err, last)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.why) || ExitCode(err) != 4 || last["step"] != "delivery" {
				t.Fatalf("want the delivery %s, got %v, %v", c.why, err, last)
			}
			dead(t, <-pids)
		})
	}
}

// dead fails t unless pid dies within 5 s: killed is not yet reaped, its new parent, init, reaps it soon after.
func dead(t *testing.T, pid int) {
	t.Helper()
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	for deadline := time.Now().Add(5 * time.Second); alive(pid); {
		if time.Now().After(deadline) {
			t.Fatalf("the delivery's grandchild %d outlived the run", pid)
		}
	}
}

// fifos are two new fifos: pid, in ATM_TEST_PID, and gate, in ATM_TEST_GATE.
func fifos(t *testing.T) (pid, gate string) {
	t.Helper()
	dir := t.TempDir()
	pid, gate = filepath.Join(dir, "pid"), filepath.Join(dir, "gate")
	for _, f := range []string{pid, gate} {
		if err := syscall.Mkfifo(f, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ATM_TEST_PID", pid)
	t.Setenv("ATM_TEST_GATE", gate)
	return pid, gate
}
