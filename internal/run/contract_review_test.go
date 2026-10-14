package run

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reviewIssue is an issue with criteria and invariants, which the contract reviewer gets whole.
const reviewIssue = "# Retry on timeout\nType: fix\n\n## Acceptance\n\n1. Retry once on a timeout.\n\n" +
	"## Invariants\n\nNo second retry.\n"

// reviewDelivery is a delivery command that records in $ATM_TEST_OUT its HEAD, its clone and each call.
const reviewDelivery = `git rev-parse HEAD > "$ATM_TEST_OUT/head"; printenv ATM_CLONE > "$ATM_TEST_OUT/clone"; ` +
	`echo ran >> "$ATM_TEST_OUT/calls"`

// blocked is a contract review that blocks on a criterion of reviewIssue, with its evidence.
const blocked = `{"disposition": "blocked", "base": "{base}", "head": "{head}", "findings": [{"criterion": ` +
	`"Retry once on a timeout.", "evidence": "a.txt:1 writes fixed and never retries"}], "summary": "s"}`

// reviewRun runs reviewIssue in a repository under atm, env set as key, value pairs, and returns the run's
// directory, the base commit, where the delivery command recorded, where the agents' calls went and its error.
func reviewRun(t *testing.T, atm string, env ...string) (runDir, base, got, call string, err error) {
	t.Helper()
	root := repo(t, "https://example.com/owner/repo.git", atm)
	got, call = t.TempDir(), filepath.Join(t.TempDir(), "call.json")
	t.Setenv("ATM_TEST_OUT", got)
	t.Setenv("FAKE_AGENT_CALL", call)
	for ; len(env) > 1; env = env[2:] {
		t.Setenv(env[0], env[1])
	}
	err = Run("t", []string{issueFile(t, reviewIssue)}, &bytes.Buffer{}, &bytes.Buffer{})
	top := gitT(t, root, "rev-parse", "--show-toplevel")
	return filepath.Join(top, ".atm", "runs", "t"), gitT(t, root, "rev-parse", "HEAD"), got, call, err
}

// contractReview is contract-review.json in the run's directory.
func contractReview(t *testing.T, runDir string) obj {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir, "contract-review.json"))
	var rec obj
	if err == nil {
		err = json.Unmarshal(b, &rec)
	}
	if err != nil {
		t.Fatalf("contract-review.json: %v", err)
	}
	return rec
}

// notDelivered checks the run failed at delivery naming why, the delivery command never ran and the persisted
// review holds no approval, and its reason why.
func notDelivered(t *testing.T, err error, runDir, got, why string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), why) {
		t.Fatalf("want the run failed naming %q, got %v", why, err)
	}
	if _, e := os.Stat(filepath.Join(got, "calls")); e == nil {
		t.Fatal("the delivery command ran")
	}
	if rep := readReport(t, filepath.Join(runDir, "report.json")); rep["failed_node"] != "delivery" {
		t.Fatalf("want delivery failed, got %v", rep["failed_node"])
	}
	rec := contractReview(t, runDir)
	if reason, _ := rec["reason"].(string); rec["approved"] == true || !strings.Contains(reason, why) {
		t.Fatalf("want the review not approved, its reason naming %q, got %v", why, rec)
	}
}

func TestRunContractReviewApprovesTheDeliveredCommit(t *testing.T) {
	runDir, base, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery))
	if err != nil {
		t.Fatal(err)
	}
	read := reader(t, got)
	if calls := read("calls"); calls != "ran" {
		t.Fatalf("want the delivery command run once, got %q", calls)
	}
	head := read("head")
	rec := contractReview(t, runDir)
	if rec["approved"] != true || rec["head"] != head || rec["base"] != base || rec["issue"] != reviewIssue {
		t.Fatalf("want the approval of base %s and HEAD %s, for the issue, got %v", base, head, rec)
	}
	if report, _ := rec["report"].(map[string]any); report["disposition"] != "approved" {
		t.Fatalf("want the reviewer's report persisted, got %v", rec["report"])
	}
}

