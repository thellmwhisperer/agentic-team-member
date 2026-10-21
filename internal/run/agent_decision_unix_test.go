//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAsksAtTheAgent(t *testing.T) {
	builtATM(t)
	// The fake ponytail pass replays the outside write: approve is proven by Checks passing before it fails.
	for _, tc := range []struct{ name, mode, finding, answer, failed, checks string }{
		{"outside", "", "the agent reached outside its clone", "approve", "ponytail", "passed"},
		{"exit", "fail", "exit code 3", "stop", "agent", "not run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top := gitT(t, repo(t, "local", atmYAML), "rev-parse", "--show-toplevel")
			t.Setenv("FAKE_AGENT", tc.mode)
			if tc.mode == "" {
				t.Setenv("FAKE_AGENT_STREAM", bashEvent("claude", "printf x > "+filepath.Join(t.TempDir(), "out"), ""))
			}
			bin := followUpsServer(t, top)
			clone, label := question(t, bin, top, tc.finding, "run", "--follow", issueFile(t, issue))
			if _, err := os.Stat(clone); err != nil {
				t.Fatalf("paused run lost its clone: %v", err)
			}
			answerFailedCheck(t, bin, top, label, tc.answer, map[string]int{"agent": 1, "ponytail": 2}[tc.failed])
			rep := e2eReport(t, top, label)
			if len(rep.Decisions) != 1 || rep.Decisions[0].Answer != tc.answer ||
				!strings.Contains(rep.Decisions[0].Finding, tc.finding) {
				t.Fatalf("answer not recorded with finding: %+v", rep.Decisions)
			}
			if rep.FailedNode != tc.failed || rep.node("checks").Result != tc.checks {
				t.Fatalf("%s: failed at %s, checks %s", tc.answer, rep.FailedNode, rep.node("checks").Result)
			}
		})
	}
}
