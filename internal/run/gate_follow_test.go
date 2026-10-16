package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A no-mistakes hand-off is not the end of the run: atm run stays up until the PR merges, and report.json
// plus the screen events carry the gate's own steps, states and durations.
func TestRunStaysAliveUntilTheGateMergesAndReportsItsNodes(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	release := filepath.Join(t.TempDir(), "release")
	realStatus(t, release)
	root := repo(t, "https://github.com/o/r.git", atmSet(atmYAML, "delivery", "no-mistakes axi run"))
	arg := issueFile(t, issue)
	report := filepath.Join(root, ".atm", "runs", "t", "report.json")
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Run("t", []string{arg}, &out, &bytes.Buffer{}) }()
	waitForGateNode(t, done, report, release)
	checkMergedGate(t, report, &out)
}

func TestFakeNoMistakesKeepsReadFailureExclusiveToStatus(t *testing.T) {
	count := realStatus(t, filepath.Join(t.TempDir(), "release"))
	clone := t.TempDir()
	if _, err := gateStatus(context.Background(), clone); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("no-mistakes", "axi", "run").CombinedOutput(); err != nil {
		t.Fatalf("delivery consumed the status read failure: %v, %s", err, out)
	}
	if _, err := gateStatus(context.Background(), clone); err == nil {
		t.Fatal("second status read lost its injected failure")
	}
	if out, err := exec.Command("no-mistakes", "axi", "run").CombinedOutput(); err != nil {
		t.Fatalf("delivery failed after the status read failure: %v, %s", err, out)
	}
	if b, err := os.ReadFile(count); err != nil || string(b) != "2" {
		t.Fatalf("two status reads counted as %q: %v", b, err)
	}
}

func waitForGateNode(t *testing.T, done <-chan error, report, release string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("run ended at the hand-off (%v); want it alive until the PR merges", err)
		case <-tick.C:
			b, err := os.ReadFile(report)
			if err == nil && bytes.Contains(b, []byte(`"name": "review"`)) {
				writeFixture(t, release, "")
				waitMerged(t, ctx, done)
				return
			}
		case <-ctx.Done():
			t.Fatal("run ended at the hand-off: report never got the gate's nodes")
		}
	}
}

func waitMerged(t *testing.T, ctx context.Context, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("run did not finish after the PR merged")
	}
}

func checkMergedGate(t *testing.T, report string, out *bytes.Buffer) {
	t.Helper()
	rep := readReport(t, report)
	if reportNode(t, rep, "delivery")["result"] != "passed" {
		t.Fatalf("delivery = %v, want passed at the hand-off", reportNode(t, rep, "delivery")["result"])
	}
	want := map[string]float64{
		"review": 129822, "test": 1384178, "document": 31889, "lint": 212998,
		"push": 7226, "pr": 16070, "ci": 1310737,
	}
	for name, ms := range want {
		n := reportNode(t, rep, name)
		if n["result"] != "passed" || n["duration_ms"] != ms {
			t.Fatalf("node %s = %v, want passed with duration %v from no-mistakes", name, n, ms)
		}
	}
	if !gateEvent(t, out, "review", "passed", 129822) || !gateEvent(t, out, "ci", "passed", 1310737) {
		t.Fatal("screen events lack the gate nodes' states and durations")
	}
}

func gateEvent(t *testing.T, out *bytes.Buffer, step, state string, ms float64) bool {
	t.Helper()
	for _, ev := range events(t, bytes.NewReader(out.Bytes())) {
		if ev["step"] == step && ev["state"] == state && ev["duration_ms"] == ms {
			return true
		}
	}
	return false
}

func TestDeliveryObservesGateBeforeCommandExitAndKeepsFollowingAfterFailure(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	for _, exit := range []int{0, 1} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			dir := t.TempDir()
			commandRelease, gateRelease := filepath.Join(dir, "command"), filepath.Join(dir, "gate")
			realStatus(t, gateRelease)
			t.Setenv("ATM_COMMAND_RELEASE", commandRelease)
			line := fmt.Sprintf(`: no-mistakes; while [ ! -f "$ATM_COMMAND_RELEASE" ]; do sleep 0.01; done; exit %d`, exit)
			root := repo(t, "https://github.com/o/r.git", atmSet(atmYAML, "delivery", line))
			arg := issueFile(t, issue)
			path := filepath.Join(root, ".atm", "runs", "t", "report.json")
			var out bytes.Buffer
			done := make(chan error, 1)
			go func() { done <- Run("t", []string{arg}, &out, io.Discard) }()
			waitForDeliveryAndReview(t, done, path, "running", "waiting")
			writeFixture(t, commandRelease, "")
			want := "passed"
			if exit != 0 {
				want = "failed"
			}
			waitForDeliveryAndReview(t, done, path, want, "waiting")
			writeFixture(t, gateRelease, "")
			select {
			case err := <-done:
				if (err != nil) != (exit != 0) {
					t.Fatalf("command exit %d: run error %v", exit, err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("run did not end after merge")
			}
			if n := reportNode(t, readReport(t, path), "ci"); n["result"] != "passed" {
				t.Fatalf("final CI result = %v", n)
			}
			if !gateEvent(t, &out, "review", "waiting", 4697267) || !gateEvent(t, &out, "ci", "passed", 1310737) {
				t.Fatal("screen missed the gate wait or completion")
			}
		})
	}
}

