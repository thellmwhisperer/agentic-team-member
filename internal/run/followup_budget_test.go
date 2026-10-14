package run

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// primaryWork is unit 1's work: it fixes a.txt, runs $ATM_TEST_PRIMARY and declares c1_test.sh, red until a.txt
// has u2, quoting the issue.
const primaryWork = `printf 'fixed\n' > a.txt; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh; ` +
	`eval "$ATM_TEST_PRIMARY"; mkdir -p .atm/follow-ups/1; echo 'grep -q u2 a.txt' > .atm/follow-ups/1/c1_test.sh; ` +
	`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"gap","red_test":"c1_test.sh",` +
	`"criterion":"A retry runs once after a timeout."}]}' > .atm/fake-report.json`

// noFollowUps is the report of a unit that declares none.
const noFollowUps = `printf '{"test_file":"a_test.sh","follow_ups":[]}' > .atm/fake-report.json`

// threeUnits runs chainWork's three units, unit 3's follow-up quoting last, the delivered log and status going to
// the file it returns.
func threeUnits(t *testing.T, last string) (root, delivered string, out *bytes.Buffer, err error) {
	t.Helper()
	delivered, out = filepath.Join(t.TempDir(), "delivered"), &bytes.Buffer{}
	for k, v := range map[string]string{"ATM_TEST_OUT": delivered, "ATM_TEST_LAST": last,
		"ATM_TEST_CRITERION": "A retry runs once after a timeout.", "FAKE_PONYTAIL_REPORT": cut,
		"FAKE_AGENT_WORK":    `if [ -f c2_test.sh ]; then ATM_TEST_CRITERION="$ATM_TEST_LAST"; fi; ` + chainWork,
		"FAKE_PONYTAIL_WORK": "grep -v extra a.txt > cut; mv cut a.txt"} {
		t.Setenv(k, v)
	}
	root = repo(t, "https://example.com/owner/repo.git",
		atmSet(atmYAML, "delivery", `{ git log --format=%s; git status --porcelain; } > "$ATM_TEST_OUT"`))
	return root, delivered, out, Run("t", []string{issueFile(t, followUpIssue)}, out, io.Discard)
}

func TestRunDeliversThreeUnitsWhenTheLastFollowUpQuotesNoCriterion(t *testing.T) {
	root, delivered, out, err := threeUnits(t, "Errors are logged.")
	if err != nil {
		t.Fatal(err)
	}
	if agents := ends(t, out, "agent"); len(agents) != 3 || agents[2]["state"] != "passed" {
		t.Fatalf("want three units, got %v", agents)
	}
	b, _ := os.ReadFile(delivered)
	if string(b) != "ponytail: 1 cuts\nfeat: Retry on timeout\nfeat: Retry on timeout\nfix: Retry on timeout\ninit\n" {
		t.Fatalf("want each unit committed on the one before, then the cut, got\n%s", b)
	}
	if left := readFollowUps(t, root); len(left) != 1 || left[0]["red_test"] != "c3_test.sh" ||
		left[0]["test"] != "grep -q u4 a.txt\n" || left[0]["criterion"] != "Errors are logged." {
		t.Fatalf("want unit 3's out-of-goal follow-up in follow-ups.json, got %v", left)
	}
}

func TestRunBlocksAnUnmetCriterionWithItsEvidence(t *testing.T) {
	root, _, out, err := threeUnits(t, "A retry runs once after a timeout.")
	if err == nil {
		t.Fatal("want the run blocked on the criterion unit 3 leaves unmet")
	}
	clone, _ := ends(t, out, "clone")[0]["clone"].(string)
	left := readFollowUps(t, root)
	if len(left) != 1 || left[0]["criterion"] != "A retry runs once after a timeout." ||
		!strings.Contains(left[0]["diff"].(string), "+u3") || !strings.Contains(left[0]["cause"].(string), "unit 3") ||
		left[0]["dependency"] != gitT(t, clone, "rev-parse", "HEAD") {
		t.Fatalf("want the unmet criterion with unit 3's diff, cause and commit, got %v", left)
	}
}

