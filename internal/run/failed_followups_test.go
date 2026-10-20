package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// failingUnit2Work is unit 1 fixing a.txt with two follow-ups, c1_test.sh on the issue and c2_test.sh outside
// it; a unit 2, which no follow-up starts any more, would add u2 to a.txt and report c3_test.sh with criterion
// $ATM_TEST_CRITERION. A test line that fails once c1_test.sh is in place would fail unit 2's checks.
const failingUnit2Work = `if [ -f c1_test.sh ]; then echo u2 >> a.txt; mkdir -p .atm/follow-ups/1; ` +
	`echo 'grep -q u3 a.txt' > .atm/follow-ups/1/c3_test.sh; ` +
	`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"unit 2 gap","red_test":"c3_test.sh",` +
	`"criterion":"%s"}]}' "$ATM_TEST_CRITERION" > .atm/fake-report.json; ` +
	`else printf 'fixed\n' > a.txt; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh; ` +
	`mkdir -p .atm/follow-ups/1 .atm/follow-ups/2; echo 'grep -q u2 a.txt' > .atm/follow-ups/1/c1_test.sh; ` +
	`echo 'grep -q extra a.txt' > .atm/follow-ups/2/c2_test.sh; ` +
	`printf '{"test_file":"a_test.sh","follow_ups":[` +
	`{"title":"chained","red_test":"c1_test.sh","criterion":"A retry runs once after a timeout."},` +
	`{"title":"pending","red_test":"c2_test.sh","criterion":"Some other work is documented."}]}' ` +
	`> .atm/fake-report.json; fi`

const failsOnUnit2 = `if [ -f c1_test.sh ]; then false; else sh a_test.sh; fi`

// unit1FollowUps is what failingUnit2Work's unit 1 reports as its follow-ups, as JSON reads them back.
var unit1FollowUps = []any{
	obj{"title": "chained", "red_test": "c1_test.sh", "criterion": "A retry runs once after a timeout."},
	obj{"title": "pending", "red_test": "c2_test.sh", "criterion": "Some other work is documented."},
}

// failedUnit fails the test unless out holds one unit whose checks failed naming why, with no follow-up handling,
// and run label of root wrote no follow-ups.json.
func failedUnit(t *testing.T, root, label string, out *bytes.Buffer, why string) {
	t.Helper()
	agents, checks := stateEnds(t, out, "agent"), stateEnds(t, out, "checks")
	if len(agents) != 1 || len(checks) != 1 || checks[0]["state"] != "failed" || checks[0]["follow_ups"] != nil ||
		!strings.Contains(fmt.Sprint(checks[0]["error"]), why) || strings.Contains(fmt.Sprint(checks[0]["error"]), "follow") {
		t.Fatalf("want the issue's unit failed at checks on %q alone, got %v, %v", why, agents, checks)
	}
	noFollowUpsFile(t, root, label)
}

// keptPoint is the resume.json run label of root kept, after checking it holds nothing of the retired
// follow-ups: no chained one, no pending ones, no gross lines and no proofs of units before.
func keptPoint(t *testing.T, root, label string) obj {
	t.Helper()
	var p obj
	b, err := os.ReadFile(filepath.Join(root, ".atm", "runs", label, "resume", "resume.json"))
	if err != nil || json.Unmarshal(b, &p) != nil {
		t.Fatalf("%s's resume.json: %v\n%s", label, err, b)
	}
	for _, key := range []string{"follow_up", "pending", "gross_lines", "proofs"} {
		if _, ok := p[key]; ok {
			t.Fatalf("%s: want no %s kept, got %s", label, key, b)
		}
	}
	return p
}

// keptFiles is the files of the work run label of root kept, read from its bundle.
func keptFiles(t *testing.T, root, label string) string {
	t.Helper()
	gitT(t, root, "fetch", "-q", filepath.Join(root, ".atm", "runs", label, "resume", "work.bundle"), "refs/atm/resume")
	return gitT(t, root, "ls-tree", "-r", "--name-only", "FETCH_HEAD")
}

func TestAUnitThatFailsChecksKeepsItsFollowUps(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmSet(atmSet(atmYAML, "test", failsOnUnit2), "lint", "false"))
	t.Setenv("ATM_TEST_CRITERION", "The retry error is reported.") // on the issue: still never chained
	t.Setenv("FAKE_AGENT_WORK", failingUnit2Work)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err == nil {
		t.Fatal("want the unit's checks to fail")
	}
	failedUnit(t, root, "t", &out, "lint")
	report, _ := stateEnds(t, &out, "agent")[0]["report"].(obj)
	if !reflect.DeepEqual(report["follow_ups"], unit1FollowUps) || strings.Contains(out.String(), "c3_test.sh") {
		t.Fatalf("want unit 1's two follow-ups as it reported them, in order, and no unit 2's, got %v", report)
	}
	if p := keptPoint(t, root, "t"); !reflect.DeepEqual(p["report"], report) {
		t.Fatalf("want the report kept as the agent wrote it, got %v", p["report"])
	}
	if files := keptFiles(t, root, "t"); files != ".atm.yaml\na.txt\na_test.sh" {
		t.Fatalf("want unit 1's work kept without its follow-ups' tests, got\n%s", files)
	}
}

