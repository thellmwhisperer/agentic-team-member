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

// scopeFollowUp is a follow-up quoting criterion, its red test red, proposing paths, a JSON array.
func scopeFollowUp(red, criterion, paths string) string {
	return `{"title":"gap","red_test":"` + red + `","criterion":"` + criterion + `","paths":` + paths + `}`
}

// scopeRun runs issue whose unit 1 declares the follow-up, its red test red, env set as key, value pairs, and
// returns the repository, the run's directory, where the agents' calls went, the events and its error.
func scopeRun(t *testing.T, issue, red, followUp string, env ...string) (root, runDir, call string,
	out *bytes.Buffer, err error) {
	t.Helper()
	root = repo(t, "https://example.com/owner/repo.git", atmYAML)
	call, out = filepath.Join(t.TempDir(), "call.json"), &bytes.Buffer{}
	env = append([]string{"FAKE_AGENT_WORK", scopeWork, "FAKE_AGENT_CALL", call, "ATM_TEST_RED", red,
		"ATM_TEST_FOLLOWUP", followUp, "ATM_TEST_UNIT2", "echo u2 >> a.txt"}, env...)
	for ; len(env) > 1; env = env[2:] {
		t.Setenv(env[0], env[1])
	}
	err = Run("t", []string{issueFile(t, issue)}, out, io.Discard)
	return root, filepath.Join(root, ".atm", "runs", "t"), call, out, err
}

// noSecondWorker checks the run failed naming why and no worker started after unit 1.
func noSecondWorker(t *testing.T, err error, runDir string, out *bytes.Buffer, why ...string) {
	t.Helper()
	for _, w := range why {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Fatalf("want the run blocked naming %q, got %v", w, err)
		}
	}
	if agents := ends(t, bytes.NewBuffer(out.Bytes()), "agent"); len(agents) != 1 {
		t.Fatalf("want only unit 1's worker, got %v", agents)
	}
	if _, e := os.Stat(filepath.Join(runDir, "worker-unit2.jsonl")); e == nil {
		t.Fatal("the worker of unit 2 started")
	}
}

// blockedChild is the one follow-up follow-ups.json holds, after checking it is the blocking child of the
// follow-up of criterion, red test red, with its cause and unit 1's commit as its dependency.
func blockedChild(t *testing.T, root, criterion, red string, out *bytes.Buffer) obj {
	t.Helper()
	left := readFollowUps(t, root)
	clone, _ := ends(t, out, "clone")[0]["clone"].(string)
	if len(left) != 1 || left[0]["criterion"] != criterion || left[0]["red_test"] != red ||
		left[0]["verified"] != true || left[0]["cause"] == nil ||
		left[0]["dependency"] != gitT(t, clone, "rev-parse", "HEAD") ||
		gitT(t, clone, "log", "-1", "--format=%B") == "init" {
		t.Fatalf("want a blocking child of %s with its cause and unit 1's commit, got %v", red, left)
	}
	return left[0]
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
			root, runDir, call, out, err := scopeRun(t, c.issue, "c1_test.sh",
				scopeFollowUp("c1_test.sh", c.criterion, c.paths), "FAKE_SCOPE_REPORT", report)
			noSecondWorker(t, err, runDir, out, c.path, c.clause, c.criterion)
			brief := agentCall(t, call+".scope").Stdin
			if !strings.Contains(brief, c.issue) || !strings.Contains(brief, c.path) {
				t.Fatalf("want the scope reviewer to get the literal issue and the paths, got:\n%s", brief)
			}
			child := blockedChild(t, root, c.criterion, "c1_test.sh", out)
			b, _ := json.Marshal(child["scope"])
			if !strings.Contains(string(b), c.path) || !strings.Contains(string(b), c.clause) ||
				!strings.Contains(child["cause"].(string), c.criterion) {
				t.Fatalf("want the child to keep the paths, clause and reason of the block, got %v", child)
			}
			if rep := readReport(t, filepath.Join(runDir, "report.json")); rep["result"] == "passed" {
				t.Fatalf("want the run not passed, got %v", rep)
			}
		})
	}
}

func TestFollowUpScopeReviewsTheRedTestPath(t *testing.T) {
	issue := "# Android card route\nType: feature\n\nScope: android/Card.kt and c1_test.sh only.\n\n" +
		"## Acceptance\n\n1. The android card opens the route.\n"
	_, runDir, call, out, err := scopeRun(t, issue, "packages/ui/c1_test.sh", scopeFollowUp(
		"packages/ui/c1_test.sh", "The android card opens the route.", `["android/Card.kt"]`), "FAKE_SCOPE", "issue")
	noSecondWorker(t, err, runDir, out, "packages/ui/c1_test.sh")
	if brief := agentCall(t, call+".scope").Stdin; !strings.Contains(brief, `"packages/ui/c1_test.sh"`) {
		t.Fatalf("want the red test among the proposed paths, got:\n%s", brief)
	}
}

