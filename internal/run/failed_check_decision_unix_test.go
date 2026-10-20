//go:build unix

package run

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
			bin := decisionServer(t, top)
			f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
			clone, label := failedCheckQuestion(t, f, top)
			f.exit(1)
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

func failedCheckQuestion(t *testing.T, f *followed, top string) (string, string) {
	t.Helper()
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
	bin := decisionServer(t, top)
	f := startFollow(t, bin, top, false, "run", "--follow", "--harness", "claude", issueFile(t, issue))
	_, label := failedCheckQuestion(t, f, top)
	f.exit(1)
	for _, answer := range []string{"fix", "stop"} {
		cmd := exec.Command(bin, "answer", label, answer)
		cmd.Dir = top
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", answer, err, out)
		}
		end := startFollow(t, bin, top, false, "attach", label)
		end.exit(1)
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

// Exit serve gracefully so real-binary coverage counters reach GOCOVERDIR.
func decisionServer(t *testing.T, top string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "atm")
	buildATM(t, bin, "follow-test")
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := startVersionServe(t, bin, top)
	t.Cleanup(func() { endServe(bin, top, server) })
	return bin
}

func TestRunAsksOnAFailedCheckInChainedWork(t *testing.T) {
	builtATM(t)
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "later check", true: "scope check"}[blocked], func(t *testing.T) {
			cfg := atmSet(atmYAML, "lint", "if test -f c1_test.sh; then echo failed-check-finding; exit 1; fi")
			top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
			t.Setenv("FAKE_AGENT_WORK", scopeWork)
			t.Setenv("ATM_TEST_RED", "c1_test.sh")
			t.Setenv("ATM_TEST_FOLLOWUP", scopeFollowUp("c1_test.sh", "The retry error is reported.", `["a.txt"]`))
			t.Setenv("ATM_TEST_UNIT2", "echo u2 >> a.txt")
			if blocked {
				t.Setenv("FAKE_SCOPE_REPORT", `{"disposition":"blocked","paths":["a.txt","c1_test.sh"],"findings":[`+
					`{"path":"a.txt","clause":"The retry error is reported.","reason":"failed-check-finding"}],"summary":"s"}`)
			}
			bin := decisionServer(t, top)
			f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, followUpIssue))
			_, label := failedCheckQuestion(t, f, top)
			f.exit(1)
			cmd := exec.Command(bin, "answer", label, "stop")
			cmd.Dir = top
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("stop: %v %s", err, out)
			}
			startFollow(t, bin, top, false, "attach", label).exit(1)
		})
	}
}

func TestRunAsksOnAFailedCheckKeepsQuestionInHistory(t *testing.T) {
	builtATM(t)
	top := gitT(t, repo(t, "local", atmSet(atmYAML, "lint", "echo failed-check-finding; exit 1")),
		"rev-parse", "--show-toplevel")
	bin := decisionServer(t, top)
	f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
	_, label := failedCheckQuestion(t, f, top)
	f.exit(1)
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
