//go:build unix

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func TestFailedCheckReportsItsLabelInIdleOutcome(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	herdr := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\nprintf '%s|' \"$@\" >> \"$HERDR_LOG\"; echo >> \"$HERDR_LOG\"\n"
	if err := os.WriteFile(herdr, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "wX:p1")
	t.Setenv("HERDR_BIN_PATH", herdr)
	t.Setenv("HERDR_LOG", calls)
	h := openHerdr("issue-1", "startup")
	for i, ev := range unit[:13] {
		if i == 12 {
			ev = `{"step":"checks","check":"red/green","state":"failed"}`
		}
		h.frame(run.Frame{Event: []byte(ev)})
	}
	h.end(run.Outcome{Outcome: "failed", Ended: time.Now()}, nil)
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--state|idle|--message|#7 failed at Checks · red/green|") {
		t.Fatalf("herdr did not receive the failed check outcome: %q", b)
	}
}
