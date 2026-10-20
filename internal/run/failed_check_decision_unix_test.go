//go:build unix

package run

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRunAsksOnAFailedCheck(t *testing.T) {
	builtATM(t)
	for _, answer := range []string{"approve", "fix", "stop"} {
		t.Run(answer, func(t *testing.T) {
			delivery := filepath.Join(t.TempDir(), "delivered")
			cfg := atmSet(atmSet(atmYAML, "lint", "test -f repaired || { echo failed-check-finding; exit 1; }"),
				"delivery", "pwd > "+delivery)
			top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
			call := filepath.Join(t.TempDir(), "call")
			t.Setenv("FAKE_AGENT_CALL", call)
			t.Setenv("FAKE_AGENT_WORK", "if test -f first-turn; then touch repaired; else "+fixWork+"; touch first-turn; fi")
			bin := followUpsServer(t, top)
			clone, label := failedCheckQuestion(t, bin, top, "run", "--follow", issueFile(t, issue))
			assertCheckWaiting(t, top, label)
			if data, err := os.ReadFile(filepath.Join(clone, "a.txt")); err != nil || string(data) != "fixed\n" {
				t.Fatalf("paused run lost its work: %q, %v", data, err)
			}
			if _, err := os.Stat(delivery); !os.IsNotExist(err) {
				t.Fatalf("delivered before answer: %v", err)
			}
			for _, args := range [][]string{{label, "skip"}, {"missing", "approve"}, {label}} {
				bad := exec.Command(bin, append([]string{"answer"}, args...)...)
				bad.Dir = top
				if out, err := bad.CombinedOutput(); err == nil {
					t.Fatalf("accepted invalid answer: %s", out)
				}
			}
			cmd := exec.Command(bin, "answer", label, answer)
			cmd.Dir = top
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("answer: %v\n%s", err, out)
			}
			next := startFollow(t, bin, top, false, "attach", label)
			code := 0
			if answer == "stop" {
				code = 1
			}
			next.exit(code)
			assertCheckAnswer(t, top, label, clone, delivery, answer)
			duplicate := exec.Command(bin, "answer", label, answer)
			duplicate.Dir = top
			if out, err := duplicate.CombinedOutput(); err == nil {
				t.Fatalf("accepted duplicate answer: %s", out)
			}
			if answer == "fix" {
				// The ponytail call has its own capture file; this is the worker's last turn.
				got := agentCall(t, call)
				if !strings.Contains(got.Stdin+strings.Join(got.Args, " "), "failed-check-finding") {
					t.Fatalf("fix turn did not receive finding: %+v", got)
				}
			}
		})
	}
}

func failedCheckQuestion(t *testing.T, bin, top string, args ...string) (string, string) {
	t.Helper()
	f := startFollow(t, bin, top, false, args...)
	var clone, label string
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for label == "" {
		select {
		case line, ok := <-f.lines:
			if !ok {
				log, _ := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
				t.Fatalf("run ended without asking approve, fix or stop: %s\n%s", f.errors(), log)
			}
			var ev obj
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatal(err)
			}
			if ev["step"] == "clone" && ev["state"] == "passed" {
				clone, _ = ev["clone"].(string)
			}
			if ev["step"] == "decision" && ev["state"] == "waiting" {
				label, _ = ev["run"].(string)
				if !strings.Contains(ev["finding"].(string), "failed-check-finding") ||
					ev["choices"] != "approve, fix, stop" {
					t.Fatalf("missing finding or choices: %v", ev)
				}
			}
		case <-timer.C:
			t.Fatal("no question after failed check")
		}
	}
	f.exit(1)
	return clone, label
}

func assertCheckAnswer(t *testing.T, top, label, clone, delivery, answer string) {
	t.Helper()
	rep := readReport(t, filepath.Join(runDir(top, label), "report.json"))
	decisions, _ := rep["decisions"].([]any)
	if len(decisions) != 1 || decisions[0].(obj)["answer"] != answer ||
		!strings.Contains(decisions[0].(obj)["finding"].(string), "failed-check-finding") {
		t.Fatalf("answer not recorded with finding: %v", rep)
	}
	if answer == "stop" {
		if _, err := os.Stat(delivery); !os.IsNotExist(err) {
			t.Fatal("stop delivered")
		}
		if _, err := os.Stat(filepath.Join(clone, "a.txt")); err != nil {
			t.Fatal("stop lost work", err)
		}
	} else if data, err := os.ReadFile(delivery); err != nil || strings.TrimSpace(string(data)) != clone {
		t.Fatalf("answer did not deliver same clone: %q, %v", data, err)
	}
}

