//go:build unix

package run

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatusListsRunWaitingForAnswer stops a real run at a question: the status command lists it as waiting
// with its question, and the run is recorded as waiting, never failed.
func TestStatusListsRunWaitingForAnswer(t *testing.T) {
	bin := builtATM(t)
	top := gitT(t, repo(t, "local", atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1")),
		"rev-parse", "--show-toplevel")
	stopServing(t, top)
	_, label := question(t, bin, top, "failed-check-finding", 1, "run", "--follow", issueFile(t, issue))
	for _, args := range [][]string{{"status"}, {"axi", "status"}, {"axi", "status", "--json"}} {
		cmd := exec.Command(bin, args...)
		cmd.Dir = top
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), label) ||
			!strings.Contains(string(out), "waiting: lint: exit status 1") {
			t.Fatalf("atm %v does not list %s as waiting with its question: %v\n%s", args, label, err, out)
		}
	}
	runs := waitingRuns(t, top)
	if runs[0].Outcome != "waiting" || !runs[0].Ended.IsZero() {
		t.Fatalf("waiting run recorded as %+v", runs[0])
	}
}

// TestPlanCountsRunWaitingForAnswerAsWorking reads a plan with the real atm, in a repository whose last run of
// issue 2 waits for an answer: the issue is working, so it cannot launch a second time.
func TestPlanCountsRunWaitingForAnswerAsWorking(t *testing.T) {
	bin := builtATM(t)
	top := gitT(t, repo(t, "https://github.com/o/r.git", ""), "rev-parse", "--show-toplevel")
	tools := t.TempDir()
	gh := "#!/bin/sh\necho '{\"state\":\"OPEN\",\"body\":\"\"}'\n"
	plan := `{"version":1,"max_active":3,"tickets":[{"issue":2,"depends_on":[],"conflicts_with":[]}]}`
	if err := errors.Join(os.WriteFile(filepath.Join(tools, "gh"), []byte(gh), 0o755),
		os.WriteFile(filepath.Join(tools, "plan.json"), []byte(plan), 0o600),
		os.MkdirAll(filepath.Join(top, ".atm"), 0o755),
		os.WriteFile(filepath.Join(top, ".atm", "runs.jsonl"),
			[]byte(`{"outcome":"waiting","run":"2-1","issue":"2","step":"checks"}`+"\n"), 0o600)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := exec.Command(bin, "axi", "plan", "--json", filepath.Join(tools, "plan.json"))
	cmd.Dir = top
	want := `{"open":[2],"ready":[],"cos_holds":[],"javi_holds":[],"working":[2],"free":3}` + "\n"
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != want {
		t.Fatalf("atm axi plan: %v\n%s\nwant\n%s", err, out, want)
	}
}
