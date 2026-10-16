package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A terminal gate failure is run_failed at delivery, with the capture's error line, then nothing.
func TestFollowStreamGateTerminalDeliveryStep(t *testing.T) {
	status := readGateFailure(t)
	reason := gateErrorLine(t, status)
	t.Run("poll", func(t *testing.T) {
		wantDeliveryFailure(t, observePoll(t, status, ""), reason)
	})
	t.Run("command exits before observer read", func(t *testing.T) {
		wantDeliveryFailure(t, observePoll(t, status, "delivery: exit status 1"), reason)
	})
	t.Run("terminal", func(t *testing.T) {
		wantDeliveryFailure(t, observeEnd(t, status), reason)
	})
	t.Run("command exits first", func(t *testing.T) {
		wantCommandThenGate(t, status, reason)
	})
	for _, step := range []string{"intent", "rebase"} {
		t.Run(step, func(t *testing.T) {
			failed := strings.Replace(status, "\n"+step+",completed,", "\n"+step+",failed,", 1)
			wantDeliveryFailure(t, observePoll(t, failed, ""), reason)
			wantDeliveryFailure(t, observeEnd(t, failed), reason)
		})
	}
}

func readGateFailure(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gate_push_failed_status.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gateErrorLine(t *testing.T, status string) string {
	t.Helper()
	for _, line := range strings.Split(status, "\n") {
		rest, ok := strings.CutPrefix(line, "error: ")
		if !ok {
			continue
		}
		text, err := strconv.Unquote(rest)
		if err != nil {
			t.Fatalf("error line %q: %v", rest, err)
		}
		return text
	}
	t.Fatal("capture has no error line")
	return ""
}

func gateFailureFix(t *testing.T, atm, first, later string) (root, clone string) {
	t.Helper()
	root = repo(t, "https://github.com/o/r.git", atm)
	clone = t.TempDir()
	count := fakeStatus(t)
	writeFixture(t, filepath.Join(clone, "first.txt"), first)
	writeFixture(t, filepath.Join(clone, "later.txt"), later)
	t.Setenv("ATM_STATUS_LIVE", filepath.Join(clone, "first.txt"))
	for _, env := range []string{"ATM_STATUS_WAITING", "ATM_STATUS_MERGED"} {
		t.Setenv(env, filepath.Join(clone, "later.txt"))
	}
	if first == later {
		writeFixture(t, count, "2")
	}
	fakeGH(t, `{"url":"https://github.com/o/r/pull/424","state":"OPEN","commits":[
		{"oid":"b46fd21ba38c","messageHeadline":"no-mistakes(ci): Repair CI"}]}`, false)
	return root, clone
}

func observePoll(t *testing.T, status, commandReason string) []map[string]any {
	t.Helper()
	root, clone := gateFailureFix(t, atmYAML, status, status)
	var out bytes.Buffer
	s := observer{o: Outcome{Run: "t", Issue: "i"}, w: &out, now: func() time.Time { return time.Unix(0, 0).UTC() },
		delivering: true, clone: clone, ci: map[string]bool{}}
	if commandReason != "" {
		code, done := s.end(context.Background(), root, Outcome{Run: "t", Outcome: "failed",
			Ended: time.Now(), FailedNode: "delivery", Reason: commandReason})
		if done || code != 0 || out.Len() != 0 {
			t.Fatalf("command failure ended observation: %d, %v, %s", code, done, &out)
		}
	}
	code, done := s.poll(context.Background(), root)
	if code != 1 || !done {
		t.Fatalf("poll = %d, %v", code, done)
	}
	return events(t, &out)
}

func observeEnd(t *testing.T, status string) []map[string]any {
	t.Helper()
	root, clone := gateFailureFix(t, atmYAML, status, status)
	r := newVerdict(t.TempDir(), io.Discard)
	if _, err := applyGate(context.Background(), r, root, clone, "branch"); err == nil || r.FailedNode == "" ||
		r.FailedNode == "delivery" {
		t.Fatalf("applyGate err=%v node=%q reason=%q", err, r.FailedNode, r.Reason)
	}
	var out bytes.Buffer
	s := observer{o: Outcome{Run: "t", Issue: "i"}, w: &out, now: time.Now, delivering: true,
		clone: clone, ci: map[string]bool{}}
	code, done := s.end(context.Background(), root, Outcome{Outcome: "failed", Ended: time.Now(),
		FailedNode: r.FailedNode, Reason: r.Reason, Run: "t", Issue: "i"})
	if code != 1 || !done {
		t.Fatalf("end = %d, %v", code, done)
	}
	return events(t, &out)
}

func wantDeliveryFailure(t *testing.T, evs []map[string]any, reason string) {
	t.Helper()
	if len(evs) != 3 || evs[0]["event"] != "pr_opened" || evs[1]["event"] != "gate_fixing_ci" {
		t.Fatalf("want PR, repair commit, then failure; got %v", evs)
	}
	commit, _ := evs[1]["commit"].(map[string]any)
	if commit["sha"] != "b46fd21ba38c" || commit["subject"] != "no-mistakes(ci): Repair CI" {
		t.Fatalf("repair commit = %v", commit)
	}
	last := evs[len(evs)-1]
	if last["event"] != "run_failed" || last["step"] != "delivery" || last["reason"] != reason {
		t.Fatalf("last event %v, want run_failed step delivery reason %q", last, reason)
	}
	for _, ev := range evs[:len(evs)-1] {
		if ev["event"] == "run_failed" || ev["event"] == "merged" || ev["event"] == "closed" {
			t.Fatalf("event %v before the terminal failure", ev)
		}
	}
}

const runningGate = "steps[1]{step,status,findings,duration_ms}:\n  review,completed,0,1\n"

func wantCommandThenGate(t *testing.T, status, reason string) {
	t.Helper()
	line := ": no-mistakes; exit 1"
	root, _ := gateFailureFix(t, atmSet(atmYAML, "delivery", line), runningGate, status)
	err := Run("t", []string{issueFile(t, issue)}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("run passed")
	}
	first, _, _ := strings.Cut(err.Error(), "\n")
	if first != reason || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("run error %q, want the gate reason then the command error", err)
	}
	report := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
	if report["failed_node"] != "push" || report["reason"] != reason {
		t.Fatalf("report node=%v reason=%v", report["failed_node"], report["reason"])
	}
	if reportNode(t, report, "delivery")["result"] != "failed" {
		t.Fatal("delivery command error was erased")
	}
}

