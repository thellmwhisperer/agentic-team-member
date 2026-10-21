package run

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// androidIssue declares its scope, android/** and the plugin, and a criterion a follow-up may quote.
const androidIssue = "# Android card route\nType: feature\n\nScope: android/** and the plugin only.\n\n" +
	"## Acceptance\n\n1. The android card opens the route.\n"

// scopeWork is a run's work: unit 1 fixes a.txt and declares $ATM_TEST_FOLLOWUP, its red test at $ATM_TEST_RED,
// red until a.txt has u2; the next unit, which finds that test in place, does $ATM_TEST_UNIT2 and declares none.
const scopeWork = `if [ -f "$ATM_TEST_RED" ]; then eval "$ATM_TEST_UNIT2"; ` + noFollowUps + `; else ` +
	`printf 'fixed\n' > a.txt; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh; ` +
	`mkdir -p ".atm/follow-ups/1/$(dirname "$ATM_TEST_RED")"; ` +
	`echo 'grep -q u2 a.txt' > ".atm/follow-ups/1/$ATM_TEST_RED"; ` +
	`printf '{"test_file":"a_test.sh","follow_ups":[%s]}' "$ATM_TEST_FOLLOWUP" > .atm/fake-report.json; fi`

// scopeATM is scopeRun's .atm.yaml: its delivery writes the commits, files and a.txt it delivers to
// $ATM_TEST_DELIVERED.
var scopeATM = atmSet(atmYAML, "delivery",
	`{ git log --format=%s; git ls-files; git show HEAD:a.txt; } > "$ATM_TEST_DELIVERED"`)

// scopeFollowUp is a follow-up quoting criterion, its red test red, proposing paths, a JSON array.
func scopeFollowUp(red, criterion, paths string) string {
	return `{"title":"gap","red_test":"` + red + `","criterion":"` + criterion + `","paths":` + paths + `}`
}

// scopeRun runs issue whose unit 1 declares the follow-up, its red test red, env set as key, value pairs, and
// returns the repository, the run's directory, where the agents' calls went, the events and its error.
func scopeRun(t *testing.T, issue, red, followUp string, env ...string) (root, runDir, call string,
	out *bytes.Buffer, err error) {
	return scopeRunAs(t, scopeATM, "t", issue, red, followUp, env...)
}

// scopeRunAs is scopeRun as run label of a repository whose .atm.yaml is atm.
func scopeRunAs(t *testing.T, atm, label, issue, red, followUp string, env ...string) (root, runDir, call string,
	out *bytes.Buffer, err error) {
	t.Helper()
	root = repo(t, "https://example.com/owner/repo.git", atm)
	call, out = filepath.Join(t.TempDir(), "call.json"), &bytes.Buffer{}
	env = append([]string{"FAKE_AGENT_WORK", scopeWork, "FAKE_AGENT_CALL", call, "ATM_TEST_RED", red,
		"ATM_TEST_FOLLOWUP", followUp, "ATM_TEST_UNIT2", "echo u2 >> a.txt",
		"FAKE_AGENT_ORDER", filepath.Join(filepath.Dir(call), "order"),
		"ATM_TEST_DELIVERED", filepath.Join(filepath.Dir(call), "delivered")}, env...)
	for ; len(env) > 1; env = env[2:] {
		t.Setenv(env[0], env[1])
	}
	err = Run(label, []string{issueFile(t, issue)}, out, io.Discard)
	return root, filepath.Join(gitT(t, root, "rev-parse", "--show-toplevel"), ".atm", "runs", label), call, out, err
}

// chained is followUp, as JSON, as a run that chained it kept it: its red test verified red, legacy in it.
func chained(t *testing.T, followUp string, legacy obj) obj {
	t.Helper()
	var f obj
	if err := json.Unmarshal([]byte(followUp), &f); err != nil {
		t.Fatal(err)
	}
	f["test"], f["verified"] = "grep -q u2 a.txt\n", true
	maps.Copy(f, legacy)
	return f
}

