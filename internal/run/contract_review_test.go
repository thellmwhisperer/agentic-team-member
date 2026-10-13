package run

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
const splitMandatoryGapContract = `{` + readyDispatch + `"status":"split","acceptance":[{"criterion":"Retry once.",` +
	`"evidence":"a.txt does not retry once","satisfied":false}],"children":[{"cause":"mandatory retry gap",` +
	`"contract":"Repair retry behavior to satisfy the original acceptance","dependency":"blocks_parent",` +
	`"evidence":"a.txt fails Retry once."},{"cause":"independent outcome",` +
	`"contract":"Track separate outcome as a child issue","dependency":"independent",` +
	`"evidence":"new independent behavior in a.txt"}]}`
const splitMissingGateContract = `{` + readyDispatch +
	`"status":"split","acceptance":[],"children":[{"cause":"missing non-goals",` +
	`"contract":"Return the issue to planning to add Non-goals","dependency":"blocks_parent",` +
	`"evidence":"the original issue has no Non-goals field"},{"cause":"independent outcome",` +
	`"contract":"Track separate outcome as a child issue","dependency":"independent",` +
	`"evidence":"new independent behavior in a.txt"}]}`
const contractDelivery = `echo delivered > "$ATM_TEST_OUT/delivered"`

func assertDeliveryBlocked(t *testing.T, out string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
		t.Fatalf("delivery ran: %v", err)
	}
}

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
				configureRecheckTrace(t, root, "fix", "a.txt")
			}
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
				checkContractRechecks(t, dir, out, rounds, "fix", "a_test.sh")
			}
			_, e = os.Stat(filepath.Join(out, "delivered"))
			if (e == nil) != (tc.want == "") {
				t.Fatalf("delivery: %v", e)
			}
		})
	}
}

func checkContractRechecks(t *testing.T, dir, out string, rounds []contractReview, typ, testFile string) {
	t.Helper()
	checkRepairEpoch(t, out)
	rechecks := readRecheckResults(t, dir)
	checkApplicableProof(t, rechecks.Reproofs[0], typ, testFile)
	checkConfiguredCommands(t, rechecks.Commands)
	checkRepairTrace(t, out, typ)
	checkRepairedHead(t, dir, rounds)
}

func checkRepairEpoch(t *testing.T, out string) {
	t.Helper()
	epoch, err := os.ReadFile(filepath.Join(out, "repair-epoch"))
	if err != nil || strings.TrimSpace(string(epoch)) != "contract-repair" {
		t.Fatalf("repair owner did not record its epoch: %q %v", epoch, err)
	}
}

func readRecheckResults(t *testing.T, dir string) struct {
	Reproofs []unitResult `json:"reproofs"`
	Commands []cmdResult  `json:"commands"`
} {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(dir, "contract-rechecks-0.json"))
	var rechecks struct {
		Reproofs []unitResult `json:"reproofs"`
		Commands []cmdResult  `json:"commands"`
	}
	if e != nil || json.Unmarshal(b, &rechecks) != nil || len(rechecks.Reproofs) != 1 {
		t.Fatalf("rechecks missing the applicable unit proof: %s %v", b, e)
	}
	return rechecks
}

func checkApplicableProof(t *testing.T, proof unitResult, typ, testFile string) {
	t.Helper()
	if proof.Type != typ || proof.TestFile != testFile || proof.Result != "passed" {
		t.Fatalf("repair did not rerun the %s proof: %+v", typ, proof)
	}
	switch typ {
	case "fix":
		if proof.Red.Exit == 0 || proof.Green.Exit != 0 {
			t.Fatalf("repair did not rerun the required red/green proof: %+v", proof)
		}
	case "refactor":
		if proof.Red.Exit != 0 {
			t.Fatalf("repair did not rerun the passing baseline suite: %+v", proof)
		}
	}
}

func checkConfiguredCommands(t *testing.T, commands []cmdResult) {
	t.Helper()
	wantCommands := []string{"install", "test", "typecheck", "lint"}
	if len(commands) != len(wantCommands) {
		t.Fatalf("repair checks missing configured commands: %+v", commands)
	}
	for i, want := range wantCommands {
		if commands[i].Name != want || commands[i].Result != "passed" {
			t.Fatalf("repair command %q not passed: %+v", want, commands[i])
		}
	}
}

