//go:build unix

package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestRunKillsAChildHoldingTheAgentsStdout(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	// Each claude call starts a grandchild that inherits its stdout and never ends, its pid in pids/<call's pid>,
	// then plays the default fake agent, which exits 0 with a valid report.
	pids, bin := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\nFAKE_AGENT=grandchild FAKE_AGENT_PID=" + pids + "/$$ " + filepath.Join(fakes, "claude") +
		" &\nexec " + filepath.Join(fakes, "claude") + ` "$@"` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	grandchildren := func() (got []int) {
		files, _ := filepath.Glob(filepath.Join(pids, "*"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			if pid, err := strconv.Atoi(string(b)); err == nil {
				got = append(got, pid)
			}
		}
		return got
	}
	t.Cleanup(func() {
		for _, pid := range grandchildren() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if end, _, _ := agentStep(t, &out); end["state"] != "passed" {
		t.Fatalf("end event: %v", end)
	}
	until(t, "a grandchild wrote its pid", func() bool { return len(grandchildren()) > 0 })
	for _, pid := range grandchildren() {
		until(t, "the grandchild "+strconv.Itoa(pid)+" is dead", func() bool { return !alive(pid) })
	}
}
