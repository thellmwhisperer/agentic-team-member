//go:build unix

package run

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestRunAsksOnAFailedCheckResumesLatestAttempt(t *testing.T) {
	builtATM(t)
	top := gitT(t, repo(t, "local", atmYAML+"e2e: 'echo failed-check-finding; exit 1'\n"),
		"rev-parse", "--show-toplevel")
	order := filepath.Join(t.TempDir(), "order")
	t.Setenv("FAKE_AGENT_ORDER", order)
	t.Setenv("FAKE_AGENT_STREAM", `{"type":"system","subtype":"init","session_id":"sess-1"}`+"\n")
	t.Setenv("FAKE_AGENT_WORK", "if test -f first-turn; then echo broken > a.txt; echo kept > fix-work.txt; else "+fixWork+"; touch first-turn; fi")
	bin := decisionServer(t, top)
	f := startFollow(t, bin, top, false, "run", "--follow", "--harness", "claude", issueFile(t, issue))
	clone, label := failedCheckQuestion(t, f, top)
	f.exit(1)
	assertSaved := func(label, row, output string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(runDir(top, label), "resume", "resume.json"))
		var saved resumePoint
		if err != nil || json.Unmarshal(data, &saved) != nil || saved.FailedRow != row || saved.E2EOutput != output {
			t.Fatalf("%s saved the wrong Checks failure: %s, %v", label, data, err)
		}
	}
	assertSaved(label, "e2e", "failed-check-finding")
	prior := e2eReport(t, top, label)
	cmd := exec.Command(bin, "answer", label, "fix")
	cmd.Dir = top
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fix: %v %s", err, out)
	}
	startFollow(t, bin, top, false, "attach", label).exit(1)
	assertSaved(label, "", "")
	stopCheckRun(t, top, label)
	assertSaved(label, "", "")
	rep := e2eReport(t, top, label)
	if !rep.Resumable || rep.FailedNode != "checks" || !strings.Contains(rep.Reason, "a_test.sh fails with the fix") ||
		len(rep.Decisions) != 2 || rep.Decisions[0].Answer != "fix" || rep.Decisions[1].Answer != "stop" ||
		!strings.Contains(rep.Decisions[0].Finding, "failed-check-finding") ||
		!strings.Contains(rep.Decisions[1].Finding, "a_test.sh fails with the fix") {
		t.Fatalf("latest failure or decision history lost: %+v", rep)
	}
	if len(prior.Commands) != 2 || prior.Commands[1].Name != "e2e" || prior.Commands[1].Result != "failed" ||
		prior.Commands[1].Tail != "failed-check-finding" || len(rep.Commands) != len(prior.Commands) {
		t.Fatalf("command history lost or proof failure ran commands: before %+v, after %+v", prior.Commands, rep.Commands)
	}
	for i, command := range prior.Commands {
		if rep.Commands[i] != command {
			t.Fatalf("command history changed: before %+v, after %+v", command, rep.Commands[i])
		}
	}
	evs := followedRun(t, bin, top, 1, "--resume", label)
	resumedClone := ""
	for _, ev := range evs {
		if ev["step"] == "agent turn after e2e" {
			t.Fatalf("resume used stale e2e failure: %v", ev)
		}
		if ev["step"] == "clone" && ev["state"] == "passed" {
			resumedClone, _ = ev["clone"].(string)
		}
	}
	if calls := workerCalls(t, order); calls != 2 {
		t.Fatalf("want initial worker and one fix turn, got %d", calls)
	}
	resumed := lastRun(t, top, "failed")
	assertSaved(resumed, "", "")
	if r := e2eReport(t, top, resumed); !r.Resumable || !strings.Contains(r.Reason, "a_test.sh fails with the fix") {
		t.Fatalf("resume lost current failure or saved work: %+v", r)
	}
	if resumedClone == "" || resumedClone == clone {
		t.Fatalf("resume did not restore a separate clone: %q", resumedClone)
	}
	for _, dir := range []string{clone, resumedClone} {
		for name, want := range map[string]string{"a.txt": "broken\n", "fix-work.txt": "kept\n"} {
			if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(data) != want {
				t.Fatalf("%s lost %s: %q, %v", dir, name, data, err)
			}
		}
	}
}

