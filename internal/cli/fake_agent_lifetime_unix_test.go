//go:build unix

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestHeldFakeCodexExitsWhenParentDies(t *testing.T) {
	if os.Getenv("ATM_TEST_FAKE_PARENT") == "1" {
		codex := exec.Command("codex")
		if err := codex.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Println(codex.Process.Pid)
		_, _ = os.Stdin.Read(make([]byte, 1)) // the test keeps this pipe open until SIGKILL
		return
	}
	ready, _ := setupBarrierAgent(t)
	parent := exec.Command(os.Args[0], "-test.run=^TestHeldFakeCodexExitsWhenParentDies$")
	parent.Env, parent.Stderr = append(os.Environ(), "ATM_TEST_FAKE_PARENT=1"), os.Stderr
	input, err := parent.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	output, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Process.Kill(); _ = parent.Wait() })
	var pid int
	if _, err := fmt.Fscan(output, &pid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	within10s(t, "fake codex held", func() bool {
		_, err := os.Stat(filepath.Join(ready, strconv.Itoa(pid)))
		return err == nil
	})
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait() // reap the parent before checking its fake
	within10s(t, "held fake codex gone after its parent", func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH })
}

func within10s(t *testing.T, what string, cond func() bool) {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for deadline := time.Now().Add(10 * time.Second); !cond(); <-tick.C {
		if time.Now().After(deadline) {
			t.Fatalf("not within 10 s: %s", what)
		}
	}
}

func TestHeldFakeHelloClaudeExitsWhenOwnerDies(t *testing.T) {
	dir, call := helloRepo(t, "hang")
	owner := exec.Command("sh", "-c", "read line")
	input, err := owner.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill(); _ = owner.Wait() })
	t.Setenv("ATM_FAKE_OWNER_PID", strconv.Itoa(owner.Process.Pid))
	claude := exec.Command("claude")
	claude.Dir = dir
	if err := claude.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = claude.Wait(); close(done) }()
	t.Cleanup(func() { _ = claude.Process.Kill(); <-done })
	within10s(t, "fake claude started", func() bool {
		_, err := os.Stat(call)
		return err == nil
	})
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	select {
	case <-done:
		if waitErr != nil {
			t.Fatalf("fake claude failed: %v", waitErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fake claude survived its owner for 10 seconds")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fake claude directory must remain present: %v", err)
	}
}
