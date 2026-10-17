//go:build unix

package cli

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestFakeHelloClaudeExitsWhenDirectoryRemoved(t *testing.T) {
	dir, call := helloRepo(t, "hang")
	cmd := exec.Command("claude")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(call); err == nil {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("fake claude did not start")
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("fake claude survived its removed working directory for 10 seconds")
	}
}