func TestRunAsksOnAFailedCheckAgain(t *testing.T) {
	builtATM(t)
	top := gitT(t, repo(t, "local", atmSet(atmYAML, "typecheck", "echo failed-check-finding; exit 1")),
		"rev-parse", "--show-toplevel")
	call := filepath.Join(t.TempDir(), "call")
	t.Setenv("FAKE_AGENT_CALL", call)
	t.Setenv("FAKE_AGENT_STREAM", `{"type":"system","subtype":"init","session_id":"sess-1"}`+"\n")
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	bin := followUpsServer(t, top)
	_, label := failedCheckQuestion(t, bin, top, "run", "--follow", "--harness", "claude", issueFile(t, issue))
	for _, answer := range []string{"fix", "stop"} {
		answerFailedCheck(t, bin, top, label, answer, 1)
	}
	rep := readReport(t, filepath.Join(runDir(top, label), "report.json"))
	decisions, _ := rep["decisions"].([]any)
	if len(decisions) != 2 || decisions[0].(obj)["answer"] != "fix" || decisions[1].(obj)["answer"] != "stop" {
		t.Fatalf("fix that fails should ask again: %v", rep)
	}
	got := agentCall(t, call)
	if !strings.Contains(strings.Join(got.Args, " "), "--resume sess-1") ||
		!strings.Contains(got.Stdin, "failed-check-finding") {
		t.Fatalf("fix did not resume with the finding: %+v", got)
	}
	logs, err := filepath.Glob(filepath.Join(runDir(top, label), "worker-fix-*.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("want exactly one fix turn, got %v: %v", logs, err)
	}
}

func TestRunAsksOnAFailedCheckInSingleUnit(t *testing.T) {
	builtATM(t)
	for _, row := range []string{"lint", "typecheck"} {
		t.Run(row, func(t *testing.T) {
			cfg := atmSet(atmYAML, row, "if test -f a_test.sh; then echo failed-check-finding; exit 1; fi")
			top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
			t.Setenv("FAKE_AGENT_WORK", fixWork)
			bin := followUpsServer(t, top)
			clone, label := failedCheckQuestion(t, bin, top, "run", "--follow", issueFile(t, issue))
			assertCheckWaiting(t, top, label)
			answerFailedCheck(t, bin, top, label, "stop", 1)
			rep := e2eReport(t, top, label)
			if len(rep.Units) != 1 || rep.Units[0].Unit != 1 || rep.FailedNode != "checks" ||
				!strings.Contains(rep.Reason, row+":") {
				t.Fatalf("single-unit check failure lost: %+v", rep)
			}
			if data, err := os.ReadFile(filepath.Join(clone, "a.txt")); err != nil || string(data) != "fixed\n" {
				t.Fatalf("stop lost single-unit work: %q, %v", data, err)
			}
		})
	}
}

func TestRunAsksOnAFailedCheckKeepsQuestionInHistory(t *testing.T) {
	builtATM(t)
	top := gitT(t, repo(t, "local", atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1")),
		"rev-parse", "--show-toplevel")
	bin := followUpsServer(t, top)
	_, label := failedCheckQuestion(t, bin, top, "run", "--follow", issueFile(t, issue))
	// Cheap failures fill the ordinary 200-run history while this question remains unanswered.
	for range 200 {
		cmd := exec.Command(bin, "run", "missing-issue.md")
		cmd.Dir = top
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("history run: %v %s", err, out)
		}
	}
	cmd := exec.Command(bin, "answer", label, "stop")
	cmd.Dir = top
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("history discarded the question: %v %s", err, out)
	}
}

func assertCheckWaiting(t *testing.T, top, label string) {
	t.Helper()
	rep := readReport(t, filepath.Join(runDir(top, label), "report.json"))
	decisions, _ := rep["decisions"].([]any)
	if rep["resumable"] != true || len(decisions) != 1 || decisions[0].(obj)["answer"] != "" {
		t.Fatalf("question published before its resumable report: %v", rep)
	}
}

func answerFailedCheck(t *testing.T, bin, top, label, answer string, code int) {
	t.Helper()
	cmd := exec.Command(bin, "answer", label, answer)
	cmd.Dir = top
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v %s", answer, err, out)
	}
	startFollow(t, bin, top, false, "attach", label).exit(code)
}

