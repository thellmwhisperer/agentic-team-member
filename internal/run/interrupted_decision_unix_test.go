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
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("NO_COLOR", "")
			open := &screenRead{f: startFollow(t, bin, top, true, "attach", label)}
			open.until(func(s string) bool {
				return strings.Contains(s, "⏸ waiting") && strings.Contains(s, "approve|fix|stop")
			})
			killServeProcesses(t, top)
			waitForDeadServer(t, top)
			// The open attach may have restarted serve already; use the normal client path.
			recovered := bin
			startFollow(t, recovered, top, false, "runs").exit(0)
			shown := decisionTerminal(t, startFollow(t, recovered, top, true, "attach", label), 1)
			const reason = "the background process ended while the run waited for an answer"
			if !strings.Contains(shown, "the background process ended") || strings.Contains(shown, "approve|fix|stop") {
				t.Fatalf("interrupted decision still waits:\n%s", shown)
			}
			shown = decisionTerminal(t, open.f, 1)
			if strings.Count(shown, "the background process ended") != 1 || strings.Contains(shown, "approve|fix|stop") {
				t.Fatalf("open screen kept the interrupted decision:\n%s", shown)
			}
			want := "run it again: atm run " + issue
			if resumable {
				want = "atm run --resume " + label
			}
			recoveredRun(t, recovered, top, reason, want)
		})
	}
}

// recoveredRun fails the test unless top's only run, and atm runs of binary bin, show it failed with reason and
// its next step want.
func recoveredRun(t *testing.T, bin, top, reason, want string) {
	t.Helper()
	runs, err := Runs(top)
	if err != nil || len(runs) != 1 || runs[0].Outcome != "failed" || runs[0].Waits() || runs[0].Ended.IsZero() ||
		runs[0].Reason != reason || runs[0].NextStep != want {
		t.Fatalf("interrupted outcome: %+v, %v", runs, err)
	}
	list := exec.Command(bin, "runs")
	list.Dir = top
	if out, err := list.CombinedOutput(); err != nil || !strings.Contains(string(out), want) ||
		strings.Contains(string(out), "atm answer ") {
		t.Fatalf("atm runs recovery: %v\n%s", err, out)
	}
}

func TestRealAttachClearsInterruptedDeliveryDecision(t *testing.T) {
	bin := builtATM(t)
	gateFake(t)
	top := gitT(t, repo(t, "local", atmSet(atmYAML, "delivery", "exit 1")), "rev-parse", "--show-toplevel")
	stopServing(t, top)
	_, label := question(t, bin, top, "", 4, "run", "--follow", issueFile(t, issue))
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	sc := &screenRead{f: startFollow(t, bin, top, true, "attach", label)}
	sc.until(func(s string) bool {
		return strings.Contains(s, "⏸ waiting") && strings.Contains(s, "approve|fix|stop") && strings.Contains(s, "╭─ Gate")
	})
	killServeProcesses(t, top)
	waitForDeadServer(t, top)
	startFollow(t, bin, top, false, "runs").exit(0)
	sc.until(func(s string) bool {
		return strings.Contains(s, "✗ failed") && strings.Contains(s, "the background process ended")
	})
	for _, cols := range []uint16{80, 100} {
		sc.resized(cols, func(s string) bool {
			return strings.Contains(s, "✗ failed") && strings.Contains(s, "the background process ended") &&
				strings.Contains(s, "╭─ Gate") && !strings.Contains(s, "approve|fix|stop")
		})
	}
	if _, err := sc.f.term.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	shown := decisionTerminal(t, sc.f, 4)
	if !strings.Contains(shown, "the background process ended") || strings.Contains(shown, "approve|fix|stop") ||
		!strings.Contains(shown, "╭─ Gate") {
		t.Fatalf("final screen lost the interruption or the gate:\n%s", shown)
	}
}
