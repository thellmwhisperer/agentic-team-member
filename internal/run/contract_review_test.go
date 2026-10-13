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
const repairContract = `{` + readyDispatch + `"status":"repair","acceptance":[{"criterion":"Retry once.",` +
	`"evidence":"a.txt does not retry once","satisfied":false}],"children":[{"cause":"missing retry",` +
	`"contract":"Repair retry implementation without editing tests","dependency":"blocks_parent",` +
	`"target":"criterion:Retry once.","evidence":"a.txt: retry criterion"}]}`
const independentSplitContract = `{` + readyDispatch + `"status":"split","acceptance":[],"children":[{"cause":"independent outcome",` +
	`"contract":"Track separate outcome as a child issue","dependency":"independent",` +
	`"evidence":"new independent behavior in a.txt"}]}`
const splitMandatoryGapContract = `{` + readyDispatch + `"status":"split","acceptance":[{"criterion":"Retry once.",` +
	`"evidence":"a.txt does not retry once","satisfied":false}],"children":[{"cause":"mandatory retry gap",` +
	`"contract":"Repair retry behavior to satisfy the original acceptance","dependency":"blocks_parent",` +
	`"target":"criterion:Retry once.","evidence":"a.txt fails Retry once."},{"cause":"independent outcome",` +
	`"contract":"Track separate outcome as a child issue","dependency":"independent",` +
	`"evidence":"new independent behavior in a.txt"}]}`
const splitMissingGateContract = `{` + readyDispatch + `"status":"split","acceptance":[],"children":[{"cause":"missing non-goals",` +
	`"contract":"Return the issue to planning to add Non-goals","dependency":"blocks_parent",` +
	`"target":"gate:non_goals","evidence":"the original issue has no Non-goals field"},{"cause":"independent outcome",` +
	`"contract":"Track separate outcome as a child issue","dependency":"independent",` +
	`"evidence":"new independent behavior in a.txt"}]}`
const splitTwoMandatoryGapsOneChild = `{` + readyDispatch + `"status":"split","acceptance":[` +
	`{"criterion":"Retry once.","evidence":"retry missing","satisfied":false},` +
	`{"criterion":"Retry error is reported.","evidence":"error missing","satisfied":false}],` +
	`"children":[{"cause":"retry gap","contract":"Implement one retry","dependency":"blocks_parent",` +
	`"target":"criterion:Retry once.","evidence":"a.txt lacks the retry"}]}`