func TestRunAsksOnAFailedCheckResumesSavedWorker(t *testing.T) {
	builtATM(t)
	cfg := atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1") + "harness: codex\n"
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	call, order := filepath.Join(t.TempDir(), "call"), filepath.Join(t.TempDir(), "order")
	t.Setenv("FAKE_AGENT_CALL", call)
	t.Setenv("FAKE_AGENT_ORDER", order)
	t.Setenv("FAKE_AGENT_STREAM", `{"type":"system","subtype":"init","session_id":"saved-session"}`+"\n")
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	bin := followUpsServer(t, top)
	_, original := failedCheckQuestion(t, bin, top, "run", "--follow", "--harness", "claude", "--model", "saved-model",
		"--effort", "high", "--harness-arg=--verbose", "--env", "ATM_SAVED_WORKER=yes", issueFile(t, issue))
	stop := exec.Command(bin, "answer", original, "stop")
	stop.Dir = top
	if out, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("stop original: %v %s", err, out)
	}
	startFollow(t, bin, top, false, "attach", original).exit(1)
	_, label := failedCheckQuestion(t, bin, top, "run", "--follow", "--resume", original)
	for _, answer := range []string{"fix", "stop"} {
		answerFailedCheck(t, bin, top, label, answer, 1)
	}
	assertSavedWorkerInvocation(t, call, order)
	assertSavedWorkerResume(t, top, label)
}

func assertSavedWorkerInvocation(t *testing.T, call, order string) {
	t.Helper()
	got := agentCall(t, call)
	args := strings.Join(got.Args, " ")
	for _, want := range []string{"--resume saved-session", "--model saved-model", "--effort high", "--verbose"} {
		if !strings.Contains(args, want) {
			t.Fatalf("saved worker lost %q: %+v", want, got)
		}
	}
	if !slices.Contains(got.Env, "ATM_SAVED_WORKER=yes") || !strings.Contains(got.Stdin, "failed-check-finding") {
		t.Fatalf("saved worker lost environment or finding: %+v", got)
	}
	if calls := workerCalls(t, order); calls != 2 {
		t.Fatalf("want initial worker and one fix turn, got %d", calls)
	}
}

func assertSavedWorkerResume(t *testing.T, top, label string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir(top, label), "resume", "resume.json"))
	var saved resumePoint
	if err != nil || json.Unmarshal(data, &saved) != nil {
		t.Fatalf("saved worker: %s, %v", data, err)
	}
	if saved.Agent.Harness != "claude" || saved.Agent.Model != "saved-model" || saved.Agent.Effort != "high" ||
		saved.Session != "saved-session" || !slices.Equal(saved.Args, []string{"--verbose"}) ||
		!slices.Equal(saved.Env, []string{"ATM_SAVED_WORKER=yes"}) {
		t.Fatalf("fix saved a different worker: %+v", saved)
	}
	rep := e2eReport(t, top, label)
	if len(rep.Decisions) != 2 || rep.Decisions[0].Answer != "fix" || rep.Decisions[1].Answer != "stop" {
		t.Fatalf("saved worker decisions: %+v", rep.Decisions)
	}
}

func TestRunAsksOnAFailedCheckDuringBinaryReplacement(t *testing.T) {
	builtATM(t)
	for _, trigger := range []string{"connection", "tick"} {
		t.Run(trigger, func(t *testing.T) {
			delivery := filepath.Join(t.TempDir(), "delivered")
			cfg := atmSet(atmSet(atmYAML, "lint", "test -f repaired || { echo failed-check-finding; exit 1; }"),
				"delivery", "pwd > "+delivery)
			top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
			t.Setenv("FAKE_AGENT_WORK", "if test -f first-turn; then touch repaired; else "+fixWork+"; touch first-turn; fi")
			bin := followUpsServer(t, top)
			clone, label := failedCheckQuestion(t, bin, top, "run", "--follow", issueFile(t, issue))
			replacement := filepath.Join(t.TempDir(), "atm")
			buildATM(t, replacement, "follow-test")
			if err := os.Rename(replacement, bin); err != nil {
				t.Fatal(err)
			}
			if trigger == "tick" {
				time.Sleep(2 * tick)
			}
			cmd := exec.Command(bin, "answer", label, "fix")
			cmd.Dir = top
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("replacement lost pending question: %v %s", err, out)
			}
			startFollow(t, bin, top, false, "attach", label).exit(0)
			assertCheckAnswer(t, top, label, clone, delivery, "fix")
		})
	}
}
