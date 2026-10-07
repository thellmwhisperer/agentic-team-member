package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCloneLeaseIsInheritedByStartedProcess(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "clone")
	if err := os.Mkdir(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clone+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startTestCloneLease(clone); err != nil {
		t.Fatal(err)
	}
	ready, done := filepath.Join(t.TempDir(), "ready"), filepath.Join(t.TempDir(), "done")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCloneLeaseChild$")
	cmd.Env = append(os.Environ(), "ATM_CLONE_LEASE_READY="+ready, "ATM_CLONE_LEASE_DONE="+done)
	if err := inheritCloneLease(cmd, clone); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := release(clone, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("release removed a clone while its child held the inherited lease: %v", err)
	}
	if err := os.WriteFile(done, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	sweep(filepath.Dir(clone), "")
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("sweep kept the clone after its child exited: %v", err)
	}
}

func TestCloneLeaseChild(t *testing.T) {
	ready, ok := os.LookupEnv("ATM_CLONE_LEASE_READY")
	if !ok {
		return
	}
	_ = os.WriteFile(ready, nil, 0o600)
	done := os.Getenv("ATM_CLONE_LEASE_DONE")
	for {
		if _, err := os.Stat(done); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
