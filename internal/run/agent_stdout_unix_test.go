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

// init puts the fake agent in a session of its own when $FAKE_AGENT_SETSID is set, before it plays its mode:
// macOS has no setsid command.
func init() {
	if os.Getenv("FAKE_AGENT_SETSID") != "" {
		_, _ = syscall.Setsid()
	}
}

func TestRunKillsAChildHoldingTheAgentsStdout(t *testing.T) {
	for _, tc := range []struct{ name, env, mode, why string }{
		{name: "in the agent's group"},
		// The agent's group is empty once it exits: killing it finds nothing, which is no failure.
		{name: "in its own session", env: "FAKE_AGENT_SETSID=1 "},
		// What it left is killed whatever its exit, and its exit still fails the node.
		{name: "agent exits non-zero", mode: "fail", why: "exit status 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(d time.Duration) { agentTimeout = d }(agentTimeout)
			agentTimeout = 10 * time.Second // bounds the run
			repo(t, "https://example.com/owner/repo.git", atmYAML)
			// Each claude call starts a grandchild that inherits its stdout and never ends, its pid in
			// pids/<call's pid>, then plays the fake agent in tc.mode, by default one that exits 0 with a valid
			// report.
			pids, bin := t.TempDir(), t.TempDir()
			script := "#!/bin/sh\n" + tc.env + "FAKE_AGENT=grandchild FAKE_AGENT_PID=" + pids + "/$$ " +
				filepath.Join(fakes, "claude") + " &\nexec " + filepath.Join(fakes, "claude") + ` "$@"` + "\n"
			if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKE_AGENT", tc.mode)
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
			err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
			if tc.why == "" && err != nil {
				t.Fatalf("the run failed: %v", err)
			}
			if tc.why != "" && (err == nil || !strings.Contains(err.Error(), tc.why)) {
				t.Fatalf("want an error naming %q, got %v", tc.why, err)
			}
			if end, _, _ := agentStep(t, &out); end["state"] != map[bool]string{true: "passed", false: "failed"}[tc.why == ""] {
				t.Fatalf("end event: %v", end)
			}
			until(t, "a grandchild wrote its pid", func() bool { return len(grandchildren()) > 0 })
			for _, pid := range grandchildren() {
				until(t, "the grandchild "+strconv.Itoa(pid)+" is dead", func() bool { return !alive(pid) })
			}
		})
	}
}
