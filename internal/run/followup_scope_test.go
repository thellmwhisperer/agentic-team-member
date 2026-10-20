package run

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// androidIssue declares its scope, android/** and the plugin, and a criterion a follow-up may quote.
const androidIssue = "# Android card route\nType: feature\n\nScope: android/** and the plugin only.\n\n" +
	"## Acceptance\n\n1. The android card opens the route.\n"

// edgeIssue declares its scope, an edge and a dialog, and reserves three paths to another owner.
const edgeIssue = "# Edge dialog\nType: feature\n\nScope: the edge function and its dialog.\n\n" +
	"Reserved to another owner: steps/index.ts, whitelabel/config.ts and Vehicle.tsx.\n\n" +
	"## Acceptance\n\n1. The dialog calls the edge.\n"

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

// scopeResume is scopeRun on a repository whose lint fails, then, once it passes, the resume of that failed run as
// run resumed, its resume point holding followUp as the follow-up it chained, red, with legacy in it: its
// scope review, and its cause, when it blocked, as a run kept it before follow-ups were retired. It checks the
// failed run stopped at lint and the resume did the issue's unit alone, and returns what scopeRun does of it.
func scopeResume(t *testing.T, issue, red, followUp string, legacy obj, env ...string) (root, runDir, call string,
	out *bytes.Buffer, err error) {
	t.Helper()
	root, runDir, call, failed, err := scopeRunAs(t, atmSet(scopeATM, "lint", "false"), "failed", issue, red,
		followUp, env...)
	if err == nil {
		t.Fatal("want the unit's lint to fail")
	}
	failedUnit(t, root, "failed", failed, "lint")
	commitConfig(t, root, scopeATM)
	out, saved, err := legacyResume(t, root, "failed", "resumed", issue, obj{"follow_up": chained(t, followUp, legacy)})
	resumedAlone(t, root, "resumed", out, err, saved)
	return root, filepath.Join(filepath.Dir(runDir), "resumed"), call, out, err
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

// freshAndResumed runs issue, its unit 1 declaring followUp, red, env set, as scopeRun, then as scopeResume with
// legacy. Each must do the issue's unit alone, check then looking at its directory and events.
func freshAndResumed(t *testing.T, issue, red, followUp string, legacy obj, env []string,
	check func(runDir string, out *bytes.Buffer)) {
	t.Helper()
	root, runDir, call, out, err := scopeRun(t, issue, red, followUp, env...)
	noSecondWorker(t, err, root, runDir, call, out)
	check(runDir, out)
	root, runDir, call, out, err = scopeResume(t, issue, red, followUp, legacy, env...)
	noSecondWorker(t, err, root, runDir, call, out)
	check(runDir, out)
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

func TestFollowUpScopeBlocksBeforeTheWorker(t *testing.T) {
	const android, edge = "The android card opens the route.", "The dialog calls the edge."
	cases := map[string]struct{ issue, criterion, paths, path, clause string }{
		"journey outside the scope": {androidIssue, android, `["android/Card.kt","apps/app/src/journey.tsx"]`,
			"apps/app/src/journey.tsx", "Scope: android/** and the plugin only."},
		"UI vitest config outside the scope": {androidIssue, android, `["packages/ui/vitest.config.ts"]`,
			"packages/ui/vitest.config.ts", "Scope: android/** and the plugin only."},
		"reserved path an integration needs": {edgeIssue, edge, `["supabase/functions/edge/index.ts","steps/index.ts"]`,
			"steps/index.ts", "Reserved to another owner: steps/index.ts, whitelabel/config.ts and Vehicle.tsx."},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			report := `{"disposition": "blocked", "paths": {paths}, "findings": [{"path": "` + c.path +
				`", "clause": "` + c.clause + `", "reason": "outside the issue's scope"}], "summary": "s"}`
			var blocked obj
			if err := json.Unmarshal([]byte(strings.Replace(report, "{paths}", c.paths, 1)), &blocked); err != nil {
				t.Fatal(err)
			}
			legacy := obj{"scope": obj{"paths": blocked["paths"], "approved": false, "report": blocked,
				"reason": c.path + " is outside " + c.clause}, "cause": c.criterion + " blocked: " + c.path}
			freshAndResumed(t, c.issue, "c1_test.sh", scopeFollowUp("c1_test.sh", c.criterion, c.paths), legacy,
				[]string{"FAKE_SCOPE_REPORT", report}, func(_ string, out *bytes.Buffer) {
					if strings.Contains(out.String(), "outside the issue's scope") || strings.Contains(out.String(), c.clause) {
						t.Fatalf("want neither the block nor its clause in the run, got\n%s", out)
					}
				})
		})
	}
}

