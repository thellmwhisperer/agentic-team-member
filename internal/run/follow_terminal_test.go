package run

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The capture is unchanged output from no-mistakes v1.88.0 axi status --run
// 01M52E5MW1MDH3EFMBBFXT99JK on 16 October 2026 at 17:29 UTC.
const terminalGateReason = "step push failed: push to upstream: refusing to force-push " +
	"refs/heads/atm/gate-nodes-herdr-names-the-node-that-fai-20261016-132039: " +
	"remote head b46fd21ba38c carries 2 commit(s) the pipeline never incorporated " +
	"(e.g. b46fd21ba38c, 591820b7cb14); pushing would discard upstream work. " +
	"Re-fetch and rebase onto the current remote, or push manually if this overwrite is intended."

func TestAcceptanceOperatorGateTerminalDeliveryStep(t *testing.T) {
	for _, held := range []bool{false, true} {
		name := "alone"
		if held {
			name = "with unrelated held run"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root := t.TempDir()
			gitT(t, root, "init", "-q")
			gitT(t, root, "remote", "add", "origin", "https://github.com/o/r.git")
			if held {
				assertUnrelatedHeldGate(t, ctx, root)
			}
			fakeStatus(t)
			failed, err := filepath.Abs(filepath.Join("testdata", "atm_watch_status_failed.txt"))
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("ATM_STATUS_LIVE", failed)
			fakeGH(t, `{"state":"OPEN","commits":[
				{"oid":"abc123","messageHeadline":"no-mistakes(review): Repair finding"}]}`, false)
			var out bytes.Buffer
			s := observer{o: Outcome{Run: "failed", Issue: "433"}, w: &out, now: time.Now,
				clone: root, delivering: true, ci: map[string]bool{}}
			if code, done := s.poll(ctx, root); code != 1 || !done {
				t.Fatalf("terminal poll: code %d, done %t; output %s", code, done, out.String())
			}
			assertTerminalDeliveryEvents(t, &out, terminalGateReason)
		})
	}
}

func TestFollowTerminalGateResult(t *testing.T) {
	for _, statusFails := range []bool{false, true} {
		name := "readable gate"
		if statusFails {
			name = "unreadable gate"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root := t.TempDir()
			gitT(t, root, "init", "-q")
			gitT(t, root, "remote", "add", "origin", "https://github.com/o/r.git")
			count := fakeStatus(t)
			failed, err := filepath.Abs(filepath.Join("testdata", "atm_watch_status_failed.txt"))
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("ATM_STATUS_LIVE", failed)
			fakeGH(t, `{"url":"https://github.com/thellmwhisperer/agentic-team-member/pull/424",
				"state":"OPEN","commits":[
				{"oid":"abc123","messageHeadline":"no-mistakes(review): Repair finding"}]}`, false)
			var gateOut bytes.Buffer
			r := newVerdict(t.TempDir(), &gateOut)
			_, err = applyGate(ctx, r, root, root, "feature")
			if err == nil || err.Error() != terminalGateReason || r.Reason != terminalGateReason || r.FailedNode != "push" {
				t.Fatalf("gate outcome = %s, %q, %v", r.FailedNode, r.Reason, err)
			}
			assertFailedGateEvent(t, &gateOut)
			if !statusFails {
				writeFixture(t, count, "0")
			}
			var out bytes.Buffer
			s := observer{o: Outcome{Run: "failed", Issue: "433"}, w: &out, now: time.Now,
				clone: root, branch: "feature", delivering: true, ci: map[string]bool{}}
			code, done := s.end(ctx, root, Outcome{Outcome: "failed", Ended: time.Now(),
				FailedNode: r.FailedNode, Reason: err.Error()})
			if code != 1 || !done {
				t.Fatalf("terminal outcome: code %d, done %t; output %s", code, done, out.String())
			}
			assertTerminalDeliveryEvents(t, &out, terminalGateReason)
		})
	}
}

func assertFailedGateEvent(t *testing.T, gateOut io.Reader) {
	t.Helper()
	failedEvents := 0
	for _, ev := range events(t, gateOut) {
		if ev["state"] == "failed" {
			failedEvents++
			if ev["step"] != "push" || ev["error"] != terminalGateReason {
				t.Fatalf("failed gate event = %v", ev)
			}
		}
	}
	if failedEvents != 1 {
		t.Fatalf("failed gate events = %d, want 1", failedEvents)
	}
}

