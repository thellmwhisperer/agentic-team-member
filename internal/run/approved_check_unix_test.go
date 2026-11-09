//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRealApprovedCheckShowsWhoApproved(t *testing.T) {
	bin := builtATM(t)
	cfg := atmSet(atmYAML, "test", "echo failed-check-finding; exit 2")
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := startVersionServe(t, bin, top)
	_, label := failedCheckQuestion(t, bin, top, "run", "--follow", issueFile(t, issue))
	failed := readReport(t, filepath.Join(runDir(top, label), "report.json"))
	rejectAnonymousApproval(t, bin, top, label)
	before := time.Now()
	cmd := exec.Command(bin, "answer", label, "approve", "--by", "operator")
	cmd.Dir = top
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	after := time.Now()
	shown := decisionTerminal(t, startFollow(t, bin, top, true, "attach", label), 0)
	if strings.Contains(shown, "✗") ||
		!regexp.MustCompile(`✓ Checks[^\n]*approved by operator`).MatchString(shown) ||
		!regexp.MustCompile(`✓ test[^\n]*approved`).MatchString(shown) {
		t.Fatalf("approved check still looks failed or omits its approver:\n%s", shown)
	}
	rep := readReport(t, filepath.Join(runDir(top, label), "report.json"))
	decisions, _ := rep["decisions"].([]any)
	if len(decisions) != 1 {
		t.Fatalf("want one decision: %v", rep)
	}
	d := decisions[0].(obj)
	at, _ := d["at"].(string)
	answered, err := time.Parse(time.RFC3339Nano, at)
	if d["node"] != "checks" || d["answer"] != "approve" || d["by"] != "operator" ||
		!strings.Contains(d["finding"].(string), "test: exit status 2") ||
		err != nil || answered.Before(before) || answered.After(after) {
		t.Fatalf("decision lost finding, answer, approver or answer time: %v (%v)", d, err)
	}
	checks := reportNode(t, rep, "checks")
	if checks["result"] != "failed" || !reflect.DeepEqual(checks, reportNode(t, failed, "checks")) ||
		!reflect.DeepEqual(rep["commands"], failed["commands"]) {
		t.Fatalf("approval erased the failed check: %v", checks)
	}
	t.Logf("final atm screen:\n%s\ndecision: %v", shown, d)
	endDeliveryServer(t, server, top)
	server = startVersionServe(t, bin, top)
	reopened := decisionTerminal(t, startFollow(t, bin, top, true, "attach", label), 0)
	approved := regexp.MustCompile(`✓ Checks[^\n]*approved by operator`)
	if strings.Contains(reopened, "✗") || !approved.MatchString(reopened) ||
		approved.FindString(reopened) != approved.FindString(shown) {
		t.Fatalf("server restart lost the approved Checks row, duration or approver:\n%s", reopened)
	}
	if restored := readReport(t, filepath.Join(runDir(top, label), "report.json")); !reflect.DeepEqual(restored, rep) {
		t.Fatal("reopening the run changed its saved report")
	}
	t.Logf("atm screen after server restart:\n%s", reopened)
	endDeliveryServer(t, server, top)
}

func TestRealApprovedCheckRestoresItsOwnApprover(t *testing.T) {
	bin := builtATM(t)
	cfg := atmSet(atmYAML, "delivery", "echo delivery-finding; exit 1") +
		"e2e: 'echo failed-check-finding; exit 2'\n"
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := startVersionServe(t, bin, top)
	_, label := failedCheckQuestion(t, bin, top, "run", "--follow", issueFile(t, issue))
	answerFailedCheck(t, bin, top, label, "fix", 1)
	answerFailedCheck(t, bin, top, label, "approve", 4)
	cmd := exec.Command(bin, "answer", label, "approve", "--by", "publisher")
	cmd.Dir = top
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("approve delivery: %v\n%s", err, out)
	}
	startFollow(t, bin, top, false, "attach", label).exit(0)
	endDeliveryServer(t, server, top)
	server = startVersionServe(t, bin, top)
	shown := decisionTerminal(t, startFollow(t, bin, top, true, "attach", label), 0)
	if !regexp.MustCompile(`✓ Checks[^\n]*approved by operator`).MatchString(shown) ||
		!regexp.MustCompile(`✓ e2e[^\n]*approved`).MatchString(shown) || strings.Contains(shown, "✗") {
		t.Fatalf("server restart confused check and delivery approvals or lost approved child rows:\n%s", shown)
	}
	t.Logf("atm screen after separate Checks and Delivery approvals:\n%s", shown)
	endDeliveryServer(t, server, top)
}

func rejectAnonymousApproval(t *testing.T, bin, top, label string) {
	t.Helper()
	for _, flags := range [][]string{nil, {"--by", "   "}} {
		cmd := exec.Command(bin, append([]string{"answer", label, "approve"}, flags...)...)
		cmd.Dir = top
		if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "--by") {
			t.Fatalf("answer without an approver must fail naming --by: %v\n%s", err, out)
		}
		assertCheckWaiting(t, top, label)
		assertQuestionNextStep(t, top, label, true)
	}
}