const contractDelivery = `echo delivered > "$ATM_TEST_OUT/delivered"`
const reviewReadyIssue = `# Retry on timeout
Type: fix

## Non-goals
Change retry policy outside this case.

## Invariants
Retry occurs once.

## Acceptance criteria
Retry once.

## Split triggers
Split if the diff adds an independent outcome.
`

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
		{name: "scope drift", report: independentSplitContract, want: "split"},
		{name: "review mutation", work: "echo changed > a.txt", want: "changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, out := contractReviewRepo(t)
			t.Setenv("FAKE_CONTRACT_REPORT", tc.report)
			t.Setenv("FAKE_CONTRACT_WORK", tc.work)
			err := Run("t", []string{issueFile(t, reviewReadyIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
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
	metadata, err := deliveryMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	valid := contractReview{Head: head, Status: "passed", metadata: metadata,
		Dispatch:   dispatchGates{true, true, true, true, true},
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
			if tc.name == "repair rechecks" {
				original, err := os.ReadFile(filepath.Join(root, ".atm.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				atm := atmSet(string(original), "install", `if grep -q repaired a.txt; then printf install >> "$ATM_TEST_OUT/install.marker"; fi`)
				atm = atmSet(atm, "test", `if grep -q repaired a.txt; then printf test >> "$ATM_TEST_OUT/test.marker"; fi; sh a_test.sh`)
				atm = atmSet(atm, "typecheck", `if grep -q repaired a.txt; then printf typecheck >> "$ATM_TEST_OUT/typecheck.marker"; fi`)
				atm = atmSet(atm, "lint", `if grep -q repaired a.txt; then printf lint >> "$ATM_TEST_OUT/lint.marker"; fi`)
				atm = atmSet(atm, "test_file", `sh {file}; status=$?; if grep -q repaired a.txt; then printf proof >> "$ATM_TEST_OUT/proof.marker"; fi; exit "$status"`)
				if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(atm), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("FAKE_CONTRACT_REPORT", repairContract)
			t.Setenv("FAKE_CONTRACT_REPAIR", tc.work)
			t.Setenv("FAKE_CONTRACT_AFTER_REPAIR_REPORT", tc.after)
			err := Run("t", []string{issueFile(t, reviewReadyIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
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
				checkContractRechecks(t, dir, out, rounds)
			}
			_, e = os.Stat(filepath.Join(out, "delivered"))
			if (e == nil) != (tc.want == "") {
				t.Fatalf("delivery: %v", e)
			}
		})
	}
}

func checkContractRechecks(t *testing.T, dir, out string, rounds []contractReview) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(dir, "contract-rechecks-0.json"))
	var rechecks struct {
		Reproofs []unitResult `json:"reproofs"`
		Commands []cmdResult  `json:"commands"`
	}
	if e != nil || json.Unmarshal(b, &rechecks) != nil || len(rechecks.Reproofs) != 1 {
		t.Fatalf("rechecks missing a unit proof: %s %v", b, e)
	}
	proof := rechecks.Reproofs[0]
	if proof.Type != "fix" || proof.TestFile != "a_test.sh" || proof.Result != "passed" ||
		proof.Red.Exit == 0 || proof.Green.Exit != 0 {
		t.Fatalf("repair did not rerun the required red/green proof: %+v", proof)
	}
	wantCommands := []string{"install", "test", "typecheck", "lint"}
	if len(rechecks.Commands) != len(wantCommands) {
		t.Fatalf("repair checks missing configured commands: %+v", rechecks.Commands)
	}
	for i, want := range wantCommands {
		if rechecks.Commands[i].Name != want || rechecks.Commands[i].Result != "passed" {
			t.Fatalf("repair command %q not passed: %+v", want, rechecks.Commands[i])
		}
		marker := filepath.Join(out, want+".marker")
		if b, err := os.ReadFile(marker); err != nil || len(b) == 0 {
			t.Fatalf("configured command %q left no repair marker: %q %v", want, b, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(out, "proof.marker")); err != nil || len(b) == 0 {
		t.Fatalf("repair proof left no marker: %q %v", b, err)
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
			report := strings.Replace(splitMissingGateContract, `"`+field+`":true`, `"`+field+`":false`, 1)
			report = strings.Replace(report, `"target":"gate:non_goals"`, `"target":"gate:`+field+`"`, 1)
			t.Setenv("FAKE_CONTRACT_REPORT", report)
			err := Run("t", []string{issueFile(t, reviewReadyIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
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
		err := Run("t", []string{issueFile(t, reviewReadyIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "task-slicing") {
			t.Fatalf("want skill proof failure got %v", err)
		}
	})
}

func TestRunContractReviewRequiresOriginalIssueGates(t *testing.T) {
	for _, tc := range []struct{ section, content string }{
		{"Non-goals", "Change retry policy outside this case."},
		{"Invariants", "Retry occurs once."},
		{"Acceptance criteria", "Retry once."},
		{"Split triggers", "Split if the diff adds an independent outcome."},
	} {
		t.Run(tc.section, func(t *testing.T) {
			root, out := contractReviewRepo(t)
			body := strings.Replace(reviewReadyIssue, "## "+tc.section+"\n", "", 1)
			body = strings.Replace(body, tc.content+"\n", "", 1)
			t.Setenv("FAKE_CONTRACT_REPORT", passedContract)
			err := Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "issue missing task-slicing") {
				t.Fatalf("missing %s accepted: %v", tc.section, err)
			}
			if _, e := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(e) {
				t.Fatal("delivery ran")
			}
			if _, e := os.Stat(filepath.Join(root, ".atm/runs/t/contract-review.json")); !os.IsNotExist(e) {
				t.Fatal("passed report persisted")
			}
		})
	}
	t.Run("numbered bold fields", func(t *testing.T) {
		_, out := contractReviewRepo(t)
		body := `# Retry on timeout
Type: fix
3. **Non-goals:** Change retry policy outside this case.
4. **Invariants:** Retry occurs once.
7. **Acceptance:** Retry once.
8. **Split triggers:** Split if the diff adds an independent outcome.
`
		t.Setenv("FAKE_CONTRACT_REPORT", passedContract)
		if err := Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(out, "delivered")); err != nil {
			t.Fatal("delivery did not run:", err)
		}
	})
	t.Run("unmarked labels", func(t *testing.T) {
		_, out := contractReviewRepo(t)
		body := `# Retry on timeout
Type: fix
Non-goals
Change retry policy outside this case.
Invariants
Retry occurs once.
Acceptance
Retry once.
Split triggers
Split if the diff adds an independent outcome.
`
		t.Setenv("FAKE_CONTRACT_REPORT", passedContract)
		err := Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "issue missing task-slicing") {
			t.Fatalf("unmarked labels accepted as gates: %v", err)
		}
		if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
			t.Fatal("delivery ran")
		}
	})
	t.Run("numbered and bold section content", func(t *testing.T) {
		gates := []struct{ section, original string }{
			{"Non-goals", "Change retry policy outside this case."},
			{"Invariants", "Retry occurs once."},
			{"Acceptance criteria", "Retry once."},
			{"Split triggers", "Split if the diff adds an independent outcome."},
		}
		for _, format := range []struct{ name, content string }{
			{"numbered scenario", "1. Given a timeout: retry exactly once."},
			{"bold label", "**Expected:** retry exactly once."},
		} {
			for _, gate := range gates {
				t.Run(format.name+"/"+gate.section, func(t *testing.T) {
					_, out := contractReviewRepo(t)
					body := strings.Replace(reviewReadyIssue, "Type: fix\n", "Type: fix\nRetry once.\n", 1)
					body = strings.Replace(body, gate.original+"\n", format.content+"\n", 1)
					t.Setenv("FAKE_CONTRACT_REPORT", passedContract)
					if err := Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(filepath.Join(out, "delivered")); err != nil {
						t.Fatal("delivery did not run:", err)
					}
				})
			}
		}
	})
}

func TestRunContractReviewIgnoresHTMLCommentsInGates(t *testing.T) {
	gates := []struct{ section, original, field, target string }{
		{"Non-goals", "Change retry policy outside this case.", "Non-goals", "non_goals"},
		{"Invariants", "Retry occurs once.", "Invariants", "invariants"},
		{"Acceptance criteria", "Retry once.", "Acceptance", "executable_acceptance"},
		{"Split triggers", "Split if the diff adds an independent outcome.", "Split triggers", "split_triggers"},
	}
	for _, form := range []struct{ name, content string }{
		{"inline field", ""},
		{"section comment", "<!-- TODO -->"},
		{"multiline comment", "<!-- TODO\nstill TODO\n-->"},
		{"fenced comment", "```html\n<!-- TODO -->\n```"},
	} {
		for _, gate := range gates {
			t.Run(form.name+"/"+gate.section, func(t *testing.T) {
				_, out := contractReviewRepo(t)
				old := "## " + gate.section + "\n" + gate.original + "\n"
				newField := form.content
				if form.name == "inline field" {
					newField = gate.field + ": <!-- TODO -->"
				} else {
					newField = "## " + gate.section + "\n" + form.content
				}
				body := strings.Replace(reviewReadyIssue, old, newField+"\n", 1)
				if !strings.Contains(body, newField) {
					t.Fatal("comment-only gate fixture was not built")
				}
				t.Setenv("FAKE_CONTRACT_REPORT", passedContract)
				err := Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{})
				if err == nil || !strings.Contains(err.Error(), "issue missing task-slicing") {
					t.Fatalf("comment-only %s gate accepted: %v", gate.section, err)
				}
				if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
					t.Fatal("delivery ran")
				}
			})
		}
	}
	t.Run("split retains blockers for every commented gate", func(t *testing.T) {
		root, out := contractReviewRepo(t)
		body := reviewReadyIssue
		for _, gate := range gates {
			body = strings.Replace(body, gate.original, "<!-- TODO -->", 1)
		}
		var children []contractChild
		for _, gate := range gates {
			children = append(children, contractChild{Cause: "empty " + gate.section, Contract: "Return the issue to planning",
				Dependency: "blocks_parent", Target: "gate:" + gate.target, Evidence: "the original field contains only a comment"})
		}
		report, err := json.Marshal(contractReview{Dispatch: dispatchGates{true, true, true, true, true},
			Status: "split", Acceptance: []contractCriterion{}, Children: children})
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("FAKE_CONTRACT_REPORT", string(report))
		err = Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "requires split") {
			t.Fatalf("want retained split, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
			t.Fatal("delivery ran")
		}
		var rounds []contractReview
		b, err := os.ReadFile(filepath.Join(root, ".atm/runs/t/contract-review.json"))
		if err != nil || json.Unmarshal(b, &rounds) != nil || len(rounds) != 1 || len(rounds[0].Children) != len(gates) {
			t.Fatalf("comment-gate blockers were not persisted: %s %v", b, err)
		}
	})
}

func TestRunContractReviewPersistsSplitWithMandatoryGap(t *testing.T) {
	root, out := contractReviewRepo(t)
	t.Setenv("FAKE_CONTRACT_REPORT", splitMandatoryGapContract)
	err := Run("t", []string{issueFile(t, reviewReadyIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires split") {
		t.Fatalf("want split disposition, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
		t.Fatal("delivery ran")
	}
	b, err := os.ReadFile(filepath.Join(root, ".atm/runs/t/contract-review.json"))
	if err != nil {
		t.Fatal("split evidence missing:", err)
	}
	var rounds []contractReview
	if err := json.Unmarshal(b, &rounds); err != nil || len(rounds) != 1 || len(rounds[0].Children) != 2 ||
		rounds[0].Status != "split" || rounds[0].Children[0].Dependency != "blocks_parent" {
		t.Fatalf("split contract lost mandatory blocker: %s, %v", b, err)
	}
}

func TestRunContractReviewRequiresOneBlockingChildPerGap(t *testing.T) {
	for _, tc := range []struct{ name, body, report, want string }{
		{"two mandatory gaps", reviewReadyIssue + "\n2. Retry error is reported.\n", splitTwoMandatoryGapsOneChild,
			"every unmet gate or criterion"},
		{"unrelated repair child", reviewReadyIssue,
			strings.Replace(repairContract, `"target":"criterion:Retry once."`, `"target":"criterion:other"`, 1),
			"does not target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, out := contractReviewRepo(t)
			t.Setenv("FAKE_CONTRACT_REPORT", tc.report)
			err := Run("t", []string{issueFile(t, tc.body)}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
				t.Fatal("delivery ran")
			}
		})
	}
}

func TestRunContractReviewMissingGateNeedsMatchingBlockingChild(t *testing.T) {
	for _, tc := range []struct{ section, content, target string }{
		{"Non-goals", "Change retry policy outside this case.", "non_goals"},
		{"Invariants", "Retry occurs once.", "invariants"},
		{"Acceptance criteria", "Retry once.", "executable_acceptance"},
		{"Split triggers", "Split if the diff adds an independent outcome.", "split_triggers"},
	} {
		t.Run(tc.section, func(t *testing.T) {
			body := strings.Replace(reviewReadyIssue, "## "+tc.section+"\n"+tc.content+"\n", "", 1)
			report := strings.Replace(splitMissingGateContract, "gate:non_goals", "gate:"+tc.target, 1)
			report = strings.Replace(report, "the original issue has no Non-goals field",
				"the original issue has no "+tc.section+" field", 1)
			root, out := contractReviewRepo(t)
			t.Setenv("FAKE_CONTRACT_REPORT", report)
			err := Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "requires split") {
				t.Fatalf("want concrete split, got %v", err)
			}
			if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
				t.Fatal("delivery ran")
			}
			b, err := os.ReadFile(filepath.Join(root, ".atm/runs/t/contract-review.json"))
			var rounds []contractReview
			if err != nil || json.Unmarshal(b, &rounds) != nil || len(rounds) != 1 || len(rounds[0].Children) != 2 {
				t.Fatalf("split children were not retained: %s %v", b, err)
			}
		})
	}
	t.Run("independent child cannot cover missing gate", func(t *testing.T) {
		body := strings.Replace(reviewReadyIssue, "## Non-goals\nChange retry policy outside this case.\n\n", "", 1)
		_, out := contractReviewRepo(t)
		t.Setenv("FAKE_CONTRACT_REPORT", independentSplitContract)
		err := Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "every unmet gate or criterion") {
			t.Fatalf("want missing gate blocker error, got %v", err)
		}
		if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
			t.Fatal("delivery ran")
		}
	})
}

func TestRunContractReviewIgnoresFencedLabelsAndMarkupOnlyGates(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"labels inside example", "# Retry on timeout\nType: fix\n```md\n## Non-goals\ntext\n## Invariants\ntext\n## Acceptance\ntext\n## Split triggers\ntext\n```\n"},
		{"separator-only sections", "# Retry on timeout\nType: fix\n## Non-goals\n---\n## Invariants\n***\n## Acceptance\n___\n## Split triggers\n---\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, out := contractReviewRepo(t)
			t.Setenv("FAKE_CONTRACT_REPORT", passedContract)
			err := Run("t", []string{issueFile(t, tc.body)}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "issue missing task-slicing") {
				t.Fatalf("empty gates accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
				t.Fatal("delivery ran")
			}
		})
	}
}

