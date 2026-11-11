//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// planWith is testdata/plan.json's path, read against a fake gh, where issue 1 is closed and every other open, in a
// repository whose runs.jsonl is runs.
func planWith(t *testing.T, runs string) string {
	t.Helper()
	plan, err := filepath.Abs(filepath.Join("testdata", "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := repo(t, "https://github.com/o/r.git")
	bin := t.TempDir()
	gh := "#!/bin/sh\n[ \"$3\" = 1 ] && state=CLOSED || state=OPEN\n" +
		"echo '{\"state\":\"'$state'\",\"body\":\"Type: feature\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.MkdirAll(filepath.Join(dir, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".atm", "runs.jsonl"), []byte(runs), 0o600); err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestAxiPlanSaysWhichTicketsCanLaunch reads testdata/plan.json against a fake gh, where issue 1 is closed and
// every other open, and a runs.jsonl where 7 runs its agent.
func TestAxiPlanSaysWhichTicketsCanLaunch(t *testing.T) {
	plan := planWith(t, `{"outcome":"running","run":"7-1","issue":"7","step":"agent"}`+"\n")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"axi", "plan", "--json", plan},
			`{"open":[2,3,5,6,7,8],"ready":[2],"cos_holds":[5],"javi_holds":[6],"working":[7],"free":2}` + "\n"},
		{[]string{"axi", "plan", plan},
			"open[6]: 2,3,5,6,7,8\nready[1]: 2\ncos_holds[1]: 5\njavi_holds[1]: 6\nworking[1]: 7\nfree: 2\n"},
	} {
		if out, err := atm(t, c.args...); err != nil || out != c.want {
			t.Fatalf("atm %v: %v\n%s\nwant\n%s", c.args, err, out, c.want)
		}
	}
}

// TestAxiPlanCountsAWaitingRunAsWorking reads testdata/plan.json with issue 2's run waiting for an answer: the
// issue is working, not ready to launch a second time.
func TestAxiPlanCountsAWaitingRunAsWorking(t *testing.T) {
	plan := planWith(t, `{"outcome":"waiting","run":"2-1","issue":"2","step":"checks"}`+"\n")
	want := `{"open":[2,3,5,6,7,8],"ready":[7,8],"cos_holds":[5],"javi_holds":[6],"working":[2],"free":3}` + "\n"
	if out, err := atm(t, "axi", "plan", "--json", plan); err != nil || out != want {
		t.Fatalf("atm axi plan: %v\n%s\nwant\n%s", err, out, want)
	}
}
