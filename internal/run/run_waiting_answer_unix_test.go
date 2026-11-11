//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRefusesIssueWithRunWaitingForAnswer(t *testing.T) {
	builtATM(t)
	top := repo(t, "local", atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1"))
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	bin := followUpsServer(t, top)
	path := issueFile(t, issue)
	clone, label := failedCheckQuestion(t, bin, top, "run", "--follow", path)
	assertCheckWaiting(t, top, label)
	relative, err := filepath.Rel(top, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{path}, {"--follow", relative}} {
		cmd := exec.Command(bin, append([]string{"run"}, args...)...)
		cmd.Dir = top
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), label) ||
			!strings.Contains(string(out), "waiting for an answer") ||
			!strings.Contains(string(out), "atm answer "+label) {
			t.Fatalf("want refusal naming the waiting run and answer command: %v\n%s", err, out)
		}
	}
	assertWaitingRunUntouched(t, top, clone, label)
	// A different issue with the same filename is still allowed to start.
	_, other := failedCheckQuestion(t, bin, top, "run", "--follow", issueFile(t, issue))
	answerFailedCheck(t, bin, top, other, "stop", 1)
	// Explicit resume keeps its existing behaviour while the original question waits.
	_, resumed := failedCheckQuestion(t, bin, top, "run", "--follow", "--resume="+label)
	answerFailedCheck(t, bin, top, resumed, "stop", 1)
	answerFailedCheck(t, bin, top, label, "stop", 1)
	nextClone, next := failedCheckQuestion(t, bin, top, "run", "--follow", path)
	if next == label || nextClone == clone || nextClone == "" {
		t.Fatalf("launch after stop did not start a fresh run: %s, %s", next, nextClone)
	}
	answerFailedCheck(t, bin, top, next, "stop", 1)
}

func assertWaitingRunUntouched(t *testing.T, top, clone, label string) {
	t.Helper()
	for dir, want := range map[string]int{"clones": 2, "runs": 1} { // the waiting clone has a lock
		entries, err := os.ReadDir(filepath.Join(top, ".atm", dir))
		if err != nil || len(entries) != want {
			t.Fatalf("refused launch created %s: %v, %v", dir, entries, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(clone, "a.txt")); err != nil || string(data) != "fixed\n" {
		t.Fatalf("refusal lost the waiting run's work: %q, %v", data, err)
	}
	assertCheckWaiting(t, top, label)
}