func TestFollowStreamTerminalGateFailureSurvivesLaterReads(t *testing.T) {
	status := readGateFailure(t)
	reason := gateErrorLine(t, status)
	for _, tc := range []struct {
		name, status, prState string
		ghFails               bool
	}{
		{"stale gate", runningGate, "OPEN", false},
		{"unreadable gate", "", "OPEN", false},
		{"merged PR", status, "MERGED", false},
		{"closed PR", status, "CLOSED", false},
		{"unreadable gate and PR", "", "OPEN", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, clone := gateFailureFix(t, atmYAML, tc.status, tc.status)
			t.Setenv("FAKE_GH_OUT", strings.Replace(os.Getenv("FAKE_GH_OUT"), "OPEN", tc.prState, 1))
			if tc.ghFails {
				t.Setenv("FAKE_GH_FAIL", "1")
			}
			var out bytes.Buffer
			s := observer{o: Outcome{Run: "t", Issue: "i"}, w: &out, now: time.Now,
				clone: clone, branch: "branch", delivering: true, ci: map[string]bool{}}
			code, done := s.end(context.Background(), root, Outcome{Outcome: "failed", Ended: time.Now(),
				FailedNode: "push", Reason: reason})
			if code != 1 || !done {
				t.Fatalf("end = %d, %v", code, done)
			}
			evs := events(t, &out)
			if tc.ghFails {
				if len(evs) != 1 || evs[0]["event"] != "run_failed" ||
					evs[0]["step"] != "delivery" || evs[0]["reason"] != reason {
					t.Fatalf("known failure lost: %v", evs)
				}
				return
			}
			wantDeliveryFailure(t, evs, reason)
		})
	}
}

func TestFollowStreamParkedGateDoesNotDiscoverPR(t *testing.T) {
	status := recorded(t, "atm_watch_status_parked.txt")
	root, clone := gateFailureFix(t, atmYAML, status, status)
	var out bytes.Buffer
	s := observer{o: Outcome{Run: "t", Issue: "i"}, w: &out, now: time.Now,
		clone: clone, branch: "branch", delivering: true, ci: map[string]bool{}}
	for range 2 {
		if code, done := s.poll(context.Background(), root); code != 0 || done {
			t.Fatalf("parked gate ended observation: %d, %v", code, done)
		}
	}
	if _, err := os.Stat(os.Getenv("FAKE_GH_ARGS")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parked gate triggered a PR lookup: %v", err)
	}
	evs := events(t, &out)
	if len(evs) != 1 || evs[0]["event"] != "gate_waiting" || evs[0]["step"] != "review" {
		t.Fatalf("want one review wait: %v", evs)
	}
}
