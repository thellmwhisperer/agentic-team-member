package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func repairATM() string {
	atm := atmSet(atmYAML, "delivery", reviewDelivery+`; git status --porcelain > "$ATM_TEST_OUT/status"`)
	atm = atmSet(atm, "test", `echo check-test >> "$FAKE_AGENT_ORDER"`)
	atm = atmSet(atm, "typecheck", `echo check-typecheck >> "$FAKE_AGENT_ORDER"`)
	atm = atmSet(atm, "lint", `echo check-lint >> "$FAKE_AGENT_ORDER"`)
	return atmSet(atm, "test_file", `echo proof >> "$FAKE_AGENT_ORDER"; sh {file}`)
}

func repairBlocked(t *testing.T, criterion, evidence string, slicing obj) string {
	t.Helper()
	if slicing == nil {
		slicing = slicingReport()
	}
	b, err := json.Marshal(obj{"disposition": "blocked", "base": "{base}", "head": "{head}",
		"findings": []obj{{"criterion": criterion, "evidence": evidence}}, "summary": "blocked",
		"task_slicing": slicing})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func repairEnv(t *testing.T, reports, works []string) string {
	t.Helper()
	order := filepath.Join(t.TempDir(), "order")
	t.Setenv("FAKE_AGENT_ORDER", order)
	for i, report := range reports {
		t.Setenv("FAKE_REVIEW_REPORT_"+strconv.Itoa(i+1), report)
	}
	for i, work := range works {
		t.Setenv("FAKE_REPAIR_WORK_"+strconv.Itoa(i+1), work)
	}
	return order
}

func repairLogs(t *testing.T, runDir string) int {
	t.Helper()
	logs, err := filepath.Glob(filepath.Join(runDir, "repair-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return len(logs)
}

func repairRecord(t *testing.T, runDir string) obj {
	t.Helper()
	got, _ := contractReview(t, runDir)["repair"].(map[string]any)
	if got == nil {
		t.Fatalf("want repair evidence, got %v", contractReview(t, runDir))
	}
	return got
}

func assertReviewAfterChecks(t *testing.T, order string) {
	t.Helper()
	b, err := os.ReadFile(order)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	repairAt, checkAt, reviewAt := -1, -1, -1
	for i, line := range lines {
		switch line {
		case "repair":
			repairAt, checkAt, reviewAt = i, -1, -1
		case "proof", "check-test", "check-typecheck", "check-lint":
			if repairAt >= 0 {
				checkAt = i
			}
		case "review":
			if checkAt >= 0 {
				reviewAt = i
			}
		}
	}
	if repairAt < 0 || checkAt < repairAt || reviewAt < checkAt {
		t.Fatalf("want the new review after checks after repair, got %v", lines)
	}
}

const repairGrow = `printf 'fixed\nretry once\nextra\n' > a.txt`

func TestContractReviewRepairDeliversAfterOneFix(t *testing.T) {
	blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 writes fixed and never retries", nil)
	order := repairEnv(t, []string{blocked, slicingJSON(t, slicingReport())}, []string{repairGrow})
	runDir, _, got, call, err := reviewRun(t, repairATM())
	if err != nil {
		t.Fatal(err)
	}
	assertOneDelivery(t, runDir, got, order, call)
}

func assertOneDelivery(t *testing.T, runDir, got, order, call string) {
	t.Helper()
	rec, rep := contractReview(t, runDir), repairRecord(t, runDir)
	heads, _ := rep["heads"].([]any)
	read := reader(t, got)
	if !oneApprovedDelivery(rep, rec, heads, read) {
		t.Fatalf("want one delivery of the new approved head, got %v %v", rec, rep)
	}
	if repairLogs(t, runDir) != 1 {
		t.Fatalf("want one repair worker, got %d", repairLogs(t, runDir))
	}
	assertReviewAfterChecks(t, order)
	assertRepairCommit(t, read("clone"), order)
	assertRepairBrief(t, agentCall(t, call+".repair1").Stdin)
}

func oneApprovedDelivery(rep, rec obj, heads []any, read func(string) string) bool {
	return rep["rounds"] == float64(1) && len(heads) == 2 && heads[0] != heads[1] && rec["head"] == heads[1] &&
		rec["approved"] == true && rec["issue"] == reviewIssue && read("head") == heads[1] &&
		read("calls") == "ran" && read("status") == ""
}

func assertRepairCommit(t *testing.T, clone, order string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(clone, "a.txt"))
	if err != nil || strings.Count(string(body), "\n") < 2 {
		t.Fatalf("a repair that grows to meet the contract was rejected: %q %v", body, err)
	}
	authors := strings.Split(gitT(t, clone, "log", "-2", "--format=%an <%ae>"), "\n")
	msg := gitT(t, clone, "log", "-1", "--format=%B")
	if len(authors) != 2 || authors[0] == "" || authors[0] != authors[1] || !strings.Contains(msg, "round 1") ||
		!strings.Contains(msg, "Retry on timeout") {
		t.Fatalf("want the repo identity and a traceable repair commit, got %q %q", authors, msg)
	}
	ord, err := os.ReadFile(order)
	if err != nil || strings.Count(string(ord), "ponytail\n") != 1 {
		t.Fatalf("cut-only policy must stay on ponytail, got %s %v", ord, err)
	}
}

func assertRepairBrief(t *testing.T, brief string) {
	t.Helper()
	for _, want := range []string{"Retry once on a timeout.", "a.txt:1 writes fixed and never retries",
		"minimal", "existing tests"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("repair brief missing %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "No logging changes.") || strings.Contains(brief, "# Contract review") {
		t.Fatalf("repair brief carried more than the violation:\n%s", brief)
	}
}

func TestContractReviewRepairStopsAfterTwoRounds(t *testing.T) {
	same := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 still never retries", nil)
	other := repairBlocked(t, "No second retry.", "a.txt:3 retries twice", nil)
	t.Run("same violation", func(t *testing.T) {
		repairEnv(t, []string{same, same, same}, []string{repairGrow, repairGrow})
		runDir, _, got, _, err := reviewRun(t, repairATM())
		assertStopped(t, err, runDir, got, 2, "Retry once on a timeout.")
	})
	t.Run("different violations", func(t *testing.T) {
		call := filepath.Join(t.TempDir(), "call.json")
		repairEnv(t, []string{same, other, other}, []string{repairGrow, repairGrow})
		runDir, _, got, _, err := reviewRun(t, repairATM(), "FAKE_AGENT_CALL", call)
		assertStopped(t, err, runDir, got, 2, "No second retry.")
		second := agentCall(t, call+".repair2").Stdin
		if !strings.Contains(second, "No second retry.") || strings.Contains(second, "Retry once on a timeout.") {
			t.Fatalf("round 2 must cite only the new violation:\n%s", second)
		}
	})
}

func assertStopped(t *testing.T, err error, runDir, got string, rounds int, criterion string) {
	t.Helper()
	if _, e := os.Stat(filepath.Join(got, "calls")); e == nil {
		t.Fatal("delivery ran")
	}
	if repairLogs(t, runDir) != rounds {
		t.Fatalf("want %d repair calls, got %d", rounds, repairLogs(t, runDir))
	}
	if _, e := os.Stat(filepath.Join(runDir, "repair-3.jsonl")); e == nil {
		t.Fatal("a third repair ran")
	}
	rep := repairRecord(t, runDir)
	draft, _ := rep["draft"].(map[string]any)
	if rep["rounds"] != float64(rounds) || rep["cause"] == "" || draft["criterion"] == "" ||
		!strings.Contains(draft["criterion"].(string)+rep["cause"].(string), criterion) {
		t.Fatalf("want cause, attempts and a continuation draft, got %v err %v", rep, err)
	}
	if contractReview(t, runDir)["approved"] == true {
		t.Fatal("a previous approval stayed current")
	}
}

func TestContractReviewRepairSkipsWorker(t *testing.T) {
	unready := slicingReport()
	unready["disposition"] = "unready"
	unready["fields"].(obj)["acceptance"] = obj{"status": "missing", "reason": "No runnable check"}
	split := slicingReport()
	split["disposition"] = "split-required"
	split["drafts"] = []obj{slicingDraft("split"), slicingDraft("split")}
	cases := map[string][2]string{
		"unready":        {"FAKE_REVIEW_REPORT", repairBlocked(t, "Retry once on a timeout.", "missing", unready)},
		"split-required": {"FAKE_REVIEW_REPORT", repairBlocked(t, "Retry once on a timeout.", "two contracts", split)},
		"invalid report": {"FAKE_REVIEW_REPORT", `{"disposition":"approved"}`},
		"mutated clone":  {"FAKE_REVIEW_WORK", "echo changed > touched.txt"},
	}
	want := map[string]string{"unready": "unready", "split-required": "split-required",
		"invalid report": "invalid contract review", "mutated clone": "contract reviewer"}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			repairEnv(t, nil, nil)
			runDir, _, got, _, err := reviewRun(t, repairATM(), env[0], env[1])
			if err == nil || !strings.Contains(err.Error(), want[name]) {
				t.Fatalf("want a block naming %q, got %v", want[name], err)
			}
			if _, e := os.Stat(filepath.Join(got, "calls")); e == nil || repairLogs(t, runDir) != 0 {
				t.Fatalf("want no delivery and no repair worker, got %v logs %d", err, repairLogs(t, runDir))
			}
		})
	}
}

