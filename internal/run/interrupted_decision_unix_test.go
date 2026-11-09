//go:build unix

package run

import (
	"os/exec"
	"strings"
	"testing"
)

func TestRealAttachEndsAfterDecisionServerDies(t *testing.T) {
	bin := builtATM(t)
	for _, resumable := range []bool{true, false} {
		name := "repeat"
		if resumable {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			top := gitT(t, repo(t, "local", atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1")),
				"rev-parse", "--show-toplevel")
			stopServing(t, top)
			if !resumable {
				t.Setenv("FAKE_AGENT", "fail")
			}
			issue := issueFile(t, issue)
			_, label := question(t, bin, top, "", 1, "run", "--follow", issue)
			waiting := waitingRuns(t, top)[0]
			if strings.Contains(waiting.NextStep, "recovery:") != resumable {
				t.Fatalf("wrong recovery at decision: %+v", waiting)
			}
			killServeProcesses(t, top)
			waitForDeadServer(t, top)
			recovered := followUpsServer(t, top)
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("NO_COLOR", "")
			shown := decisionTerminal(t, startFollow(t, recovered, top, true, "attach", label), 1)
			const reason = "the background process ended while the run waited for an answer"
			if !strings.Contains(shown, "the background process ended") || strings.Contains(shown, "approve|fix|stop") {
				t.Fatalf("interrupted decision still waits:\n%s", shown)
			}
			want := "run it again: atm run " + issue
			if resumable {
				want = "atm run --resume " + label
			}
			runs, err := Runs(top)
			if err != nil || len(runs) != 1 || runs[0].Outcome != "failed" || runs[0].Waits() ||
				runs[0].Reason != reason || runs[0].NextStep != want {
				t.Fatalf("interrupted outcome: %+v, %v", runs, err)
			}
			list := exec.Command(recovered, "runs")
			list.Dir = top
			if out, err := list.CombinedOutput(); err != nil || !strings.Contains(string(out), want) ||
				strings.Contains(string(out), "atm answer ") {
				t.Fatalf("atm runs recovery: %v\n%s", err, out)
			}
		})
	}
}
