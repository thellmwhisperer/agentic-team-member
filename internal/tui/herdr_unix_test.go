//go:build unix

package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func TestFailedCheckReportsItsLabelInIdleOutcome(t *testing.T) {
	calls, h := newHerdrRecorder(t)
	for i, ev := range unit[:13] {
		if i == 12 {
			ev = `{"step":"checks","check":"red/green","state":"failed"}`
		}
		h.frame(run.Frame{Event: []byte(ev)})
	}
	if got := finishHerdr(t, h, calls); !strings.Contains(got, "--state|idle|--message|#7 failed at Checks · red/green|") {
		t.Fatalf("herdr did not receive the failed check outcome: %q", got)
	}
}

func TestHerdrBlocksWhileAGateStepWaits(t *testing.T) {
	calls, h := newHerdrRecorder(t)
	for _, ev := range []string{
		`{"step":"issue","state":"started"}`,
		`{"step":"issue","state":"passed","title":"Retry","number":7}`,
		`{"step":"delivery","state":"passed"}`,
		`{"step":"test","state":"running","duration_ms":843130}`,
		`{"step":"test","state":"passed","duration_ms":843130}`,
		`{"step":"review","state":"waiting","duration_ms":4697267}`,
	} {
		h.frame(run.Frame{Event: []byte(ev)})
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "--state|working|--message|#7 Test|") ||
		!strings.Contains(got, "--state|blocked|--message|#7 Review|") {
		t.Fatalf("herdr status was not working while a node ran and blocked while the gate waited: %q", got)
	}
}

func TestGateActivityOverridesRunningDelivery(t *testing.T) {
	for _, gate := range []struct{ step, label string }{
		{"review", "Review"}, {"test", "Test"}, {"document", "Document"}, {"lint", "Lint"},
		{"push", "Push"}, {"pr", "PR"}, {"ci", "CI"},
	} {
		t.Run(gate.step, func(t *testing.T) {
			calls, h := newHerdrRecorder(t)
			h.m.width, h.m.height = 80, 40
			h.frame(run.Frame{Event: []byte(`{"step":"issue","state":"passed","number":7}`)})
			h.frame(run.Frame{Event: []byte(`{"step":"delivery","state":"started"}`)})
			for _, state := range []string{"waiting", "running", "waiting"} {
				h.frame(run.Frame{Event: []byte(fmt.Sprintf(`{"step":%q,"state":%q}`, gate.step, state))})
				want, icon := "blocked", "⏸ waiting"
				if state == "running" {
					want, icon = "working", "running"
				}
				b, err := os.ReadFile(calls)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSpace(string(b)), "\n")
				if !strings.Contains(lines[len(lines)-1], "--state|"+want+"|--message|#7 "+gate.label+"|") {
					t.Fatalf("gate %s hidden by delivery: %s", state, b)
				}
				if h.m.state != state || !strings.Contains(view(h.m), icon) || h.m.current("delivery").state != "running" {
					t.Fatalf("gate %s changed delivery or lost its screen state: %s", state, view(h.m))
				}
			}
			h.frame(run.Frame{Event: []byte(`{"step":"delivery","state":"passed"}`)})
			if h.m.state != "waiting" || h.m.step() != gate.label {
				t.Fatal("delivery completion replaced the waiting gate")
			}
		})
	}
}

func TestRecoveredSlopCheckDoesNotReplaceLaterDeliveryFailure(t *testing.T) {
	calls, h := newHerdrRecorder(t)
	for _, ev := range []string{
		`{"step":"issue","state":"started"}`,
		`{"step":"issue","state":"passed","title":"Retry","number":7}`,
		`{"step":"ponytail","state":"started"}`,
		`{"step":"ponytail","check":"package proof","state":"failed"}`,
		`{"step":"ponytail","state":"passed"}`,
		`{"step":"delivery","state":"started"}`,
		`{"step":"delivery","state":"failed"}`,
	} {
		h.frame(run.Frame{Event: []byte(ev)})
	}
	if got := finishHerdr(t, h, calls); !strings.Contains(got, "--state|idle|--message|#7 failed at Delivery|") {
		t.Fatalf("herdr retained a recovered check as the failed step: %q", got)
	}
}

func newHerdrRecorder(t *testing.T) (string, *herdr) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	herdrPath := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\nprintf '%s|' \"$@\" >> \"$HERDR_LOG\"; echo >> \"$HERDR_LOG\"\n"
	if err := os.WriteFile(herdrPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "wX:p1")
	t.Setenv("HERDR_BIN_PATH", herdrPath)
	t.Setenv("HERDR_LOG", calls)
	return calls, openHerdr("issue-1", "startup")
}

func finishHerdr(t *testing.T, h *herdr, calls string) string {
	t.Helper()
	h.end(run.Outcome{Outcome: "failed", Ended: time.Now()}, nil)
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