func TestContractReviewRepairNeedsOriginalCriterion(t *testing.T) {
	t.Run("owner", func(t *testing.T) {
		repairEnv(t, nil, nil)
		report := repairBlocked(t, "Please add logging.", "log.go:1 adds a logger", nil)
		runDir, _, got, _, err := reviewRun(t, repairATM(), "FAKE_REVIEW_REPORT", report)
		assertUnmatched(t, err, runDir, got, "owner")
	})
	t.Run("optional", func(t *testing.T) {
		repairEnv(t, nil, nil)
		s := slicingReport()
		d := slicingDraft("optional")
		d["contract"].(obj)["evidence"] = "Please add logging."
		s["drafts"] = []obj{d}
		report := repairBlocked(t, "Please add logging.", "log.go:1 adds a logger", s)
		runDir, _, got, _, err := reviewRun(t, repairATM(), "FAKE_REVIEW_REPORT", report)
		assertUnmatched(t, err, runDir, got, "optional")
		if !strings.Contains(string(mustRead(t, filepath.Join(runDir, "contract-review.json"))), "Please add logging.") {
			t.Fatal("optional improvement was dropped")
		}
	})
}

func assertUnmatched(t *testing.T, err error, runDir, got, want string) {
	t.Helper()
	if err == nil {
		t.Fatal("want the finding returned, not delivered")
	}
	if _, e := os.Stat(filepath.Join(got, "calls")); e == nil || repairLogs(t, runDir) != 0 {
		t.Fatal("a finding without an original criterion was repaired")
	}
	rep := repairRecord(t, runDir)
	unmatched, _ := rep["unmatched"].([]any)
	if len(unmatched) != 1 || unmatched[0].(map[string]any)["disposition"] != want {
		t.Fatalf("want disposition %s, got %v", want, rep)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestContractReviewRepairBlocksProtectedTestEdits(t *testing.T) {
	t.Run("modifies", func(t *testing.T) {
		blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 never retries", nil)
		repairEnv(t, []string{blocked}, []string{"echo weakened > a_test.sh"})
		runDir, _, got, _, err := reviewRun(t, repairATM())
		assertNoDelivery(t, err, runDir, got, "a_test.sh")
	})
	t.Run("deletes", func(t *testing.T) {
		blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 never retries", nil)
		repairEnv(t, []string{blocked}, []string{"rm old_test.sh"})
		runDir, _, got, _, err := reviewRun(t, repairATM(), "FAKE_AGENT_WORK",
			fixWork+"; echo 'test -f a.txt' > old_test.sh")
		assertNoDelivery(t, err, runDir, got, "old_test.sh")
	})
	t.Run("new test still reruns protected", func(t *testing.T) {
		blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 never retries", nil)
		order := repairEnv(t, []string{blocked, slicingJSON(t, slicingReport())},
			[]string{`echo 'echo ok' > extra_test.sh; ` + repairGrow})
		runDir, _, got, _, err := reviewRun(t, repairATM())
		if err != nil || reader(t, got)("calls") != "ran" {
			t.Fatalf("adding a test should not block a valid repair, got %v", err)
		}
		assertReviewAfterChecks(t, order)
		if repairLogs(t, runDir) != 1 {
			t.Fatalf("want one repair, got %d", repairLogs(t, runDir))
		}
	})
}

func assertNoDelivery(t *testing.T, err error, runDir, got, why string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), why) {
		t.Fatalf("want a block naming %q, got %v", why, err)
	}
	if _, e := os.Stat(filepath.Join(got, "calls")); e == nil || contractReview(t, runDir)["approved"] == true {
		t.Fatal("protected-test edit was delivered or kept an approval")
	}
	if repairLogs(t, runDir) != 1 {
		t.Fatalf("want the failed repair and no retry, got %d", repairLogs(t, runDir))
	}
}

