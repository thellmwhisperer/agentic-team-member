//go:build unix

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A background process that outlives its socket and its log file is stopped by the cleanup, which returns
// once its process id is gone; one that ends by itself passes untouched.
func TestCleanBackgroundServerWaitsForTheProcess(t *testing.T) {
	defer func(d time.Duration) { serveGrace = d }(serveGrace)
	serveGrace = 100 * time.Millisecond
	for _, c := range []struct{ name, program string }{{"stays alive", "sleep 60"}, {"ends by itself", "true"}} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, ".atm"), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", c.program)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			go func() { _ = cmd.Wait(); close(exited) }()
			log := filepath.Join(dir, ".atm", "serve.log")
			if err := os.WriteFile(log, fmt.Appendf(nil, "atm serve pid: %d\n", cmd.Process.Pid), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Run("cleanup", func(t *testing.T) { cleanBackgroundServer(t, dir) })
			select {
			case <-exited:
			case <-time.After(5 * time.Second): // the reaper may be a moment behind the cleanup
				t.Fatalf("pid %d is alive after its cleanup", cmd.Process.Pid)
			}
		})
	}
}
