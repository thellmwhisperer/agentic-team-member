package run

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
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

// legacyDependency is the commit a blocking child kept as its dependency, before follow-ups were retired.
const legacyDependency = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// chainEnv sets chainWork, which once made three units, unit 3's follow-up quoting last, as the agents' work, the
// delivered log and status going to the file it returns.
func chainEnv(t *testing.T, last string) string {
	t.Helper()
	delivered := filepath.Join(t.TempDir(), "delivered")
	for k, v := range map[string]string{"ATM_TEST_OUT": delivered, "ATM_TEST_LAST": last,
		"ATM_TEST_CRITERION": "A retry runs once after a timeout.", "FAKE_PONYTAIL_REPORT": cut,
		"FAKE_AGENT_WORK":    `if [ -f c2_test.sh ]; then ATM_TEST_CRITERION="$ATM_TEST_LAST"; fi; ` + chainWork,
		"FAKE_PONYTAIL_WORK": "grep -v extra a.txt > cut; mv cut a.txt"} {
		t.Setenv(k, v)
	}
	return delivered
}

// threeUnits runs chainWork, which once made three units, unit 3's follow-up quoting last, the delivered log and
// status going to the file it returns.
func threeUnits(t *testing.T, last string) (root, delivered string, out *bytes.Buffer, err error) {
	t.Helper()
	delivered, out = chainEnv(t, last), &bytes.Buffer{}
	root = repo(t, "https://example.com/owner/repo.git",
		atmSet(atmYAML, "delivery", `{ git log --format=%s; git status --porcelain; } > "$ATM_TEST_OUT"`))
	return root, delivered, out, Run("t", []string{issueFile(t, followUpIssue)}, out, io.Discard)
}

// failedAtLint runs followUpIssue, its work $FAKE_AGENT_WORK, as run failed of a repository whose lint fails, and
// checks it failed there, delivering nothing. It returns the repository, which then has a lint that passes, the
// file its delivery writes, its log, files and status, and the failed run's events.
func failedAtLint(t *testing.T) (root, delivered string, out *bytes.Buffer) {
	t.Helper()
	delivered, out = filepath.Join(t.TempDir(), "delivered"), &bytes.Buffer{}
	t.Setenv("ATM_TEST_OUT", delivered)
	deliver := atmSet(atmYAML, "delivery",
		`{ git log --format=%s; git ls-files; git status --porcelain; } > "$ATM_TEST_OUT"`)
	root = repo(t, "https://example.com/owner/repo.git", atmSet(deliver, "lint", "false"))
	if err := Run("failed", []string{issueFile(t, followUpIssue)}, out, io.Discard); err == nil {
		t.Fatal("want the unit's lint to fail")
	}
	failedUnit(t, root, "failed", out, "lint")
	if _, err := os.Stat(delivered); !os.IsNotExist(err) {
		t.Fatalf("want no delivery of a failed run, got %v", err)
	}
	commitConfig(t, root, deliver)
	return root, delivered, out
}

// resumeDelivers resumes run failed of root as run resumed, fields in its resume point, checks it resumed the
// issue's unit alone, under no budget and blocked by nothing, and returns what it delivered.
func resumeDelivers(t *testing.T, root, delivered string, fields obj) string {
	t.Helper()
	out, saved, err := legacyResume(t, root, "failed", "resumed", followUpIssue, fields)
	resumedAlone(t, root, "resumed", out, err, saved)
	b, err := os.ReadFile(delivered)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// reportedFollowUps is the follow-ups the one agent in out reported.
func reportedFollowUps(t *testing.T, out *bytes.Buffer) any {
	t.Helper()
	report, _ := stateEnds(t, out, "agent")[0]["report"].(obj)
	return report["follow_ups"]
}

// chainedGap is chainWork's unit k's follow-up, as JSON reads it back, quoting criterion.
func chainedGap(k, criterion string) obj {
	return obj{"title": "gap " + k, "red_test": "c" + k + "_test.sh", "criterion": criterion}
}

// primaryGap is primaryWork's follow-up as a run that chained it kept it, with extra, what it kept on its unit.
func primaryGap(extra obj) obj {
	f := obj{"title": "gap", "red_test": "c1_test.sh", "criterion": "A retry runs once after a timeout.",
		"test": "grep -q u2 a.txt\n"}
	maps.Copy(f, extra)
	return f
}

// oneUnitRun runs followUpIssue as run t of root and checks it did the issue's unit alone, nothing blocking it as
// want says, and wrote no follow-ups.json.
func oneUnitRun(t *testing.T, root, want string) {
	t.Helper()
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatalf("want %s, got %v", want, err)
	}
	oneUnit(t, &out)
	noFollowUpsFile(t, root, "t")
}