func TestRunAsksOnAFailedCheckOnTerminal(t *testing.T) {
	builtATM(t)
	top := gitT(t, repo(t, "local", atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1")),
		"rev-parse", "--show-toplevel")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	bin := decisionServer(t, top)
	for _, args := range [][]string{{"run", issueFile(t, issue)}, {"attach", "issue-1"}} {
		shown := decisionTerminal(t, startFollow(t, bin, top, true, args...), 1)
		for _, want := range []string{"Decision", "failed-check-finding", "Answer: atm answer issue-1", "approve|fix|stop"} {
			if !strings.Contains(shown, want) {
				t.Fatalf("%s omitted %q:\n%s", args[0], want, shown)
			}
		}
	}
	cmd := exec.Command(bin, "answer", "issue-1", "stop")
	cmd.Dir = top
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stop: %v %s", err, out)
	}
	shown := decisionTerminal(t, startFollow(t, bin, top, true, "attach", "issue-1"), 1)
	if strings.Contains(shown, "Decision") || strings.Contains(shown, "approve|fix|stop") {
		t.Fatalf("answered question still asks for an answer:\n%s", shown)
	}
}

func decisionTerminal(t *testing.T, f *followed, code int) string {
	t.Helper()
	var shown strings.Builder
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line, open := <-f.lines:
			if !open {
				_ = f.cmd.Wait()
				if f.cmd.ProcessState.ExitCode() != code {
					t.Fatalf("terminal exit = %d, want %d: %s", f.cmd.ProcessState.ExitCode(), code, f.errors())
				}
				_, final, ok := strings.Cut(shown.String(), "\x1b[?1049l")
				if !ok {
					t.Fatalf("terminal did not restore its screen:\n%s", shown.String())
				}
				return ansi.Strip(final)
			}
			shown.WriteString(line + "\n")
		case <-timer.C:
			t.Fatalf("terminal did not finish:\n%s", shown.String())
		}
	}
}

func TestRunAsksOnAFailedCheckApprovalKeepsValidCut(t *testing.T) {
	builtATM(t)
	delivered, lintOK := filepath.Join(t.TempDir(), "delivered"), filepath.Join(t.TempDir(), "lint-ok")
	t.Setenv("ATM_TEST_LINT_OK", lintOK)
	cfg := atmSet(atmSet(atmYAML, "lint", `test -f "$ATM_TEST_LINT_OK" || { touch "$ATM_TEST_LINT_OK"; echo failed-check-finding; exit 1; }`),
		"delivery", "cat a.txt > "+delivered)
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest)
	t.Setenv("FAKE_PONYTAIL_WORK", "echo fixed > a.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	bin := decisionServer(t, top)
	f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
	_, label := failedCheckQuestion(t, f, top)
	f.exit(1)
	cmd := exec.Command(bin, "answer", label, "approve")
	cmd.Dir = top
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("approve: %v %s", err, out)
	}
	startFollow(t, bin, top, false, "attach", label).exit(0)
	rep := e2eReport(t, top, label)
	if len(rep.Decisions) != 1 || rep.Decisions[0].Answer != "approve" ||
		rep.node("ponytail").Kept == nil || !*rep.node("ponytail").Kept {
		t.Fatalf("approved check rejected the unchanged test: %+v; %+v", rep.Decisions, rep.node("ponytail"))
	}
	if data, err := os.ReadFile(delivered); err != nil || string(data) != "fixed\n" {
		t.Fatalf("valid cut was not delivered: %q, %v", data, err)
	}
}