func TestRunContractReviewAcceptsExecutableAcceptanceFence(t *testing.T) {
	body := "# Retry on timeout\nType: fix\nRetry once.\n## Non-goals\nChange retry policy outside this case.\n" +
		"## Invariants\nRetry occurs once.\n## Acceptance\n```sh\ngo test ./internal/run\n```\n" +
		"## Split triggers\nSplit if the diff adds an independent outcome.\n"
	_, out := contractReviewRepo(t)
	t.Setenv("FAKE_CONTRACT_REPORT", passedContract)
	if err := Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "delivered")); err != nil {
		t.Fatal("delivery did not run:", err)
	}
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
			text := strings.Replace(reviewReadyIssue, "Type: fix", "Type: "+typ, 1)
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

func TestRunContractReviewRejectsProtectedTestRename(t *testing.T) {
	root, out := contractReviewRepo(t)
	if err := os.WriteFile(filepath.Join(root, "other_test.sh"), []byte("true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "add", "other_test.sh")
	gitT(t, root, "commit", "-m", "fixture additional protected test")
	t.Setenv("FAKE_CONTRACT_REPORT", repairContract)
	t.Setenv("FAKE_CONTRACT_REPAIR", "printf 'fixed\\nrepaired\\n' > a.txt; mv other_test.sh other.sh")
	t.Setenv("FAKE_CONTRACT_AFTER_REPAIR_REPORT", passedContract)
	err := Run("t", []string{issueFile(t, reviewReadyIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "protected test other_test.sh") {
		t.Fatalf("protected rename accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
		t.Fatal("delivery executed despite protected rename")
	}
}

func TestRunContractReviewRejectsDeliveryMetadataMutation(t *testing.T) {
	const origin = "git remote set-url origin https://example.com/unexpected/repo.git"
	for _, tc := range []struct {
		name, command string
		repair        bool
	}{
		{"review origin", origin, false},
		{"review push URL", "git remote set-url --push origin https://example.com/unexpected/repo.git", false},
		{"review branch", "git checkout -q -b unexpected-delivery-branch", false},
		{"repair origin", origin, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, out := contractReviewRepo(t)
			if tc.repair {
				t.Setenv("FAKE_CONTRACT_REPORT", repairContract)
				t.Setenv("FAKE_CONTRACT_REPAIR", "printf 'fixed\\nrepaired\\n' > a.txt; "+tc.command)
				t.Setenv("FAKE_CONTRACT_AFTER_REPAIR_REPORT", passedContract)
			} else {
				t.Setenv("FAKE_CONTRACT_WORK", tc.command)
			}
			err := Run("t", []string{issueFile(t, reviewReadyIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "delivery metadata") {
				t.Fatalf("metadata mutation accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
				t.Fatal("delivery executed despite metadata mutation")
			}
		})
	}
}