func TestRunDeliversThreeUnitsWhenTheLastFollowUpQuotesNoCriterion(t *testing.T) {
	root, delivered, out, err := threeUnits(t, "Errors are logged.")
	if err != nil {
		t.Fatal(err)
	}
	oneUnit(t, out)
	b, _ := os.ReadFile(delivered)
	if string(b) != "ponytail: 1 cuts\nchore: Retry on timeout\ninit\n" {
		t.Fatalf("want the issue's unit committed, then the cut, got\n%s", b)
	}
	want := []any{chainedGap("1", "A retry runs once after a timeout.")}
	if got := reportedFollowUps(t, out); !reflect.DeepEqual(got, want) ||
		strings.Contains(out.String(), "Errors are logged.") {
		t.Fatalf("want unit 1's follow-up as reported and no unit 3's, got %v", got)
	}
	noFollowUpsFile(t, root, "t")

	chainEnv(t, "Errors are logged.")
	root, delivered, _ = failedAtLint(t)
	got := resumeDelivers(t, root, delivered, obj{"follow_up": chainedGap("2", "A retry runs once after a timeout."),
		"pending": []obj{{"title": "gap 3", "red_test": "c3_test.sh", "criterion": "Errors are logged.",
			"test": "grep -q u4 a.txt\n", "verified": true}}, "gross_lines": 2})
	if !strings.HasPrefix(got, "ponytail: 1 cuts\nchore: Retry on timeout\ninit\n.atm.yaml\n") ||
		!strings.HasSuffix(got, "\na.txt\na_test.sh\n") {
		t.Fatalf("want the resumed issue's unit committed, then the cut, and unit 3's follow-up nowhere, got\n%s", got)
	}
}

func TestRunBlocksAnUnmetCriterionWithItsEvidence(t *testing.T) {
	root, delivered, out, err := threeUnits(t, "A retry runs once after a timeout.")
	if err != nil {
		t.Fatalf("want a follow-up quoting a criterion to block nothing, got %v", err)
	}
	oneUnit(t, out)
	if b, err := os.ReadFile(delivered); err != nil || !strings.HasPrefix(string(b), "ponytail: 1 cuts\nfix:") {
		t.Fatalf("want the run delivered, got %v\n%s", err, b)
	}
	noFollowUpsFile(t, root, "t")

	chainEnv(t, "A retry runs once after a timeout.")
	root, delivered, _ = failedAtLint(t)
	unmet := chainedGap("3", "A retry runs once after a timeout.")
	unmet["test"], unmet["verified"], unmet["diff"] = "grep -q u4 a.txt\n", true, "+u3\n"
	unmet["cause"], unmet["dependency"] = "unit 3 leaves A retry runs once after a timeout. unmet", legacyDependency
	if got := resumeDelivers(t, root, delivered, obj{"pending": []obj{unmet}}); !strings.HasPrefix(got,
		"ponytail: 1 cuts\nchore: Retry on timeout\ninit\n") || strings.Contains(got, "c3_test.sh") {
		t.Fatalf("want the resumed run delivered, the unmet criterion it kept blocking nothing, got\n%s", got)
	}
}