func TestFollowUpScopeApprovesAnInScopePlan(t *testing.T) {
	_, runDir, call, out, err := scopeRun(t, androidIssue, "c1_test.sh",
		scopeFollowUp("c1_test.sh", "The android card opens the route.", `["a.txt"]`))
	if err != nil {
		t.Fatal(err)
	}
	if agents := ends(t, out, "agent"); len(agents) != 2 || agents[1]["state"] != "passed" {
		t.Fatalf("want unit 2 run, got %v", agents)
	}
	if brief := agentCall(t, call+".scope").Stdin; !strings.Contains(brief, androidIssue) {
		t.Fatalf("want the scope reviewer to get the issue, got:\n%s", brief)
	}
	b, err := os.ReadFile(filepath.Join(runDir, "brief-unit-2.md"))
	if err != nil || !strings.Contains(string(b), "`a.txt`") || !strings.Contains(string(b), "`c1_test.sh`") {
		t.Fatalf("want unit 2's brief to name its approved paths, got %v:\n%s", err, b)
	}
	var report struct{ Units []obj }
	b, _ = os.ReadFile(filepath.Join(runDir, "report.json"))
	if json.Unmarshal(b, &report) != nil || len(report.Units) != 2 || report.Units[1]["result"] != "passed" ||
		report.Units[1]["green"] == nil {
		t.Fatalf("want unit 2 proven, got %s", b)
	}
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
			root, runDir, _, out, err := scopeRun(t, androidIssue, "c1_test.sh",
				scopeFollowUp("c1_test.sh", "The android card opens the route.", c.paths), c.key, c.value)
			noSecondWorker(t, err, runDir, out, c.why)
			child := blockedChild(t, root, "The android card opens the route.", "c1_test.sh", out)
			if b, _ := json.Marshal(child["scope"]); !strings.Contains(string(b), c.why) {
				t.Fatalf("want the child to keep why, %q, got %s", c.why, b)
			}
		})
	}
}

func TestFollowUpScopeBlocksAnUnplannedPathAcrossResume(t *testing.T) {
	root, _, _, _, err := scopeRun(t, androidIssue, "c1_test.sh",
		scopeFollowUp("c1_test.sh", "The android card opens the route.", `["a.txt"]`),
		"ATM_TEST_UNIT2", "echo u2 >> a.txt; echo extra > b.txt")
	for _, label := range []string{"t", "resumed"} {
		if label == "resumed" {
			err = Run(label, []string{"--resume", "t", issueFile(t, androidIssue)}, io.Discard, io.Discard)
		}
		if err == nil || !strings.Contains(err.Error(), "b.txt") {
			t.Fatalf("%s: want unit 2 blocked on its unplanned b.txt alone, got %v", label, err)
		}
		dir := filepath.Join(root, ".atm", "runs", label)
		var left []obj
		b, _ := os.ReadFile(filepath.Join(dir, "follow-ups.json"))
		if json.Unmarshal(b, &left) != nil || len(left) != 1 || left[0]["red_test"] != "c1_test.sh" ||
			!strings.Contains(left[0]["cause"].(string), "b.txt") ||
			!strings.Contains(left[0]["diff"].(string), "+extra") {
			t.Fatalf("%s: want the discrepancy kept in follow-ups.json, got %s", label, b)
		}
		if rep := readReport(t, filepath.Join(dir, "report.json")); rep["failed_node"] != "checks" {
			t.Fatalf("%s: want no delivery past unit 2's checks, got %v", label, rep["failed_node"])
		}
	}
}

func TestFollowUpScopeResumeReviewsTheOriginalIssue(t *testing.T) {
	original := "# Android card route\nType: feature\n\nScope: a.txt and c1_test.sh only.\n\n" +
		"## Acceptance\n\n1. The android card opens the route.\n"
	changed := strings.Replace(original, "a.txt and", "a.txt, b.txt and", 1)
	root, _, call, _, err := scopeRun(t, original, "c1_test.sh",
		scopeFollowUp("c1_test.sh", "The android card opens the route.", `["a.txt","b.txt"]`),
		"FAKE_SCOPE", "issue")
	if err == nil {
		t.Fatal("want the first run blocked on b.txt, outside the original issue")
	}
	// A run that fails its first unit's checks, before any scope review, resumes from them.
	if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(atmSet(atmYAML, "lint", "false")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run("failed", []string{issueFile(t, original)}, io.Discard, io.Discard); err == nil {
		t.Fatal("want unit 1's checks failed")
	}
	if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(atmYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(call + ".scope"); err != nil {
		t.Fatal(err)
	}
	err = Run("resumed", []string{"--resume", "failed", issueFile(t, changed)}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "b.txt") {
		t.Fatalf("want the resumed scope review to block b.txt, which only the changed issue allows, got %v", err)
	}
	if brief := agentCall(t, call+".scope").Stdin; !strings.Contains(brief, original) ||
		strings.Contains(brief, "b.txt and") {
		t.Fatalf("want the resumed scope review to get the original issue, got:\n%s", brief)
	}
}