func TestContractReviewRepairBlocksBrokenChecks(t *testing.T) {
	blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 never retries", nil)
	t.Run("check", func(t *testing.T) {
		repairEnv(t, []string{blocked}, []string{"touch repair-broke-lint; echo fixed > a.txt"})
		atm := atmSet(repairATM(), "lint", `echo check-lint >> "$FAKE_AGENT_ORDER"; test ! -f repair-broke-lint`)
		runDir, _, got, _, err := reviewRun(t, atm)
		assertNoDelivery(t, err, runDir, got, "lint")
	})
	t.Run("proof", func(t *testing.T) {
		repairEnv(t, []string{blocked}, []string{"echo broken > a.txt"})
		runDir, _, got, _, err := reviewRun(t, repairATM())
		assertNoDelivery(t, err, runDir, got, "a_test.sh")
	})
	t.Run("earlier unit", func(t *testing.T) {
		repairEnv(t, []string{repairBlocked(t, "The retry error is reported.", "a.txt:1 drops the error", nil)},
			[]string{"printf 'broken\\nu2\\n' > a.txt"})
		t.Setenv("ATM_TEST_ISSUE", followUpIssue)
		t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then echo u2 >> a.txt; `+
			`printf '{"test_file":"c1_test.sh","follow_ups":[]}' > .atm/fake-report.json; else `+
			`printf 'fixed\n' > a.txt; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh; `+
			`mkdir -p .atm/follow-ups/1; echo 'grep -q u2 a.txt' > .atm/follow-ups/1/c1_test.sh; `+
			`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"gap","red_test":"c1_test.sh",`+
			`"criterion":"A retry runs once after a timeout.","paths":["a.txt"]}]}' > .atm/fake-report.json; fi`)
		runDir, _, got, _, err := reviewRun(t, repairATM())
		assertNoDelivery(t, err, runDir, got, "unit 1")
	})
}