func TestRunExemptsThePrimaryUnitFromTheGrossBudget(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("ATM_TEST_PRIMARY", "seq 1 300 > big.txt")
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then echo u2 >> a.txt; `+noFollowUps+`; else `+
		primaryWork+`; fi`)
	oneUnitRun(t, root, "300 lines of the issue's unit under no budget")

	root, delivered, _ := failedAtLint(t)
	got := resumeDelivers(t, root, delivered, obj{"follow_up": primaryGap(nil), "gross_lines": 0})
	if got != "chore: Retry on timeout\ninit\n.atm.yaml\na.txt\na_test.sh\nbig.txt\n" {
		t.Fatalf("want the resumed 300 primary lines delivered, no unit 2 after them, got\n%s", got)
	}
}

func TestRunBlocksAdditionalWorkOverTheGrossBudgetThoughItsNetIsSmall(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("ATM_TEST_PRIMARY", "seq 1 81 > big.txt")
	// A unit 2 would delete 81 lines and add 120: net 39, gross 201. None runs.
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then : > big.txt; seq 1 119 > more.txt; `+
		`echo u2 >> a.txt; `+noFollowUps+`; else `+primaryWork+`; fi`)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatalf("want no additional unit and no budget to block it, got %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "report.json"))
	var report struct{ Units []obj }
	if err := json.Unmarshal(b, &report); err != nil || len(report.Units) != 1 ||
		report.Units[0]["base"] != gitT(t, root, "rev-parse", "main") {
		t.Fatalf("report.json: want the issue's unit alone, on the base, got %v\n%s", err, b)
	}
	oneUnit(t, &out)
	noFollowUpsFile(t, root, "t")

	root, delivered, _ := failedAtLint(t)
	got := resumeDelivers(t, root, delivered, obj{"follow_up": primaryGap(obj{"diff": "-81\n+119\n",
		"cause": "unit 2 changes 201 gross lines, over the 200-line budget", "dependency": legacyDependency}),
		"gross_lines": 201})
	if got != "chore: Retry on timeout\ninit\n.atm.yaml\na.txt\na_test.sh\nbig.txt\n" {
		t.Fatalf("want the resumed unit 1 delivered, 201 kept gross lines blocking nothing, got\n%s", got)
	}
}

func TestRunKeepsTheGrossBudgetAcrossUnitsAndResume(t *testing.T) {
	// A unit 2 would add 150 lines, a unit 3 take 59 of them back and add one: gross 210, net 92. None runs.
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c2_test.sh ]; then tail -n +60 x.txt > y; mv y x.txt; `+
		`echo u3 >> a.txt; `+noFollowUps+`; elif [ -f c1_test.sh ]; then seq 1 149 > x.txt; echo u2 >> a.txt; `+
		`mkdir -p .atm/follow-ups/1; echo 'grep -q u3 a.txt' > .atm/follow-ups/1/c2_test.sh; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"gap 2","red_test":"c2_test.sh",`+
		`"criterion":"A retry runs once after a timeout."}]}' > .atm/fake-report.json; else `+primaryWork+`; fi`)
	root, delivered, failed := failedAtLint(t)
	if strings.Contains(failed.String(), "gross") {
		t.Fatalf("want no budget in the failed run, got\n%s", failed)
	}
	// What resuming unit 3 kept: unit 2's 150 gross lines and the follow-up it chained.
	got := resumeDelivers(t, root, delivered, obj{"follow_up": obj{"title": "gap 2", "red_test": "c2_test.sh",
		"criterion": "A retry runs once after a timeout.", "test": "grep -q u3 a.txt\n", "verified": true},
		"gross_lines": 150})
	if got != "chore: Retry on timeout\ninit\n.atm.yaml\na.txt\na_test.sh\n" {
		t.Fatalf("want the resumed unit 1 alone delivered, under no budget, got\n%s", got)
	}
}

func TestRunPersistsTinyOutOfGoalWorkOfAnAdditionalUnit(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then echo u2 >> a.txt; mkdir -p .atm/follow-ups/1; `+
		`echo 'test -f log.txt' > .atm/follow-ups/1/e_test.sh; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"logging","red_test":"e_test.sh",`+
		`"criterion":"Errors are logged."}]}' > .atm/fake-report.json; else `+primaryWork+`; fi`)
	oneUnitRun(t, root, "an out-of-goal follow-up not to block")

	root, delivered, _ := failedAtLint(t)
	got := resumeDelivers(t, root, delivered, obj{"pending": []obj{{"title": "logging", "red_test": "e_test.sh",
		"criterion": "Errors are logged.", "test": "test -f log.txt\n", "verified": true}}})
	if got != "chore: Retry on timeout\ninit\n.atm.yaml\na.txt\na_test.sh\n" {
		t.Fatalf("want the resumed unit 1 delivered, its kept out-of-goal follow-up persisted nowhere, got\n%s", got)
	}
}

