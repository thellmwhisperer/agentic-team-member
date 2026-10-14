//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAxiPlanSaysWhichTicketsCanLaunch reads testdata/plan.json against a fake gh, where issue 1 is closed and
// every other open, and a runs.jsonl where 7 runs its agent.
func TestAxiPlanSaysWhichTicketsCanLaunch(t *testing.T) {
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
	runs := `{"outcome":"running","run":"7-1","issue":"7","step":"agent"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".atm", "runs.jsonl"), []byte(runs), 0o600); err != nil {
		t.Fatal(err)
	}
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
