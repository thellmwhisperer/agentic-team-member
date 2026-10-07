package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCloneLeaseIsInheritedByStartedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps live clones through their working directory, not inherited lock handles")
	}
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

func TestRemoveDoesNotDeleteCloneWithReplacedLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "clone")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := dir + ".lock"
	old, err := openCloneLock(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	locked, err := tryLockCloneExclusive(old)
	if err != nil || !locked {
		t.Fatalf("lock old clone: %v, %v", locked, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "alive")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := remove(dir, old); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("remove deleted a clone with a different lock file: %v", err)
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
