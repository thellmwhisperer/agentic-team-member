//go:build unix

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

// Exercise the public stream with the issue #433 capture through the real run
// server. Only external agent, no-mistakes and GitHub processes are doubled.
func TestFollowStreamCapturedFailureAfterCommandExit(t *testing.T) {
	failed, err := filepath.Abs(filepath.Join("testdata", "atm_watch_status_failed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, held := range []bool{false, true} {
		name := "alone"
		if held {
			name = "unrelated gate waiting"
		}
		t.Run(name, func(t *testing.T) {
			if held {
				other := followStream(t, atmSet(atmYAML, "delivery", "true"))
				other.delivered()
				other.gate("parked")
				other.advance(10 * time.Second)
				line := other.next("gate_waiting", map[string]any{"step": "review"})
				t.Logf("unrelated run waiting: %v", line)
				defer func() {
					select {
					case <-other.done:
						t.Error("unrelated held stream terminated")
					default:
						t.Log("unrelated stream remains waiting after terminal failure")
					}
				}()
			}
			root := repo(t, "https://github.com/o/r.git", atmSet(atmYAML, "delivery", ": no-mistakes; exit 1"))
			count := fakeStatus(t)
			writeFixture(t, count, "1") // first read fails, so the command ends first
			t.Setenv("ATM_STATUS_LIVE", failed)
			t.Setenv("ATM_STATUS_WAITING", failed)
			fakeGH(t, `{"url":"https://github.com/thellmwhisperer/agentic-team-member/pull/424",
				"state":"OPEN","commits":[
				{"oid":"abc123","messageHeadline":"no-mistakes(review): Repair finding"}]}`, false)
			t.Setenv("FAKE_GH_FAIL", "")
			background(t, root)
			o, err := startWithWatcher(t, root, []string{issueFile(t, issue)})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var out bytes.Buffer
			code := o.Stream(ctx, root, &out, time.Now, time.After)
			t.Logf("public Stream exit=%d\n%s", code, out.String())
			if code != 1 || ctx.Err() != nil {
				t.Fatalf("exit=%d context=%v", code, ctx.Err())
			}
			assertCapturedTerminalEvents(t, &out)
			end, err := Attach(root, o.Run, &bytes.Buffer{})
			if err != nil || end.FailedNode != "push" || end.Reason != terminalGateReason {
				t.Fatalf("terminal run result=%+v err=%v", end, err)
			}
			b, err := os.ReadFile(end.Report)
			if err != nil || !json.Valid(b) {
				t.Fatalf("report unavailable: %v", err)
			}
			t.Logf("persisted report:\n%s", b)
		})
	}
}

func assertCapturedTerminalEvents(t *testing.T, out io.Reader) {
	t.Helper()
	lines := events(t, out)
	if len(lines) < 3 {
		t.Fatalf("missing terminal observations: %v", lines)
	}
	for i, name := range []string{"pr_opened", "gate_fixing_ci", "run_failed"} {
		if lines[len(lines)-3+i]["event"] != name {
			t.Fatalf("terminal ordering: %v", lines)
		}
	}
	terminal := 0
	for _, line := range lines {
		if line["event"] == "run_failed" {
			terminal++
			if line["step"] != "delivery" || line["reason"] != terminalGateReason {
				t.Fatalf("terminal event: %v", line)
			}
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events=%d", terminal)
	}
}

func TestFollowStreamCapturedFailureAtPoll(t *testing.T) {
	s := followStream(t, atmSet(atmYAML, "delivery", "true"))
	s.delivered()
	s.gate("failed")
	s.pr("OPEN", "no-mistakes(review): Repair finding")
	s.advance(10 * time.Second)
	for _, name := range []string{"pr_opened", "gate_fixing_ci", "run_failed"} {
		want := map[string]any{}
		if name == "run_failed" {
			want = map[string]any{"step": "delivery", "reason": terminalGateReason}
		}
		line := s.next(name, want)
		b, _ := json.Marshal(line)
		t.Log(string(b))
	}
	s.ended(1)
	t.Log("public Stream exit=1; EOF after run_failed")
}
