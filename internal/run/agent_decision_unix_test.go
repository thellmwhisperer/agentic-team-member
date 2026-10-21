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
			assertQuestionNextStep(t, top, label, false)
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

func TestRunAsksAtTheAgentAfterFix(t *testing.T) {
	builtATM(t)
	for _, tc := range []struct{ name, finding string }{
		{"outside", "the agent reached outside its clone"},
		{"exit", "exit code 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top := gitT(t, repo(t, "local", atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1")),
				"rev-parse", "--show-toplevel")
			script := filepath.Join(t.TempDir(), "agent.sh")
			writeFixture(t, script, `cat > /dev/null
if test -f first-turn; then
  printf '%s\n' "$ATM_TEST_FAILURE_EVENT"
  exit 3
fi
`+fixWork+`
touch first-turn
printf '%s\n' '{"type":"result","result":"{\"test_file\":\"a_test.sh\"}"}'
`)
			t.Setenv("REJECTION_SCRIPT", script)
			t.Setenv("ATM_TEST_FAILURE_EVENT", "")
			if tc.name == "outside" {
				t.Setenv("ATM_TEST_FAILURE_EVENT", bashEvent("claude", "printf x > "+filepath.Join(t.TempDir(), "out"), ""))
			}
			bin := followUpsServer(t, top)
			_, label := failedCheckQuestion(t, bin, top, "run", "--follow", "--harness", "claude", issueFile(t, issue))
			assertQuestionNextStep(t, top, label, true)
			answerFailedCheck(t, bin, top, label, "fix", 1)
			assertQuestionNextStep(t, top, label, false)
			rep := e2eReport(t, top, label)
			if rep.FailedNode != "agent" || len(rep.Decisions) != 2 || rep.Decisions[0].Answer != "fix" ||
				rep.Decisions[1].Answer != "" || !strings.Contains(rep.Decisions[1].Finding, tc.finding) {
				t.Fatalf("fix failure did not ask at agent: %+v", rep)
			}
			answerFailedCheck(t, bin, top, label, "stop", 1)
		})
	}
}