func TestRunKeepsUnmetDependencyInTheResumeBundle(t *testing.T) {
	chainEnv(t, "A retry runs once after a timeout.")
	root, delivered, out := failedAtLint(t)
	clone, _ := ends(t, out, "clone")[0]["clone"].(string)
	if err := os.RemoveAll(clone); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "fetch", "-q", filepath.Join(root, ".atm", "runs", "failed", "resume", "work.bundle"),
		"refs/atm/resume")
	work := gitT(t, root, "rev-parse", "FETCH_HEAD")
	if got := gitT(t, root, "rev-parse", "FETCH_HEAD^"); got != gitT(t, root, "rev-parse", "main^") {
		t.Fatalf("want the kept work recoverable without the clone, on the run's base, got %s", got)
	}
	if got := gitT(t, root, "show", "FETCH_HEAD:a.txt"); got != "fixed\nextra" {
		t.Fatalf("want unit 1's work in the bundle, got %q", got)
	}
	if files := gitT(t, root, "ls-tree", "-r", "--name-only", "FETCH_HEAD"); strings.Contains(files, "c1_test.sh") {
		t.Fatalf("want no follow-up's red test kept for a unit to come, got\n%s", files)
	}
	unmet := chainedGap("3", "A retry runs once after a timeout.")
	unmet["test"], unmet["verified"], unmet["dependency"] = "grep -q u4 a.txt\n", true, work
	if got := resumeDelivers(t, root, delivered, obj{"pending": []obj{unmet}}); !strings.HasPrefix(got,
		"ponytail: 1 cuts\nchore: Retry on timeout\ninit\n") {
		t.Fatalf("want the kept work delivered, its dependency scheduling nothing, got\n%s", got)
	}
}

func TestRunKeepsEvidenceForEveryMandatoryPendingFollowUp(t *testing.T) {
	for _, unit := range []string{"1", "3"} {
		t.Run("pending-from-unit-"+unit, func(t *testing.T) {
			t.Setenv("ATM_TEST_CRITERION", "A retry runs once after a timeout.")
			t.Setenv("FAKE_AGENT_WORK", chainWork+`; if [ "$k" = `+unit+` ]; then `+
				`mkdir -p .atm/follow-ups/2; echo 'test -f error.txt' > .atm/follow-ups/2/error_test.sh; `+
				`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"gap","red_test":"c%s_test.sh",`+
				`"criterion":"A retry runs once after a timeout."},{"title":"error gap",`+
				`"red_test":"error_test.sh","criterion":"The retry error is reported."}]}' "$k" `+
				`> .atm/fake-report.json; fi`)
			root, delivered, out := failedAtLint(t)
			// Unit 1 reports both mandatory gaps; unit 3's, which no run reaches now, are what a resume kept.
			mandatory := []any{obj{"title": "gap", "red_test": "c1_test.sh", "criterion": "A retry runs once after a timeout."},
				obj{"title": "error gap", "red_test": "error_test.sh", "criterion": "The retry error is reported."}}
			var pending []obj
			if unit == "3" {
				mandatory = []any{chainedGap("1", "A retry runs once after a timeout.")}
				for _, f := range []obj{chainedGap("3", "A retry runs once after a timeout."),
					{"title": "error gap", "red_test": "error_test.sh", "criterion": "The retry error is reported."}} {
					f["diff"], f["dependency"], f["verified"], f["test"] = "+u3\n", legacyDependency, true, "false\n"
					f["cause"] = "unit 3 leaves " + f["criterion"].(string) + " unmet"
					pending = append(pending, f)
				}
			}
			if got := reportedFollowUps(t, out); !reflect.DeepEqual(got, mandatory) {
				t.Fatalf("want unit 1's follow-ups as reported, got %v", got)
			}
			if got := resumeDelivers(t, root, delivered, obj{"pending": pending}); got !=
				"chore: Retry on timeout\ninit\n.atm.yaml\na.txt\na_test.sh\n" {
				t.Fatalf("want the resumed unit 1 delivered, no mandatory criterion blocking it, got\n%s", got)
			}
		})
	}
}
