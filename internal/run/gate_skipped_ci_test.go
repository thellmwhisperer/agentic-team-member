package run

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFollowGateWithSkippedCI(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	for _, state := range []string{"MERGED", "CLOSED", "OPEN"} {
		t.Run(state, func(t *testing.T) {
			root := repo(t, "https://github.com/o/r.git", "")
			clone := t.TempDir()
			status := filepath.Join(clone, "status.txt")
			merged := filepath.Join(clone, "merged.txt")
			const skipped = "run:\n  status: completed\n  steps[1]{step,status,findings,duration_ms}:\n" +
				"    ci,skipped,0,0\noutcome: passed\n"
			writeFixture(t, status, skipped)
			writeFixture(t, merged, skipped+"pr_state: merged\n")
			count := fakeStatus(t)
			t.Setenv("ATM_STATUS_LIVE", status)
			for _, env := range []string{"ATM_STATUS_WAITING", "ATM_STATUS_MERGED"} {
				t.Setenv(env, merged)
			}
			fakeGH(t, state, false)
			r := newVerdict(t.TempDir(), io.Discard)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			done, err := followGate(ctx, r, root, clone, "feature", nil)
			var wantErr error
			wantCalls := "1"
			switch state {
			case "CLOSED":
				wantErr = errPRClosed
			case "OPEN":
				// Open must keep polling through the fake's second-call error until merge.
				// End on that response, not a timeout that can interrupt a counter write.
				wantCalls = "3"
			}
			if !done || !errors.Is(err, wantErr) {
				t.Fatalf("followGate = %v, %v; want true, %v", done, err, wantErr)
			}
			if b, err := os.ReadFile(count); err != nil || string(b) != wantCalls {
				t.Fatalf("status calls = %q, %v; want %s", b, err, wantCalls)
			}
			if n := r.node("ci"); n.Result != "skipped" {
				t.Fatalf("ci result = %q, want skipped", n.Result)
			}
			if _, err := os.Stat(status); err != nil {
				t.Fatalf("clone was removed: %v", err)
			}
		})
	}
}

func TestSkippedCIFallbackPreservesGateAuthority(t *testing.T) {
	for _, c := range []struct {
		name, status, ci, prState string
		done, failed              bool
	}{
		{name: "running gate", status: "running", ci: "skipped"},
		{name: "running CI", status: "completed", ci: "running"},
		{name: "completed CI", status: "completed", ci: "completed"},
		{name: "failed step", status: "completed", ci: "failed", failed: true},
		{name: "open state", status: "completed", ci: "skipped", prState: "pr_state: open\n"},
		{name: "unknown state", status: "completed", ci: "skipped", prState: "pr_state: unknown\n"},
		{name: "merged state", status: "completed", ci: "skipped", prState: "pr_state: merged\n", done: true},
		{name: "closed state", status: "completed", ci: "skipped", prState: "pr_state: closed\n", done: true,
			failed: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "https://github.com/o/r.git", "")
			clone := t.TempDir()
			status := filepath.Join(clone, "status.txt")
			writeFixture(t, status, "run:\n  status: "+c.status+"\n  steps[1]{step,status,findings,duration_ms}:\n"+
				"    ci,"+c.ci+",0,0\n"+c.prState)
			fakeStatus(t)
			t.Setenv("ATM_STATUS_LIVE", status)
			args := fakeGH(t, "MERGED", false)
			done, err := applyGate(context.Background(), newVerdict(t.TempDir(), io.Discard), root, clone, "feature")
			if done != c.done || (err != nil) != c.failed {
				t.Fatalf("applyGate = %v, %v; want done %v, failed %v", done, err, c.done, c.failed)
			}
			if _, err := os.Stat(args); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("gate authority was bypassed by a PR query: %v", err)
			}
		})
	}
}