func TestFollowUpScopeReviewsTheRedTestPath(t *testing.T) {
	issue := "# Android card route\nType: feature\n\nScope: android/Card.kt and c1_test.sh only.\n\n" +
		"## Acceptance\n\n1. The android card opens the route.\n"
	legacy := obj{"scope": obj{"paths": []string{"android/Card.kt", "packages/ui/c1_test.sh"}, "approved": false,
		"reason": "packages/ui/c1_test.sh is outside Scope: android/Card.kt and c1_test.sh only."}}
	freshAndResumed(t, issue, "packages/ui/c1_test.sh", scopeFollowUp("packages/ui/c1_test.sh",
		"The android card opens the route.", `["android/Card.kt"]`), legacy, []string{"FAKE_SCOPE", "issue"},
		func(runDir string, _ *bytes.Buffer) {
			if strings.Contains(briefOf(t, runDir), "packages/ui/c1_test.sh") {
				t.Fatalf("want the red test's path in no brief, got\n%s", briefOf(t, runDir))
			}
		})
}

func TestFollowUpScopeApprovesAnInScopePlan(t *testing.T) {
	legacy := obj{"scope": obj{"paths": []string{"a.txt", "c1_test.sh"}, "approved": true}}
	freshAndResumed(t, androidIssue, "c1_test.sh",
		scopeFollowUp("c1_test.sh", "The android card opens the route.", `["a.txt"]`), legacy, nil,
		func(runDir string, _ *bytes.Buffer) {
			if _, err := os.Stat(filepath.Join(runDir, "brief-unit-2.md")); !os.IsNotExist(err) {
				t.Fatalf("want no brief for a unit 2, got %v", err)
			}
			var report struct{ Units []obj }
			b, _ := os.ReadFile(filepath.Join(runDir, "report.json"))
			if json.Unmarshal(b, &report) != nil || len(report.Units) != 1 || report.Units[0]["result"] != "passed" ||
				report.Units[0]["green"] == nil || report.Units[0]["test_file"] != "a_test.sh" {
				t.Fatalf("want the issue's unit alone proven, on its own test, got %s", b)
			}
		})
}

func TestFollowUpScopeFailsClosed(t *testing.T) {
	approval := `{"disposition": "approved", "paths": {paths}, "findings": [], "summary": "s"}`
	cases := map[string]struct{ paths, key, value, why string }{
		"no proposed paths": {`[]`, "FAKE_SCOPE", "", "no proposed paths"},
		"no report":         {`["a.txt"]`, "FAKE_SCOPE", "no-report", "agent left no report"},
		"malformed":         {`["a.txt"]`, "FAKE_SCOPE_REPORT", `{"disposition": "approved"}`, "invalid scope review"},
		"unclosed array":    {`["a.txt"]`, "FAKE_SCOPE_REPORT", "[" + approval, "invalid scope review"},
		"trailing text":     {`["a.txt"]`, "FAKE_SCOPE_REPORT", approval + " ok", "invalid scope review"},
		"stale paths": {`["a.txt"]`, "FAKE_SCOPE_REPORT", strings.Replace(approval, "{paths}", `["a.txt"]`, 1),
			"not the proposed"},
		"approved with a finding": {`["a.txt"]`, "FAKE_SCOPE_REPORT", `{"disposition": "approved", "paths": {paths}, ` +
			`"findings": [{"path": "a.txt", "clause": "Scope: android/** and the plugin only.", "reason": "r"}], ` +
			`"summary": "s"}`, "invalid scope review"},
		"reviewer edits": {`["a.txt"]`, "FAKE_SCOPE_WORK", "echo changed >> a.txt", "scope reviewer changed the clone"},
		"reviewer commits": {`["a.txt"]`, "FAKE_SCOPE_WORK",
			"git -c user.name=r -c user.email=r@example.com commit -q --allow-empty -m moved", "scope reviewer"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var paths []string
			if err := json.Unmarshal([]byte(c.paths), &paths); err != nil {
				t.Fatal(err)
			}
			// What a run kept of a review that failed closed: its reason, as the cause of the blocked follow-up.
			legacy := obj{"scope": obj{"paths": append(paths, "c1_test.sh"), "approved": false, "reason": c.why},
				"cause": c.why}
			// No reviewer runs to edit, commit or report: noSecondWorker delivers unit 1's work, as it made it.
			freshAndResumed(t, androidIssue, "c1_test.sh",
				scopeFollowUp("c1_test.sh", "The android card opens the route.", c.paths), legacy,
				[]string{c.key, c.value}, func(_ string, out *bytes.Buffer) {
					if strings.Contains(out.String(), c.why) {
						t.Fatalf("want no %q in the run, got\n%s", c.why, out)
					}
				})
		})
	}
}

