package run

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
			failedEvents := 0
			for _, ev := range events(t, &gateOut) {
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
