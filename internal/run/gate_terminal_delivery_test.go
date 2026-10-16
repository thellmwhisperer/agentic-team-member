package run

import (
	"bytes"
	"context"
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
		wantDeliveryFailure(t, observePoll(t, status), reason)
	})
	t.Run("terminal", func(t *testing.T) {
		wantDeliveryFailure(t, observeEnd(t, status), reason)
	})
	t.Run("command exits first", func(t *testing.T) {
		wantCommandThenGate(t, status, reason)
	})
	for _, step := range []string{"intent", "rebase"} {
		t.Run(step, func(t *testing.T) {
			failed := strings.Replace(status, "\n    "+step+",completed,", "\n    "+step+",failed,", 1)
			wantDeliveryFailure(t, observePoll(t, failed), reason)
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

// gateFailureFix is the shared no-mistakes and gh doubles: the first status read is first, the rest later.
func gateFailureFix(t *testing.T, atm, first, later string) (root, clone string) {
	t.Helper()
	root = repo(t, "https://github.com/o/r.git", atm)
	clone = t.TempDir()
	installCountedStatus(t, first, later)
	fakeGH(t, `{"url":"https://github.com/o/r/pull/424","state":"OPEN","commits":[]}`, false)
	return root, clone
}

func installCountedStatus(t *testing.T, first, later string) {
	t.Helper()
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "first.txt"), first)
	writeFixture(t, filepath.Join(dir, "later.txt"), later)
	count := filepath.Join(dir, "count")
	script := "#!/bin/sh\nn=0\n[ -f '" + count + "' ] && n=$(cat '" + count + "')\n" +
		"n=$((n+1))\nprintf '%s' \"$n\" > '" + count + "'\n" +
		"if [ \"$n\" -eq 1 ]; then cat '" + filepath.Join(dir, "first.txt") + "'; " +
		"else cat '" + filepath.Join(dir, "later.txt") + "'; fi\n"
	writeFixture(t, filepath.Join(dir, "no-mistakes"), script, 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func observePoll(t *testing.T, status string) []map[string]any {
	t.Helper()
	root, clone := gateFailureFix(t, atmYAML, status, status)
	var out bytes.Buffer
	s := observer{o: Outcome{Run: "t", Issue: "i"}, w: &out, now: func() time.Time { return time.Unix(0, 0).UTC() },
		delivering: true, clone: clone, ci: map[string]bool{}}
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
	s := observer{o: Outcome{Run: "t", Issue: "i"}, w: &out, now: time.Now, delivering: true}
	code, done := s.end(root, Outcome{Outcome: "failed", Ended: time.Now(), FailedNode: r.FailedNode,
		Reason: r.Reason, Run: "t", Issue: "i"})
	if code != 1 || !done {
		t.Fatalf("end = %d, %v", code, done)
	}
	return events(t, &out)
}

func wantDeliveryFailure(t *testing.T, evs []map[string]any, reason string) {
	t.Helper()
	if len(evs) == 0 {
		t.Fatal("no events")
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