func TestFollowUpScopeBlocksAnUnplannedPathAcrossResume(t *testing.T) {
	legacy := obj{"scope": obj{"paths": []string{"a.txt", "c1_test.sh"}, "approved": true},
		"cause": "unit 2 changes b.txt, outside the paths a.txt, c1_test.sh", "diff": "+u2\n+extra\n"}
	freshAndResumed(t, androidIssue, "c1_test.sh",
		scopeFollowUp("c1_test.sh", "The android card opens the route.", `["a.txt"]`), legacy,
		[]string{"ATM_TEST_UNIT2", "echo u2 >> a.txt; echo extra > b.txt"}, func(_ string, out *bytes.Buffer) {
			if strings.Contains(out.String(), "b.txt") {
				t.Fatalf("want no unit 2 to change b.txt, nor its discrepancy, got\n%s", out)
			}
		})
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
	// A chained follow-up an older run kept, its scope approved, does not stand in for the issue.
	saved["follow_up"] = obj{"title": "gap", "red_test": "c1_test.sh", "criterion": "The android card opens the route.",
		"test": "grep -q u2 a.txt\n", "verified": true, "scope": obj{"paths": []string{"a.txt", "c1_test.sh"},
			"approved": true}}
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

func TestFollowUpScopeRequiresExactPlannedFiles(t *testing.T) {
	for _, plan := range []string{".", "android", ""} {
		t.Run("plan="+plan, func(t *testing.T) {
			paths, _ := json.Marshal([]string{"a.txt", plan})
			legacy := obj{"scope": obj{"paths": []string{"a.txt", plan, "c1_test.sh"}, "approved": true},
				"cause": "unit 2 changes android/unplanned.txt, outside the paths"}
			freshAndResumed(t, androidIssue, "c1_test.sh",
				scopeFollowUp("c1_test.sh", "The android card opens the route.", string(paths)), legacy,
				[]string{"ATM_TEST_UNIT2", "echo u2 >> a.txt; mkdir -p android; echo extra > android/unplanned.txt"},
				func(_ string, out *bytes.Buffer) {
					if strings.Contains(out.String(), "unplanned.txt") {
						t.Fatalf("want no unit 2 to change android/unplanned.txt, got\n%s", out)
					}
				})
		})
	}
}

func TestFollowUpScopeBlocksUnplannedCheckChanges(t *testing.T) {
	for _, command := range []string{"install", "test", "typecheck", "lint"} {
		for _, status := range []string{"true", "false"} {
			t.Run(command+"/"+status, func(t *testing.T) {
				line := "if [ -f c1_test.sh ]; then echo extra > b.txt; " + status + "; fi"
				fixed := atmSet(atmSet(atmYAML, command, line), "delivery", `ls > "$ATM_TEST_DELIVERED"`)
				root := repo(t, "https://example.com/owner/repo.git", fixed)
				delivered := filepath.Join(t.TempDir(), "delivered.txt")
				t.Setenv("ATM_TEST_DELIVERED", delivered)
				t.Setenv("FAKE_AGENT_WORK", scopeWork)
				t.Setenv("ATM_TEST_RED", "c1_test.sh")
				t.Setenv("ATM_TEST_FOLLOWUP", scopeFollowUp("c1_test.sh",
					"The android card opens the route.", `["a.txt"]`))
				t.Setenv("ATM_TEST_UNIT2", "echo u2 >> a.txt")
				var out bytes.Buffer
				err := Run("t", []string{issueFile(t, androidIssue)}, &out, io.Discard)
				if err != nil {
					t.Fatalf("want the issue's unit alone, nothing blocked, got %v", err)
				}
				oneUnit(t, &out)
				assertUnplannedCheckEvidence(t, root, "t", delivered)
				if status == "false" { // a configured command that fails still blocks delivery, on its own reason
					commitConfig(t, root, atmSet(fixed, command, "echo extra > b.txt; false"))
					err := Run("blocked", []string{issueFile(t, androidIssue)}, io.Discard, io.Discard)
					if err == nil || !strings.Contains(err.Error(), command+":") {
						t.Fatalf("want the command failure preserved, got %v", err)
					}
					if b, _ := os.ReadFile(delivered); string(b) != "" {
						t.Fatalf("blocked: delivery ran despite the failing %s:\n%s", command, b)
					}
				}
				// A run that failed at another check, resumed with the follow-up an older run chained: no check
				// finds its red test in place.
				other := map[bool]string{true: "typecheck", false: "lint"}[command == "lint"]
				commitConfig(t, root, atmSet(fixed, other, "false"))
				var failed bytes.Buffer
				if err := Run("failed", []string{issueFile(t, androidIssue)}, &failed, io.Discard); err == nil {
					t.Fatalf("want the unit's %s to fail", other)
				}
				failedUnit(t, root, "failed", &failed, other)
				commitConfig(t, root, fixed)
				resumed, saved, err := legacyResume(t, root, "failed", "resumed", androidIssue, obj{"follow_up": obj{
					"title": "gap", "red_test": "c1_test.sh", "criterion": "The android card opens the route.",
					"paths": []string{"a.txt"}, "test": "grep -q u2 a.txt\n", "verified": true,
					"scope": obj{"paths": []string{"a.txt", "c1_test.sh"}, "approved": true}}})
				resumedAlone(t, root, "resumed", resumed, err, saved)
				assertUnplannedCheckEvidence(t, root, "resumed", delivered)
			})
		}
	}
}

// assertUnplannedCheckEvidence checks run label of root delivered its one unit, which no red test of a follow-up
// joined, so no check wrote b.txt.
func assertUnplannedCheckEvidence(t *testing.T, root, label, delivered string) {
	t.Helper()
	var report struct {
		Units []unitResult `json:"units"`
	}
	b, err := os.ReadFile(filepath.Join(root, ".atm", "runs", label, "report.json"))
	if err != nil || json.Unmarshal(b, &report) != nil || len(report.Units) != 1 ||
		report.Units[0].TestFile != "a_test.sh" {
		t.Fatalf("%s: want one unit, on the issue's test, got %s, %v", label, b, err)
	}
	if b, err := os.ReadFile(delivered); err != nil || strings.Contains(string(b), "c1_test.sh") ||
		strings.Contains(string(b), "b.txt") || !strings.Contains(string(b), "a_test.sh") {
		t.Fatalf("%s: want delivery without a follow-up's red test or what it set off, got %v:\n%s", label, err, b)
	}
	if err := os.Remove(delivered); err != nil {
		t.Fatal(err)
	}
}

// TestFollowUpScopeBriefNamesItsScratch: no scope reviewer runs, so no scope scratch directory is made and nothing
// writes the tickets or temporary file a reviewer did; the worker's brief names the worker's scratch alone, its
// TMPDIR.
func TestFollowUpScopeBriefNamesItsScratch(t *testing.T) {
	legacy := obj{"scope": obj{"paths": []string{"a.txt", "c1_test.sh"}, "approved": true}}
	freshAndResumed(t, androidIssue, "c1_test.sh",
		scopeFollowUp("c1_test.sh", "The android card opens the route.", `["a.txt"]`), legacy,
		[]string{"FAKE_REVIEW_SCRATCH", "1"}, func(runDir string, _ *bytes.Buffer) {
			scratch := filepath.Join(runDir, "worker-scratch")
			if !strings.Contains(briefOf(t, runDir), "`"+scratch+"`") {
				t.Fatalf("want the brief to name the worker's scratch %s, got:\n%s", scratch, briefOf(t, runDir))
			}
			for _, name := range []string{"tickets.txt", "tmp.txt", "plan.txt"} {
				if found, _ := filepath.Glob(filepath.Join(filepath.Dir(runDir), "*", "*", name)); len(found) != 0 {
					t.Fatalf("want no %s a scope reviewer wrote, got %v", name, found)
				}
			}
		})
	// The last worker is the failed run's: the resume calls none.
	scratch := filepath.Join(gitT(t, ".", "rev-parse", "--show-toplevel"), ".atm", "runs", "failed", "worker-scratch")
	if env := agentCall(t, os.Getenv("FAKE_AGENT_CALL")).Env; !slices.Contains(env, "TMPDIR="+scratch) {
		t.Fatalf("want the worker's TMPDIR at its scratch %s, got %v", scratch, env)
	}
}
