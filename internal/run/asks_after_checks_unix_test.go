//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunAsksAfterChecks: a ponytail step or delivery command that fails stops the run and asks, as a failed
// check does. Approve goes on, fix gives the worker one turn with the finding, stop keeps the clone and its work.
func TestRunAsksAfterChecks(t *testing.T) {
	builtATM(t)
	// Each step fails until the fix turn leaves repaired; want is in its finding, code is its exit code.
	cases := map[string]struct {
		ponytail, delivery, want string
		code                     int
	}{
		"ponytail": {"test -f repaired", "", "ponytail.jsonl", 2},
		"delivery": {"", "test -f repaired || { echo delivery-finding; exit 1; }; ", "delivery-finding", 4},
	}
	for step, c := range cases {
		for _, answer := range []string{"approve", "fix", "stop"} {
			t.Run(step+"/"+answer, func(t *testing.T) {
				delivered, call := filepath.Join(t.TempDir(), "delivered"), filepath.Join(t.TempDir(), "call")
				for k, v := range map[string]string{"FAKE_PONYTAIL_WORK": c.ponytail, "FAKE_AGENT_CALL": call,
					"FAKE_AGENT_WORK": "test -f first-turn && touch repaired || { " + fixWork + "; touch first-turn; }"} {
					t.Setenv(k, v)
				}
				top := gitT(t, repo(t, "local", atmSet(atmYAML, "delivery", c.delivery+"pwd > "+delivered)),
					"rev-parse", "--show-toplevel")
				bin := followUpsServer(t, top)
				clone, label := question(t, c.want, c.code, bin, top, "run", "--follow", issueFile(t, issue))
				assertCheckWaiting(t, top, label)
				_, early := os.Stat(delivered)
				answerFailedCheck(t, bin, top, label, answer, map[bool]int{true: c.code}[answer == "stop"])
				if !os.IsNotExist(early) {
					t.Fatalf("delivered before the answer: %v", early)
				}
				assertAnsweredAfterChecks(t, top, label, clone, delivered, step, answer, c.want, call)
			})
		}
	}
}

func assertAnsweredAfterChecks(t *testing.T, top, label, clone, delivered, step, answer, want, call string) {
	t.Helper()
	rep := e2eReport(t, top, label)
	if len(rep.Decisions) != 1 || rep.Decisions[0].Answer != answer ||
		!strings.Contains(rep.Decisions[0].Finding, want) {
		t.Fatalf("answer not recorded with finding: %+v", rep.Decisions)
	}
	data, err := os.ReadFile(delivered)
	if (answer == "fix" || answer == "approve" && step == "ponytail") != (err == nil) ||
		err == nil && strings.TrimSpace(string(data)) != clone {
		t.Fatalf("%s after a failed %s delivered %q, %v", answer, step, data, err)
	}
	switch answer {
	case "stop":
		work, err := os.ReadFile(filepath.Join(clone, "a.txt"))
		if rep.FailedNode != step || err != nil || string(work) != "fixed\n" {
			t.Fatalf("stop lost the failed %s or its work: %q, %v, %+v", step, work, err, rep)
		}
	case "fix":
		if got := agentCall(t, call); !strings.Contains(got.Stdin+strings.Join(got.Args, " "), want) {
			t.Fatalf("fix turn did not receive finding: %+v", got)
		}
	}
}
