package run

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

const readyDispatch = `"dispatch":{"non_goals":true,"invariants":true,` +
	`"executable_acceptance":true,"split_triggers":true,"single_contract":true},`
const passedContract = `{` + readyDispatch + `"status":"passed","acceptance":[{"criterion":"Retry once.",` +
	`"evidence":"a_test.sh passes","satisfied":true}],"children":[]}`
const repairContract = `{` + readyDispatch + `"status":"repair","acceptance":[],"children":[{"cause":"missing retry",` +
	`"contract":"Repair retry implementation without editing tests","dependency":"blocks_parent",` +
	`"evidence":"a.txt: retry criterion"}]}`
const contractDelivery = `echo delivered > "$ATM_TEST_OUT/delivered"`

func TestRunContractReview(t *testing.T) {
	for _, tc := range []struct{ name, report, work, want string }{
		{name: "current evidence"},
		{name: "missing", report: `{}`, want: "invalid contract review"},
		{name: "passed without dispatch gates", report: strings.Replace(passedContract, readyDispatch, "", 1),
			want: "task-slicing dispatch gates"},
		{name: "multiple contracts cannot pass", want: "task-slicing dispatch gates",
			report: strings.Replace(passedContract, `"single_contract":true`, `"single_contract":false`, 1)},
		{name: "mandatory unmet", want: "unmet",
			report: strings.Replace(passedContract, `"satisfied":true`, `"satisfied":false`, 1)},
		{name: "scope drift", report: strings.Replace(repairContract, `"repair"`, `"split"`, 1), want: "split"},
		{name: "review mutation", work: "echo changed > a.txt", want: "changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, out := contractReviewRepo(t)
			t.Setenv("FAKE_CONTRACT_REPORT", tc.report)
			t.Setenv("FAKE_CONTRACT_WORK", tc.work)
			err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %s, got %v", tc.want, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			_, e := os.Stat(filepath.Join(out, "delivered"))
			if (e == nil) != (tc.want == "") {
				t.Fatalf("delivery: %v", e)
			}
			if tc.want == "" || tc.want == "split" {
				dir := filepath.Join(root, ".atm/runs/t")
				b, e := os.ReadFile(filepath.Join(dir, "contract-review.json"))
				if e != nil || !strings.Contains(string(b), `"head"`) {
					t.Fatalf("evidence %s: %v", b, e)
				}
				brief, e := os.ReadFile(filepath.Join(dir, "brief-contract-review-0.md"))
				if e != nil || !strings.Contains(string(brief), "+fixed") || !strings.Contains(string(brief), "Retry once.") {
					t.Fatalf("review lacks actual diff/issue: %s %v", brief, e)
				}
			}
		})
	}
}

func TestContractReviewEvidence(t *testing.T) {
	root := repo(t, t.TempDir(), "")
	head := gitT(t, root, "rev-parse", "HEAD")
	valid := contractReview{Head: head, Status: "passed", Dispatch: dispatchGates{true, true, true, true, true},
		Acceptance: []contractCriterion{{Criterion: "Retry once.", Evidence: "test passes", Satisfied: true}}}
	stale := valid
	stale.Head = "old"
	for _, tc := range []struct {
		name     string
		evidence contractReview
		want     bool
	}{
		{"missing", contractReview{}, false},
		{"stale", stale, false},
		{"current", valid, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.evidence.current(root); (err == nil) != tc.want {
				t.Fatalf("got %v", err)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := valid.current(root); err == nil {
		t.Fatal("dirty work accepted")
	}
}

func TestRunContractReviewRepair(t *testing.T) {
	for _, tc := range []struct {
		name, work, after, want string
		rounds                  int
	}{
		{"repair rechecks", "printf 'fixed\nrepaired\n' > a.txt", passedContract, "", 2},
		{"failed rechecks", "echo broken > a.txt", passedContract, "contract repair checks", 1},
		{"protected test", "echo true > a_test.sh", passedContract, "protected test", 1},
		{"two corrections", "echo fixed > a.txt", "", "requires split", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, out := contractReviewRepo(t)
			t.Setenv("FAKE_CONTRACT_REPORT", repairContract)
			t.Setenv("FAKE_CONTRACT_REPAIR", tc.work)
			t.Setenv("FAKE_CONTRACT_AFTER_REPAIR_REPORT", tc.after)
			err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s got %v", tc.want, err)
			}
			dir := filepath.Join(root, ".atm/runs/t")
			b, e := os.ReadFile(filepath.Join(dir, "contract-review.json"))
			if e != nil {
				t.Fatal(e)
			}
			var rounds []contractReview
			if e := json.Unmarshal(b, &rounds); e != nil {
				t.Fatal(e)
			}
			if len(rounds) != tc.rounds {
				t.Fatalf("want %d rounds got %s", tc.rounds, b)
			}
			if tc.name == "two corrections" && rounds[2].Status != "split" {
				t.Fatalf("no split: %s", b)
			}
			if tc.name == "repair rechecks" {
				checkContractRechecks(t, dir, rounds)
			}
			_, e = os.Stat(filepath.Join(out, "delivered"))
			if (e == nil) != (tc.want == "") {
				t.Fatalf("delivery: %v", e)
			}
		})
	}
}

func checkContractRechecks(t *testing.T, dir string, rounds []contractReview) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(dir, "contract-rechecks-0.json"))
	if e != nil || !strings.Contains(string(b), `"reproofs"`) || !strings.Contains(string(b), `"passed"`) {
		t.Fatalf("rechecks missing: %s %v", b, e)
	}
	rep := readReport(t, filepath.Join(dir, "report.json"))
	if rep["head_sha"] != rounds[1].Head || rounds[0].Head == rounds[1].Head {
		t.Fatal("review not bound to repaired head")
	}
}

