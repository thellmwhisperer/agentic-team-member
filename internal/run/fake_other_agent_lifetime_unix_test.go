//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestHeldFakeAgentsExitWhenParentDies(t *testing.T) {
	names := []string{"codex", "pi", "cursor-agent", "opencode"}
	if os.Getenv("ATM_TEST_FAKE_PARENT") == "1" {
		for _, name := range names {
			if err := exec.Command(filepath.Join(fakes, name), "Task type: fix.").Start(); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = os.Stdin.Read(make([]byte, 1)) // the test keeps this pipe open until SIGKILL
		return
	}
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	if err := os.Mkdir(ready, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_AGENT_BARRIER_READY", ready)
	t.Setenv("FAKE_AGENT_BARRIER_RELEASE", filepath.Join(dir, "release"))
	parent := exec.Command(os.Args[0], "-test.run=^TestHeldFakeAgentsExitWhenParentDies$")
	parent.Env = append(os.Environ(), "ATM_TEST_FAKE_PARENT=1", "TMPDIR="+dir)
	parent.Dir, parent.Stderr = dir, os.Stderr
	input, err := parent.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Process.Kill(); _ = parent.Wait() })
	assertHeldFakesExit(t, parent, ready, len(names))
}

// assertHeldFakesExit waits for count fakes held at ready, kills their parent and fails unless all are gone
// within 10 seconds.
func assertHeldFakesExit(t *testing.T, parent *exec.Cmd, ready string, count int) {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	var entries []os.DirEntry
	for len(entries) < count {
		select {
		case <-tick.C:
			entries, _ = os.ReadDir(ready)
		case <-deadline.C:
			t.Fatalf("%d of %d fakes held", len(entries), count)
		}
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
		t.Cleanup(func() {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		})
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait() // reap the parent before checking its fakes
	deadline.Reset(10 * time.Second)
	for _, pid := range pids {
		for alive(pid) {
			select {
			case <-tick.C:
			case <-deadline.C:
				t.Fatalf("held fake %d survived its parent for 10 seconds", pid)
			}
		}
	}
}