func checkRepairTrace(t *testing.T, out, typ string) {
	t.Helper()
	trace, err := os.ReadFile(filepath.Join(out, "recheck.trace"))
	if err != nil {
		t.Fatalf("repair command trace missing: %v", err)
	}
	// A fix proof runs red, green, then the base with the fix's added lines, which fails and ends it.
	wantTrace := map[string][]string{
		"fix":      {"install", "proof", "proof", "proof", "test", "typecheck", "lint"},
		"refactor": {"install", "baseline", "test", "typecheck", "lint"},
		"docs":     {"install", "test", "typecheck", "lint"},
		"chore":    {"install", "test", "typecheck", "lint"},
	}[typ]
	var repairTrace []string
	for _, line := range strings.Split(strings.TrimSpace(string(trace)), "\n") {
		repairTrace = append(repairTrace, strings.TrimPrefix(line, "repair:"))
	}
	if got, want := repairTrace, wantTrace; !reflect.DeepEqual(got, want) {
		t.Fatalf("repair epoch did not execute every applicable check and proof: got %q want %q", got, want)
	}
}

func checkRepairedHead(t *testing.T, dir string, rounds []contractReview) {
	t.Helper()
	rep := readReport(t, filepath.Join(dir, "report.json"))
	if rep["head_sha"] != rounds[1].Head || rounds[0].Head == rounds[1].Head {
		t.Fatal("review not bound to repaired head")
	}
}

func configureRecheckTrace(t *testing.T, root, typ, repairedFile string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".atm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	record := recheckRecord
	repaired := `grep -q repaired ` + repairedFile
	atm := atmSet(string(b), "install", record("install", repaired))
	test := record("test", repaired)
	if typ == "refactor" {
		// The fixture adds this file only in WORK; BASE execution must be observed independently.
		test += record("baseline", "[ ! -e "+repairedFile+" ]")
	}
	atm = atmSet(atm, "test", test+"sh a_test.sh")
	atm = atmSet(atm, "typecheck", record("typecheck", repaired))
	atm = atmSet(atm, "lint", record("lint", repaired))
	if typ == "fix" {
		atm = atmSet(atm, "test_file", proofRecheckCommand)
	}
	if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(atm), 0o600); err != nil {
		t.Fatal(err)
	}
}

func recheckRecord(name, condition string) string {
	return `if ` + condition + ` && grep -q contract-repair "$ATM_TEST_OUT/repair-epoch" 2>/dev/null; then ` +
		`printf "repair:` + name + `\n" >> "$ATM_TEST_OUT/recheck.trace"; fi; `
}

const proofRecheckCommand = `sh {file}; status=$?; ` +
	`if grep -q contract-repair "$ATM_TEST_OUT/repair-epoch" 2>/dev/null; then ` +
	`printf "repair:proof\n" >> "$ATM_TEST_OUT/recheck.trace"; fi; exit "$status"`