func TestRunContractReviewTaskSlicing(t *testing.T) {
	fields := []string{"non_goals", "invariants", "executable_acceptance", "split_triggers", "single_contract"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			root, out := contractReviewRepo(t)
			report := strings.Replace(repairContract, `"status":"repair"`, `"status":"split"`, 1)
			report = strings.Replace(report, `"`+field+`":true`, `"`+field+`":false`, 1)
			t.Setenv("FAKE_CONTRACT_REPORT", report)
			err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "requires split") {
				t.Fatalf("want split got %v", err)
			}
			if _, e := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(e) {
				t.Fatal("delivery ran")
			}
			log, e := os.ReadFile(filepath.Join(root, ".atm/runs/t/contract-review-0.jsonl"))
			if e != nil || !strings.Contains(string(log), "task-slicing") {
				t.Fatalf("skill not loaded: %s %v", log, e)
			}
		})
	}
	t.Run("skill required", func(t *testing.T) {
		repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery", contractDelivery))
		t.Setenv("FAKE_CONTRACT_MODE", "no-skill")
		err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "task-slicing") {
			t.Fatalf("want skill proof failure got %v", err)
		}
	})
}

func TestRunContractReviewBlocksPendingMandatoryCriterion(t *testing.T) {
	root, out := contractReviewRepo(t)
	t.Setenv("ATM_TEST_CRITERION", "A retry runs once after a timeout.")
	t.Setenv("FAKE_AGENT_WORK", chainWork)
	err := Run("t", []string{issueFile(t, followUpIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "mandatory criterion unmet") {
		t.Fatalf("got %v", err)
	}
	if _, e := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(e) {
		t.Fatal("delivery ran")
	}
	left := readFollowUps(t, root)
	if len(left) != 1 || left[0]["red_test"] != "c3_test.sh" {
		t.Fatalf("lost mandatory follow-up: %v", left)
	}
}

func contractReviewRepo(t *testing.T) (root, out string) {
	t.Helper()
	root = repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery", contractDelivery))
	out = t.TempDir()
	t.Setenv("ATM_TEST_OUT", out)
	return root, out
}

func TestRunContractReviewRepairWithoutTestFile(t *testing.T) {
	for typ, file := range map[string]string{"docs": "README.md", "chore": "deps.txt", "refactor": "b.txt"} {
		t.Run(typ, func(t *testing.T) {
			root, out := contractReviewRepo(t)
			t.Setenv("FAKE_AGENT_WORK", "echo original > "+file+
				`; mkdir -p .atm; echo "{\"test_file\":\"\"}" > .atm/fake-report.json`)
			t.Setenv("FAKE_CONTRACT_REPORT", repairContract)
			t.Setenv("FAKE_CONTRACT_REPAIR", "echo repaired > "+file)
			t.Setenv("FAKE_CONTRACT_AFTER_REPAIR_FILE", file)
			t.Setenv("FAKE_CONTRACT_AFTER_REPAIR_REPORT", passedContract)
			text := strings.Replace(issue, "Type: fix", "Type: "+typ, 1)
			if err := Run("t", []string{issueFile(t, text)}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(out, "delivered")); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(root, ".atm/runs/t/contract-rechecks-0.json"))
			if err != nil || !strings.Contains(string(b), `"name": "test"`) || !strings.Contains(string(b), `"passed"`) {
				t.Fatalf("repair checks missing: %s %v", b, err)
			}
		})
	}
}

func TestContractReviewReproofRequiresTestFile(t *testing.T) {
	for _, typ := range []string{"fix", "feature", "greenfield", "tests"} {
		t.Run(typ, func(t *testing.T) {
			root := repo(t, t.TempDir(), "")
			head := gitT(t, root, "rev-parse", "HEAD")
			why, _, _, err := reprove(t.TempDir(), root, head, []proof{{base: head, typ: typ}}, config.Config{})
			if err != nil || !strings.Contains(why, "test") {
				t.Fatalf("missing test must block: %q %v", why, err)
			}
		})
	}
}
