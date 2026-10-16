package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