func TestRunExemptsThePrimaryUnitFromTheGrossBudget(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("ATM_TEST_PRIMARY", "seq 1 300 > big.txt")
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then echo u2 >> a.txt; `+noFollowUps+`; else `+
		primaryWork+`; fi`)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatalf("want 300 primary lines exempt from the budget, got %v", err)
	}
	if agents := ends(t, &out, "agent"); len(agents) != 2 {
		t.Fatalf("want two units, got %v", agents)
	}
}

func TestRunBlocksAdditionalWorkOverTheGrossBudgetThoughItsNetIsSmall(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("ATM_TEST_PRIMARY", "seq 1 81 > big.txt")
	// Unit 2 deletes 81 lines and adds 120: net 39, gross 201.
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then : > big.txt; seq 1 119 > more.txt; `+
		`echo u2 >> a.txt; `+noFollowUps+`; else `+primaryWork+`; fi`)
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "201") {
		t.Fatalf("want 201 gross lines of unit 2 over the 200-line budget, got %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "report.json"))
	var report struct{ Units []obj }
	if err := json.Unmarshal(b, &report); err != nil || len(report.Units) != 2 {
		t.Fatalf("report.json: %v\n%s", err, b)
	}
	left := readFollowUps(t, root)
	if len(left) != 1 || left[0]["red_test"] != "c1_test.sh" ||
		left[0]["criterion"] != "A retry runs once after a timeout." ||
		!strings.Contains(left[0]["diff"].(string), "+119") || !strings.Contains(left[0]["diff"].(string), "-81") ||
		!strings.Contains(left[0]["cause"].(string), "201") || left[0]["dependency"] != report.Units[1]["base"] {
		t.Fatalf("want a blocking child with unit 2's diff, cause, contract and base, got %v", left)
	}
}

func TestRunKeepsTheGrossBudgetAcrossUnitsAndResume(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	// Unit 2 adds 150 lines, unit 3 takes 59 of them back and adds one: gross 210, net 92.
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c2_test.sh ]; then tail -n +60 x.txt > y; mv y x.txt; `+
		`echo u3 >> a.txt; `+noFollowUps+`; elif [ -f c1_test.sh ]; then seq 1 149 > x.txt; echo u2 >> a.txt; `+
		`mkdir -p .atm/follow-ups/1; echo 'grep -q u3 a.txt' > .atm/follow-ups/1/c2_test.sh; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"gap 2","red_test":"c2_test.sh",`+
		`"criterion":"A retry runs once after a timeout."}]}' > .atm/fake-report.json; else `+primaryWork+`; fi`)
	var first bytes.Buffer
	err := Run("failed", []string{issueFile(t, followUpIssue)}, &first, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "210") {
		t.Fatalf("want units 2 and 3 over the budget at 210 gross lines, got %v", err)
	}
	var resumed bytes.Buffer
	err = Run("resumed", []string{"--resume", "failed", issueFile(t, followUpIssue)}, &resumed, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "210") {
		t.Fatalf("want the resumed unit 3 still over the budget at 210 gross lines, got %v", err)
	}
	var left []obj
	b, _ := os.ReadFile(filepath.Join(root, ".atm", "runs", "resumed", "follow-ups.json"))
	if json.Unmarshal(b, &left) != nil || len(left) != 1 || left[0]["red_test"] != "c2_test.sh" ||
		!strings.Contains(left[0]["cause"].(string), "210") {
		t.Fatalf("want the resumed run's blocking child, got %s", b)
	}
}

func TestRunPersistsTinyOutOfGoalWorkOfAnAdditionalUnit(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then echo u2 >> a.txt; mkdir -p .atm/follow-ups/1; `+
		`echo 'test -f log.txt' > .atm/follow-ups/1/e_test.sh; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"logging","red_test":"e_test.sh",`+
		`"criterion":"Errors are logged."}]}' > .atm/fake-report.json; else `+primaryWork+`; fi`)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatalf("want an out-of-goal follow-up not to block, got %v", err)
	}
	if left := readFollowUps(t, root); len(left) != 1 || left[0]["title"] != "logging" ||
		left[0]["test"] != "test -f log.txt\n" || left[0]["verified"] != true {
		t.Fatalf("want unit 2's tiny out-of-goal follow-up in follow-ups.json, got %v", left)
	}
}