func waitForDeliveryAndReview(t *testing.T, done <-chan error, path, delivery, review string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("run ended before merge: %v", err)
		case <-deadline:
			t.Fatalf("report never showed delivery %s and review %s", delivery, review)
		case <-tick.C:
			var report verdict
			b, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(b, &report) != nil {
				continue
			}
			states := map[string]string{}
			for _, n := range report.Nodes {
				states[n.Name] = n.Result
			}
			if states["delivery"] == delivery && states["review"] == review {
				return
			}
		}
	}
}

// A delivery that failed before the gate showed a step has no gate to follow: an unreadable status, here a
// released clone whose branch has an open PR, ends the follow instead of polling it forever.
func TestFailedDeliveryWithoutGateStepsEndsOnAnUnreadableStatus(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	root := repo(t, "https://github.com/o/r.git", "")
	fakeGH(t, "OPEN", false)
	r := newVerdict(t.TempDir(), io.Discard)
	r.err = errors.New("delivery failed")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if done, err := followGate(ctx, r, root, filepath.Join(root, "missing"), "feature", nil); !done || err != nil {
		t.Fatalf("follow = %v, %v; want it ended, the run keeping the delivery's own failure", done, err)
	}
}

// Ctrl-C while the delivery command runs fails the delivery, as before the gate was followed: a cancelled
// follow never counts as a finished gate, whichever of the two the select picks.
func TestCancelledFollowIsNoFinishedGate(t *testing.T) {
	root := repo(t, "https://github.com/o/r.git", "")
	fakeGH(t, "OPEN", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newVerdict(t.TempDir(), io.Discard)
	done, err := followGate(ctx, r, root, filepath.Join(root, "missing"), "feature", make(chan error))
	if done || !errors.Is(err, context.Canceled) {
		t.Fatalf("follow = %v, %v; want an unfinished gate and the cancellation", done, err)
	}
}

func TestClosedPRPreservesGateResultsAndEndsStreamAsClosed(t *testing.T) {
	for _, source := range []string{"gate", "released clone"} {
		t.Run(source, func(t *testing.T) {
			completed := recorded(t, "atm_watch_status_completed.txt")
			root := repo(t, "https://github.com/o/r.git", "")
			r := newVerdict(t.TempDir(), io.Discard)
			r.noteGate(parseGate(completed))
			clone := filepath.Join(root, "missing")
			if source == "gate" {
				clone = t.TempDir()
				status := filepath.Join(t.TempDir(), "closed.txt")
				writeFixture(t, status, strings.ReplaceAll(completed, "pr_state: merged", "pr_state: closed"))
				fakeStatus(t)
				t.Setenv("ATM_STATUS_LIVE", status)
			} else {
				fakeGH(t, "CLOSED", false)
			}
			done, err := applyGate(context.Background(), r, root, clone, "feature")
			if !done || err == nil || err.Error() != "PR closed" {
				t.Fatalf("closure = %v, %v", done, err)
			}
			if err := r.write(); err != nil {
				t.Fatal(err)
			}
			report := readReport(t, r.path)
			if n := reportNode(t, report, "pr"); n["result"] != "passed" ||
				n["duration_ms"] != float64(16070) || r.FailedNode != "" {
				t.Fatalf("closure changed the gate result: %v, failed node %q", n, r.FailedNode)
			}
			var out bytes.Buffer
			s := observer{w: &out, now: time.Now, pr: recordedPR}
			code, ended := s.end(context.Background(), root, Outcome{
				Outcome: "failed", Ended: time.Now(), FailedNode: r.FailedNode, Reason: err.Error(),
			})
			events := events(t, &out)
			if !ended || code != 1 || len(events) != 1 || events[0]["event"] != "closed" {
				t.Fatalf("stream closure = %d, %v, %v", code, ended, events)
			}
		})
	}
}

func TestRecordedGateSnapshotsOmitUnknownStarts(t *testing.T) {
	for _, snapshot := range []string{"running", "parked", "completed"} {
		t.Run(snapshot, func(t *testing.T) {
			r := newVerdict(t.TempDir(), io.Discard)
			g := parseGate(recorded(t, "atm_watch_status_"+snapshot+".txt"))
			if g.err != nil {
				t.Fatal(g.err)
			}
			r.noteGate(g)
			if _, err := g.outcome(r); err != nil {
				t.Fatal(err)
			}
			if err := r.write(); err != nil {
				t.Fatal(err)
			}
			report := readReport(t, r.path)
			for _, name := range []string{"review", "test", "document", "lint", "push", "pr", "ci"} {
				n := reportNode(t, report, name)
				if got, ok := n["started_at"]; ok {
					t.Errorf("node %s invented started_at %v from %s capture", name, got, snapshot)
				}
			}
		})
	}
}