// failsOnTestFile checks that the unit of a run whose agent does work, reporting an empty test_file, fails at
// checks on it alone, before any delivery, and returns the run's events.
func failsOnTestFile(t *testing.T, work string) *bytes.Buffer {
	t.Helper()
	delivered := filepath.Join(t.TempDir(), "delivered")
	t.Setenv("ATM_TEST_OUT", delivered)
	root := repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery", `touch "$ATM_TEST_OUT"`))
	t.Setenv("FAKE_AGENT_WORK", work)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err == nil {
		t.Fatal("want checks to reject the empty test_file")
	}
	failedUnit(t, root, "t", &out, "test_file")
	if _, err := os.Stat(delivered); !os.IsNotExist(err) {
		t.Fatalf("want no delivery, got %v", err)
	}
	return &out
}

func TestUnit1ThatFailsChecksKeepsItsFollowUps(t *testing.T) {
	out := failsOnTestFile(t, fixWork+"; mkdir -p .atm/follow-ups/1; "+
		"echo 'grep -q restored a.txt' > .atm/follow-ups/1/r_test.sh; "+
		`printf '{"test_file":"","follow_ups":[{"title":"atomic restore","red_test":"r_test.sh",`+
		`"criterion":"Restores are atomic."}]}' > .atm/fake-report.json`)
	report, _ := stateEnds(t, out, "agent")[0]["report"].(obj)
	want := []any{obj{"title": "atomic restore", "red_test": "r_test.sh", "criterion": "Restores are atomic."}}
	if !reflect.DeepEqual(report["follow_ups"], want) {
		t.Fatalf("want unit 1's follow-up as it reported it, unverified, got %v", report)
	}
}

func TestInvalidFollowUpsDoNotHideAChecksFailure(t *testing.T) {
	out := failsOnTestFile(t,
		fixWork+`; mkdir -p .atm; printf '{"test_file":"","follow_ups":"later"}' > .atm/fake-report.json`)
	reason, _ := stateEnds(t, out, "checks")[0]["error"].(string)
	if first, rest, _ := strings.Cut(reason, "\n"); !strings.Contains(first, "test_file") || rest != "" {
		t.Fatalf("want the checks reason alone, nothing on the follow-ups next to it, got %q", reason)
	}
	if report, _ := stateEnds(t, out, "agent")[0]["report"].(obj); report["follow_ups"] != "later" {
		t.Fatalf("want the invalid follow_ups left in the report as written, got %v", report)
	}
}

func TestResumeKeepsAFailedUnitsFollowUpsUntilItsReportReplacesThem(t *testing.T) {
	delivered := filepath.Join(t.TempDir(), "delivered")
	t.Setenv("ATM_TEST_OUT", delivered)
	deliver := atmSet(atmSet(atmYAML, "test", failsOnUnit2), "delivery",
		`{ git log --format=%s; git ls-files; git show HEAD:a.txt; } > "$ATM_TEST_OUT"`)
	root := repo(t, "https://example.com/owner/repo.git", atmSet(deliver, "lint", "false"))
	t.Setenv("ATM_TEST_CRITERION", "Unit two's gap is documented.")
	t.Setenv("FAKE_AGENT_WORK", failingUnit2Work)
	issue := issueFile(t, followUpIssue)
	var failed bytes.Buffer
	if err := Run("failed", []string{issue}, &failed, io.Discard); err == nil {
		t.Fatal("want the unit's checks to fail")
	}
	failedUnit(t, root, "failed", &failed, "lint")
	kept, _ := keptPoint(t, root, "failed")["report"].(obj)
	if !reflect.DeepEqual(kept["follow_ups"], unit1FollowUps) {
		t.Fatalf("want unit 1's follow-ups kept in its report, got %v", kept)
	}
	gitT(t, root, "config", "--global", "core.autocrlf", "true")
	again, saved, err := legacyResume(t, root, "failed", "again", followUpIssue, obj{
		"follow_up": obj{"title": "chained", "red_test": "c1_test.sh", "criterion": "A retry runs once after a timeout.",
			"test": "grep -q u2 a.txt\n", "verified": true},
		"pending": []obj{{"title": "pending", "red_test": "c2_test.sh", "criterion": "Some other work is documented.",
			"test": "grep -q extra a.txt\n", "verified": true}},
		"gross_lines": 1})
	if err == nil {
		t.Fatal("want the unit's checks to fail again")
	}
	failedUnit(t, root, "again", again, "lint")
	if !reflect.DeepEqual(saved, kept) || !reflect.DeepEqual(keptPoint(t, root, "again")["report"], kept) {
		t.Fatalf("want the report kept, once, as unit 1 wrote it, got %v", keptPoint(t, root, "again")["report"])
	}
	if _, err := os.Stat(delivered); !os.IsNotExist(err) {
		t.Fatalf("want no delivery of a failed run, got %v", err)
	}
	commitConfig(t, root, deliver) // the resume reads it from the base ref
	resumed, saved, err := legacyResume(t, root, "again", "resumed", followUpIssue, obj{
		"pending": []obj{{"title": "unit 2 gap", "red_test": "c3_test.sh", "criterion": "Unit two's gap is documented.",
			"test": "grep -q u3 a.txt\n", "verified": false}}})
	resumedAlone(t, root, "resumed", resumed, err, saved)
	if !reflect.DeepEqual(saved, kept) {
		t.Fatalf("want the report unit 1 wrote resumed, got %v", saved)
	}
	b, err := os.ReadFile(delivered)
	if err != nil || string(b) != "fix: Retry on timeout\ninit\n.atm.yaml\na.txt\na_test.sh\nfixed\n" {
		t.Fatalf("want unit 1's kept work alone delivered, got %v:\n%s", err, b)
	}
}