func TestRunContractReviewTaskSlicing(t *testing.T) {
	fields := []string{"non_goals", "invariants", "executable_acceptance", "split_triggers", "single_contract"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			root, out := contractReviewRepo(t)
			report := strings.Replace(splitMissingGateContract, `"`+field+`":true`, `"`+field+`":false`, 1)
			t.Setenv("FAKE_CONTRACT_REPORT", report)
			err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "requires split") {
				t.Fatalf("want split got %v", err)
			}
			assertDeliveryBlocked(t, out)
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

func TestRunContractReviewRequiresOriginalIssueGates(t *testing.T) {
	for _, tc := range []struct{ section, content, field string }{
		{"Non-goals", "Change retry policy outside this case.", "non_goals"},
		{"Invariants", "Retry occurs once.", "invariants"},
		{"Acceptance criteria", "Retry once.", "executable_acceptance"},
		{"Split triggers", "Split if the diff adds an independent outcome.", "split_triggers"},
	} {
		t.Run(tc.section, func(t *testing.T) {
			root, out := contractReviewRepo(t)
			body := strings.Replace(issue, "## "+tc.section+"\n", "", 1)
			body = strings.Replace(body, tc.content+"\n", "", 1)
			report := strings.Replace(splitMissingGateContract, `"`+tc.field+`":true`, `"`+tc.field+`":false`, 1)
			report = strings.ReplaceAll(report, "non-goals", tc.section)
			report = strings.ReplaceAll(report, "Non-goals", tc.section)
			t.Setenv("FAKE_CONTRACT_REPORT", report)
			err := Run("t", []string{issueFile(t, body)}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "requires split") {
				t.Fatalf("want missing gate split, got %v", err)
			}
			assertDeliveryBlocked(t, out)
			brief, err := os.ReadFile(filepath.Join(root, ".atm/runs/t/brief-contract-review-0.md"))
			if err != nil || !strings.Contains(string(brief), body) {
				t.Fatalf("review did not receive original incomplete issue: %s %v", brief, err)
			}
		})
	}
}

func TestRunContractReviewPersistsSplitWithMandatoryGap(t *testing.T) {
	root, out := contractReviewRepo(t)
	t.Setenv("FAKE_CONTRACT_REPORT", splitMandatoryGapContract)
	err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "requires split") {
		t.Fatalf("want split disposition, got %v", err)
	}
	assertDeliveryBlocked(t, out)
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

func TestRunContractReviewBlocksPendingMandatoryCriterion(t *testing.T) {
	root, out := contractReviewRepo(t)
	t.Setenv("ATM_TEST_CRITERION", "A retry runs once after a timeout.")
	t.Setenv("FAKE_AGENT_WORK", chainWork)
	err := Run("t", []string{issueFile(t, followUpIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "mandatory criterion unmet") {
		t.Fatalf("got %v", err)
	}
	assertDeliveryBlocked(t, out)
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
			configureRecheckTrace(t, root, typ, file)
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
			var rounds []contractReview
			b, err := os.ReadFile(filepath.Join(root, ".atm/runs/t/contract-review.json"))
			if err != nil || json.Unmarshal(b, &rounds) != nil {
				t.Fatalf("contract review rounds missing: %s %v", b, err)
			}
			checkContractRechecks(t, filepath.Join(root, ".atm/runs/t"), out, rounds, typ, "")
		})
	}
}

func TestRunContractReviewDocsRepairRejectsSourceChange(t *testing.T) {
	root, out := contractReviewRepo(t)
	configureRecheckTrace(t, root, "docs", "README.md")
	t.Setenv("FAKE_AGENT_WORK", docsRepairAgentWork)
	t.Setenv("FAKE_CONTRACT_REPORT", repairContract)
	t.Setenv("FAKE_CONTRACT_REPAIR", "echo repaired > README.md; echo source-change > a.txt")
	t.Setenv("FAKE_CONTRACT_AFTER_REPAIR_FILE", "README.md")
	t.Setenv("FAKE_CONTRACT_AFTER_REPAIR_REPORT", passedContract)
	docsIssue := strings.Replace(issue, "Type: fix", "Type: docs", 1)
	err := Run("t", []string{issueFile(t, docsIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not docs") {
		t.Fatalf("docs repair accepted a source change: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
		t.Fatal("delivery ran after a source change")
	}
	var rechecks struct {
		Reason   string       `json:"reason"`
		Reproofs []unitResult `json:"reproofs"`
		Commands []cmdResult  `json:"commands"`
	}
	b, err := os.ReadFile(filepath.Join(root, ".atm/runs/t/contract-rechecks-0.json"))
	if err != nil || json.Unmarshal(b, &rechecks) != nil || len(rechecks.Reproofs) != 1 ||
		rechecks.Reproofs[0].Type != "docs" || rechecks.Reproofs[0].Result != "failed" ||
		!strings.Contains(rechecks.Reproofs[0].Result, "failed") || !strings.Contains(rechecks.Reason, "not docs") {
		t.Fatalf("docs scope proof did not reject repaired source: %s %v", b, err)
	}
	if got := strings.TrimSpace(readRecheckTrace(t, out)); got != "repair:install" {
		t.Fatalf("checks after the failed docs proof ran: %q", got)
	}
}

const docsRepairAgentWork = "echo original > README.md; mkdir -p .atm; " +
	"echo '{\"test_file\":\"\"}' > .atm/fake-report.json"

func readRecheckTrace(t *testing.T, out string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, "recheck.trace"))
	if err != nil {
		t.Fatalf("repair execution trace missing: %v", err)
	}
	return string(b)
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
	err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
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
			err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "delivery metadata") {
				t.Fatalf("metadata mutation accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(out, "delivered")); !os.IsNotExist(err) {
				t.Fatal("delivery executed despite metadata mutation")
			}
		})
	}
}