func TestRunAsksOnAFailedCheckFixWithoutReport(t *testing.T) {
	builtATM(t)
	for _, report := range []string{"Done without JSON.", `{"summary":"missing test_file"}`} {
		for _, repair := range []bool{false, true} {
			t.Run(report+map[bool]string{false: "/still failing", true: "/repaired"}[repair], func(t *testing.T) {
				delivery := filepath.Join(t.TempDir(), "delivered")
				cfg := atmSet(atmSet(atmYAML, "lint", "test -f repaired || { echo failed-check-finding; exit 1; }"),
					"delivery", "pwd > "+delivery)
				top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
				order := filepath.Join(t.TempDir(), "order")
				t.Setenv("FAKE_AGENT_ORDER", order)
				t.Setenv("FAKE_REPORT_1", validWorker)
				t.Setenv("FAKE_REPORT_2", report)
				work := reportWork + "\nif test -f first-turn; then echo kept > fix-work.txt; "
				if repair {
					work += "touch repaired; "
				}
				t.Setenv("FAKE_AGENT_WORK", work+"else touch first-turn; fi")
				bin := decisionServer(t, top)
				f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
				clone, label := failedCheckQuestion(t, f, top)
				f.exit(1)
				cmd := exec.Command(bin, "answer", label, "fix")
				cmd.Dir = top
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fix: %v %s", err, out)
				}
				startFollow(t, bin, top, false, "attach", label).exit(map[bool]int{false: 1, true: 0}[repair])
				if calls := workerCalls(t, order); calls != 2 {
					t.Fatalf("want initial worker and one fix turn, got %d", calls)
				}
				rep := e2eReport(t, top, label)
				if rep.node("agent").Result != "passed" || rep.node("agent").Exit != "" ||
					rep.node("agent").Tail != nil {
					t.Fatalf("missing report failed the agent: %+v", rep.node("agent"))
				}
				if repair {
					assertCheckAnswer(t, top, label, clone, delivery, "fix")
					return
				}
				if !rep.Resumable || len(rep.Decisions) != 2 || rep.Decisions[1].Answer != "" ||
					!strings.Contains(rep.Decisions[1].Finding, "failed-check-finding") {
					t.Fatalf("missing report lost the question or saved work: %+v", rep)
				}
				if data, err := os.ReadFile(filepath.Join(clone, "fix-work.txt")); err != nil || string(data) != "kept\n" {
					t.Fatalf("missing report lost fix work: %q, %v", data, err)
				}
				cmd = exec.Command(bin, "answer", label, "stop")
				cmd.Dir = top
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("stop: %v %s", err, out)
				}
				startFollow(t, bin, top, false, "attach", label).exit(1)
			})
		}
	}
}

func TestRunAsksOnAFailedCheckCleanupAfterAssertion(t *testing.T) {
	if marker := os.Getenv("ATM_DECISION_CLEANUP_TEST"); marker != "" {
		builtATM(t)
		top := gitT(t, repo(t, "local", atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1")),
			"rev-parse", "--show-toplevel")
		bin := decisionServer(t, top)
		writeFixture(t, marker, top)
		f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
		_, label := failedCheckQuestion(t, f, top)
		f.exit(1)
		assertCheckWaiting(t, top, label)
		t.Fatal("intentional assertion failure with pending question")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "server-root")
	t.Cleanup(func() {
		if data, err := os.ReadFile(marker); err == nil {
			top := string(data)
			if _, err := os.Stat(filepath.Join(top, ".atm", "serve.log")); err == nil {
				killServeProcesses(t, top)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestRunAsksOnAFailedCheckCleanupAfterAssertion$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ATM_DECISION_CLEANUP_TEST="+marker)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err == nil || !strings.Contains(string(out), "intentional assertion failure with pending question") {
		t.Fatalf("pending-question assertion did not fail cleanly: %v, %v\n%s", ctx.Err(), err, out)
	}
}

func TestRunAsksOnAFailedCheckCleanupStopsPendingRun(t *testing.T) {
	builtATM(t)
	top := gitT(t, repo(t, "local", atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1")),
		"rev-parse", "--show-toplevel")
	bin := filepath.Join(t.TempDir(), "atm")
	buildATM(t, bin, "follow-test")
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := startVersionServe(t, bin, top)
	f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
	clone, label := failedCheckQuestion(t, f, top)
	f.exit(1)
	endServe(bin, top, server)
	if server.ProcessState == nil || !server.ProcessState.Success() {
		t.Fatalf("cleanup killed the server instead of resolving its question: %v", server.ProcessState)
	}
	rep := e2eReport(t, top, label)
	if len(rep.Decisions) != 1 || rep.Decisions[0].Answer != "stop" || len(rep.Units) != 1 || !rep.Resumable {
		t.Fatalf("cleanup did not finish and retain the stopped run: %+v", rep)
	}
	if data, err := os.ReadFile(filepath.Join(clone, "a.txt")); err != nil || string(data) != "fixed\n" {
		t.Fatalf("cleanup lost the stopped run's work: %q, %v", data, err)
	}
}
