package run

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// checkProof checks rep's proof of unit n, on base, with test and type typ: red, then green, then passed.
func checkProof(t *testing.T, what string, got any, n int, base, test, typ string) {
	t.Helper()
	u, _ := got.(obj)
	red, _ := u["red"].(obj)
	green, _ := u["green"].(obj)
	if u["unit"] != float64(n) || u["base"] != base || u["test_file"] != test || u["type"] != typ ||
		u["result"] != "passed" || !strings.Contains(red["command"].(string), test) || red["exit_code"] != float64(1) ||
		green["command"] != red["command"] || green["exit_code"] != float64(0) {
		t.Fatalf("%s %d: want it on %s with %s, %s, red then green and passed, got %v", what, n, base, test, typ, u)
	}
	if _, ok := red["tail"].(string); !ok {
		t.Fatalf("%s %d: want the red trial's output, got %v", what, n, red)
	}
}

func TestReportKeepsEveryUnitTheReproofsAndTheInstall(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmInstall("true"))
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	t.Setenv("FAKE_PONYTAIL_WORK", "grep -v extra a.txt > cut; mv cut a.txt")
	t.Setenv("FAKE_AGENT_WORK", chainWork)
	t.Setenv("ATM_TEST_CRITERION", "A retry runs once after a timeout.")
	t.Setenv("ATM_TEST_EDIT", "ATM_TEST_CRITERION=none") // unit 2's follow-up quotes no criterion: two units
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if p := ends(t, &out, "ponytail"); len(p) != 1 || p[0]["kept"] != true {
		t.Fatalf("want the cut kept, got %v", p)
	}
	rep := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
	units, _ := rep["units"].([]any)
	if len(units) != 2 {
		t.Fatalf("want both units in the report, got %v", rep["units"])
	}
	sha := gitT(t, root, "rev-parse", "main")
	unit2, _ := units[1].(obj)["base"].(string)
	if unit2 == "" || unit2 == sha {
		t.Fatalf("want unit 2 on unit 1's commit, got %v", units[1])
	}
	checkProof(t, "unit", units[0], 1, sha, "a_test.sh", "fix")
	checkProof(t, "unit", units[1], 2, unit2, "c1_test.sh", "feature")
	reproofs, _ := rep["reproofs"].([]any)
	if len(reproofs) != 2 {
		t.Fatalf("want both proofs the slop detector re-ran, got %v", rep["reproofs"])
	}
	checkProof(t, "re-run proof of unit", reproofs[0], 1, sha, "a_test.sh", "fix")
	checkProof(t, "re-run proof of unit", reproofs[1], 2, unit2, "c1_test.sh", "feature")
	var installs int
	for _, c := range rep["commands"].([]any) {
		if c := c.(obj); c["name"] == "install" && c["command"] == "true" && c["result"] == "passed" {
			installs++
		}
	}
	if installs != 3 {
		t.Fatalf("want the clone's install, then each unit's, got %v", rep["commands"])
	}
}
