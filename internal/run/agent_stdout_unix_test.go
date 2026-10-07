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
	"time"
)

// init puts the fake agent in a session of its own when $FAKE_AGENT_SETSID is set, before it plays its mode:
// macOS has no setsid command.
func init() {
	if os.Getenv("FAKE_AGENT_SETSID") != "" {
		_, _ = syscall.Setsid()
	}
}

func TestRunKillsAChildHoldingTheAgentsStdout(t *testing.T) {
	for _, tc := range []struct{ name, env string }{
		{name: "in the agent's group"},
		// The agent's group is empty once it exits: killing it finds nothing, which is no failure.
		{name: "in its own session", env: "FAKE_AGENT_SETSID=1 "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(d time.Duration) { agentTimeout = d }(agentTimeout)
			agentTimeout = 10 * time.Second // bounds the run
			repo(t, "https://example.com/owner/repo.git", atmYAML)
			// Each claude call starts a grandchild that inherits its stdout and never ends, its pid in
			// pids/<call's pid>, then plays the default fake agent, which exits 0 with a valid report.
			pids, bin := t.TempDir(), t.TempDir()
			script := "#!/bin/sh\n" + tc.env + "FAKE_AGENT=grandchild FAKE_AGENT_PID=" + pids + "/$$ " +
				filepath.Join(fakes, "claude") + " &\nexec " + filepath.Join(fakes, "claude") + ` "$@"` + "\n"
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
		})
	}
}