// noSecondWorker checks the run passed with unit 1's worker alone, each worker then the ponytail pass: no scope
// review, no worker of a unit 2 and no follow-ups.json, and unit 1's work alone delivered.
func noSecondWorker(t *testing.T, err error, root, runDir, call string, out *bytes.Buffer) {
	t.Helper()
	if err != nil {
		t.Fatalf("want the issue's unit alone, nothing blocked, got %v", err)
	}
	oneUnit(t, out)
	if _, e := os.Stat(filepath.Join(runDir, "worker-unit2.jsonl")); e == nil {
		t.Fatal("the worker of unit 2 started")
	}
	if _, e := os.Stat(call + ".scope"); !os.IsNotExist(e) {
		t.Fatalf("want no scope review, got %v", e)
	}
	if b, e := os.ReadFile(filepath.Join(filepath.Dir(call), "order")); e != nil || len(b) == 0 ||
		strings.ReplaceAll(string(b), "worker\nponytail\n", "") != "" {
		t.Fatalf("want each worker followed by the ponytail pass, nothing else, got %v:\n%s", e, b)
	}
	if dirs, _ := filepath.Glob(filepath.Join(runDir, "scope-review-unit*")); len(dirs) != 0 {
		t.Fatalf("want no scope review's files, got %v", dirs)
	}
	noFollowUpsFile(t, root, filepath.Base(runDir))
	if rep := readReport(t, filepath.Join(runDir, "report.json")); rep["failed_node"] != "" {
		t.Fatalf("want the run passed, got %v", rep)
	}
	b, e := os.ReadFile(filepath.Join(filepath.Dir(call), "delivered"))
	if title, rest, _ := strings.Cut(string(b), "\n"); e != nil || !strings.HasPrefix(title, "chore: ") ||
		strings.ReplaceAll(rest, "config\n", "") != "init\n.atm.yaml\na.txt\na_test.sh\nfixed\n" {
		t.Fatalf("want unit 1's work alone delivered, got %v:\n%s", e, b)
	}
}

// briefOf is the brief.md of the run in runDir.
func briefOf(t *testing.T, runDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFollowUpScopeResumeReviewsTheOriginalIssue(t *testing.T) {
	original := "# Android card route\nType: feature\n\nScope: a.txt and c1_test.sh only.\n\n" +
		"## Acceptance\n\n1. The android card opens the route.\n"
	changed := strings.Replace(original, "a.txt and", "a.txt, b.txt and", 1)
	followUp := scopeFollowUp("c1_test.sh", "The android card opens the route.", `["a.txt","b.txt"]`)
	root, runDir, call, out, err := scopeRun(t, original, "c1_test.sh", followUp, "FAKE_SCOPE", "issue")
	noSecondWorker(t, err, root, runDir, call, out)
	// A run that fails its unit's checks resumes from them.
	commitConfig(t, root, atmSet(scopeATM, "lint", "false"))
	var failed bytes.Buffer
	if err := Run("failed", []string{issueFile(t, original)}, &failed, io.Discard); err == nil {
		t.Fatal("want unit 1's checks failed")
	}
	failedUnit(t, root, "failed", &failed, "lint")
	commitConfig(t, root, scopeATM)
	f := chained(t, followUp, obj{"scope": obj{"paths": []string{"a.txt", "b.txt", "c1_test.sh"}, "approved": false,
		"reason": "b.txt is outside Scope: a.txt and c1_test.sh only."}})
	resumed, saved, err := legacyResume(t, root, "failed", "resumed", changed, obj{"follow_up": f})
	resumedAlone(t, root, "resumed", resumed, err, saved)
	dir := filepath.Join(filepath.Dir(runDir), "resumed")
	noSecondWorker(t, err, root, dir, call, resumed)
	if b := briefOf(t, dir); !strings.Contains(b, "a.txt and") || strings.Contains(b, "b.txt and") {
		t.Fatalf("want the resumed run on the original issue, got:\n%s", b)
	}
}

func TestFollowUpScopeResumeRequiresSavedOriginalIssue(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "lint", "false"))
	t.Setenv("FAKE_AGENT_WORK", scopeWork)
	t.Setenv("ATM_TEST_RED", "c1_test.sh")
	t.Setenv("ATM_TEST_FOLLOWUP", scopeFollowUp("c1_test.sh", "The android card opens the route.", `["a.txt"]`))
	err := Run("t", []string{issueFile(t, androidIssue)}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "lint") {
		t.Fatalf("want a resumable run failed at lint, got %v", err)
	}
	path := filepath.Join(root, ".atm", "runs", "t", "resume", "resume.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved obj
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	delete(saved, "original")
	b, err = json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(androidIssue, "android/** and the plugin only", "all files", 1)
	var out bytes.Buffer
	err = Run("resumed", []string{"--resume", "t", issueFile(t, changed)}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "saved original issue text is missing") {
		t.Fatalf("want resume blocked without the original issue, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("want no run steps without the original issue, got %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".atm", "runs", "resumed")); !os.IsNotExist(err) {
		t.Fatalf("want no resumed run created, got %v", err)
	}
}