func TestRunContractReviewBlocksTheDelivery(t *testing.T) {
	runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery), "FAKE_REVIEW_REPORT", blocked)
	notDelivered(t, err, runDir, got, "Retry once on a timeout.")
	if !strings.Contains(err.Error(), "a.txt:1 writes fixed and never retries") {
		t.Fatalf("want the evidence kept, got %v", err)
	}
}

func TestRunContractReviewRejectsAnInvalidReport(t *testing.T) {
	approved := `{"disposition": "approved", "base": "{base}", "head": "{head}", "findings": [], "summary": "s"}`
	other := "0123456789012345678901234567890123456789"
	cases := map[string][3]string{ // the variable, its value, why the review fails
		"absent":              {"FAKE_REVIEW", "no-report", "agent left no report"},
		"invalid JSON":        {"FAKE_REVIEW_REPORT", `{"disposition": "approved", "base": 1}`, "invalid contract review"},
		"unknown disposition": {"FAKE_REVIEW_REPORT", strings.Replace(approved, `"approved"`, `"maybe"`, 1), `"maybe"`},
		"other HEAD":          {"FAKE_REVIEW_REPORT", strings.Replace(approved, "{head}", other, 1), "HEAD " + other},
		"other base":          {"FAKE_REVIEW_REPORT", strings.Replace(approved, "{base}", other, 1), "base " + other},
		"blocked, no finding": {"FAKE_REVIEW_REPORT", strings.Replace(approved, `"approved"`, `"blocked"`, 1), `"blocked"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery), c[0], c[1])
			notDelivered(t, err, runDir, got, c[2])
		})
	}
}

func TestRunContractReviewMustNotTouchTheClone(t *testing.T) {
	cases := map[string]string{
		"modifies a file": "echo changed >> a.txt",
		"moves HEAD":      "git -c user.name=r -c user.email=r@example.com commit -q --allow-empty -m moved",
		"adds a file":     "echo new > new.txt",
	}
	for name, work := range cases {
		t.Run(name, func(t *testing.T) {
			runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery), "FAKE_REVIEW_WORK", work)
			notDelivered(t, err, runDir, got, "contract reviewer")
		})
	}
}

func TestRunContractReviewGetsTheIssueAndTheCommittedDiff(t *testing.T) {
	for _, kept := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed by delivery", true: "committed by ponytail"}[kept], func(t *testing.T) {
			var env []string
			if kept { // the cut leaves the work in commits, nothing uncommitted for delivery
				env = []string{"FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt" + fixTest,
					"FAKE_PONYTAIL_WORK", "echo fixed > a.txt", "FAKE_PONYTAIL_REPORT", cut}
			}
			_, base, got, call, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery), env...)
			if err != nil {
				t.Fatal(err)
			}
			read := reader(t, got)
			diff := gitT(t, read("clone"), "diff", base, read("head"))
			brief := agentCall(t, call+".review").Stdin
			if diff == "" || !strings.Contains(brief, diff) || !strings.Contains(brief, reviewIssue) {
				t.Fatalf("want the brief to hold the issue and the diff %s..HEAD:\n%s\ngot:\n%s", base, diff, brief)
			}
		})
	}
}

func TestRunContractReviewNeedsADeliveryAndPassedChecks(t *testing.T) {
	runDir, _, _, call, err := reviewRun(t, atmYAML)
	if err != nil {
		t.Fatal(err)
	}
	if n := reportNode(t, readReport(t, filepath.Join(runDir, "report.json")), "delivery"); n["result"] != "skipped" {
		t.Fatalf("want delivery skipped, got %v", n)
	}
	if _, e := os.Stat(call + ".review"); e == nil {
		t.Fatal("the contract reviewer ran without a delivery command")
	}
	_, _, got, call, err := reviewRun(t, atmSet(atmSet(atmYAML, "delivery", reviewDelivery), "lint", "false"))
	if err == nil {
		t.Fatal("want the run failed at its checks")
	}
	for _, path := range []string{call + ".review", filepath.Join(got, "calls")} {
		if _, e := os.Stat(path); e == nil {
			t.Fatalf("%s exists after failed checks", path)
		}
	}
}