func TestFollowTerminalCommandExitsBeforeGateFailure(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Hour
	failed, err := filepath.Abs(filepath.Join("testdata", "atm_watch_status_failed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	root := repo(t, "https://github.com/o/r.git", atmSet(atmYAML, "delivery", ": no-mistakes; exit 1"))
	count := fakeStatus(t)
	writeFixture(t, count, "1")
	t.Setenv("ATM_STATUS_LIVE", failed)
	t.Setenv("ATM_STATUS_WAITING", failed)
	fakeGH(t, "OPEN", false)
	r := &bgRun{Outcome: Outcome{Run: "failed", Issue: "433", Outcome: "running",
		Report: filepath.Join(runDir(root, "failed"), "report.json")}, more: make(chan struct{})}
	if err := os.MkdirAll(filepath.Dir(r.Report), 0o755); err != nil {
		t.Fatal(err)
	}
	server := &server{root: root, runs: []*bgRun{r}}
	args := []string{issueFile(t, issue)}
	done := make(chan struct{})
	go func() { server.run(r, args); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("delivery did not terminate after the command and gate failed")
	}
	if r.Outcome.Outcome != "failed" || r.FailedNode != "push" || r.Reason != terminalGateReason {
		t.Fatalf("terminal run outcome = %+v", r.Outcome)
	}
	assertCommandAndGateFailure(t, r)
	assertCommandFailureStream(t, root, count, r)
}

func assertCommandFailureStream(t *testing.T, root, count string, r *bgRun) {
	t.Helper()
	fakeGH(t, `{"url":"https://github.com/thellmwhisperer/agentic-team-member/pull/424",
		"state":"OPEN","commits":[
		{"oid":"abc123","messageHeadline":"no-mistakes(review): Repair finding"}]}`, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, counter := range []string{"0", "1"} {
		writeFixture(t, count, counter)
		var out bytes.Buffer
		s := observer{o: r.Outcome, w: &out, now: time.Now, ci: map[string]bool{}}
		for _, f := range r.frames {
			s.frame(f)
		}
		out.Reset()
		if code, ended := s.end(ctx, root, r.Outcome); code != 1 || !ended {
			t.Fatalf("terminal outcome: code %d, ended %t; output %s", code, ended, out.String())
		}
		assertTerminalDeliveryEvents(t, &out, terminalGateReason)
	}
	writeFixture(t, count, "0")
	var out bytes.Buffer
	s := observer{o: r.Outcome, w: &out, now: time.Now, clone: root, delivering: true,
		reason: "delivery: exit status 1", ci: map[string]bool{}}
	if code, ended := s.poll(ctx, root); code != 1 || !ended {
		t.Fatalf("terminal poll: code %d, ended %t; output %s", code, ended, out.String())
	}
	assertTerminalDeliveryEvents(t, &out, terminalGateReason)
}

func assertCommandAndGateFailure(t *testing.T, r *bgRun) {
	t.Helper()
	report := readReport(t, r.Report)
	if report["failed_node"] != "push" || report["reason"] != terminalGateReason {
		t.Fatalf("terminal report = %v", report)
	}
	commandFailed, gateFailed := false, false
	for _, f := range r.frames {
		var ev struct{ Step, State, Error string }
		if json.Unmarshal(f.Event, &ev) != nil {
			continue
		}
		if ev.Step == "delivery" && ev.Error == "delivery: exit status 1" {
			commandFailed = true
		}
		if ev.Step == "push" && ev.State == "failed" {
			if !commandFailed || ev.Error != terminalGateReason {
				t.Fatalf("gate failure preceded command diagnostic or lost its reason: %+v", ev)
			}
			gateFailed = true
		}
	}
	if !commandFailed || !gateFailed {
		t.Fatalf("missing command or gate failure: command=%t, gate=%t", commandFailed, gateFailed)
	}
}

func assertTerminalDeliveryEvents(t *testing.T, out io.Reader, reason string) {
	t.Helper()
	dec := json.NewDecoder(out)
	for _, event := range []string{"pr_opened", "gate_fixing_ci", "run_failed"} {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			t.Fatal(err)
		}
		if line["event"] != event || line["run"] != "failed" || line["issue"] != "433" {
			t.Fatalf("want %s for failed run: %v", event, line)
		}
		if event == "run_failed" {
			if line["step"] != "delivery" {
				t.Errorf("step = %v, want delivery", line["step"])
			}
			if line["reason"] != reason {
				t.Errorf("reason = %v, want %s", line["reason"], reason)
			}
		}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("output after run_failed: %v, %v", extra, err)
	}
}

func assertUnrelatedHeldGate(t *testing.T, ctx context.Context, root string) {
	t.Helper()
	fakeStatus(t)
	parked, err := filepath.Abs(filepath.Join("testdata", "atm_watch_status_parked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATM_STATUS_LIVE", parked)
	var out bytes.Buffer
	other := observer{o: Outcome{Run: "unrelated"}, w: &out, now: time.Now,
		clone: t.TempDir(), delivering: true}
	if code, done := other.poll(ctx, root); code != 0 || done || other.waiting != "review" {
		t.Fatalf("unrelated run: code %d, done %t, waiting %q", code, done, other.waiting)
	}
}
