//go:build unix

package run

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRealScreenShowsTheWholePipelineFromTheStart(t *testing.T) {
	t.Run("delivery configured", func(t *testing.T) {
		nm := gateFake(t)
		writeFixture(t, filepath.Join(nm, "none"), "")
		a := liveAttach(t, 120, 50, "echo delivered")
		first := a.sees(func(s string) bool {
			return strings.Contains(s, "○ Delivery") && strings.Contains(s, "q detach")
		})
		assertPendingGatePipeline(t, first)
		a.send(`{"step":"agent","state":"started"}`)
		a.send(`{"step":"agent","says":"working-before-delivery"}`)
		working := a.sees(func(s string) bool { return strings.Contains(s, "working-before-delivery") })
		assertPendingGatePipeline(t, working)
		if _, err := os.Stat(filepath.Join(nm, "log")); !os.IsNotExist(err) {
			t.Fatalf("the screen asked no-mistakes before delivery: %v", err)
		}
		t.Logf("first screen:\n%s\nagent screen:\n%s", first, working)
		if err := os.Remove(filepath.Join(nm, "none")); err != nil {
			t.Fatal(err)
		}
		a.send(`{"step":"agent","state":"passed"}`)
		a.delivered(t.TempDir())
		gate := a.sees(func(s string) bool {
			return regexp.MustCompile(`(?m)^│ ⏸ review +waiting │`).MatchString(s) &&
				strings.Contains(s, "– lint") && !strings.Contains(s, "○ ci")
		})
		rows := regexp.MustCompile(`(?m)^│ \S+ review +\w+ │`).FindAllString(gate, -1)
		if len(rows) != 1 || strings.Contains(gate, "○ intent") || strings.Contains(gate, "○ ci") ||
			!regexp.MustCompile(`(?m)^│ ○ test +pending │`).MatchString(gate) ||
			!regexp.MustCompile(`(?m)^│ – lint +skipped │`).MatchString(gate) {
			t.Fatalf("the gate did not replace the pending rows with its own steps and states:\n%s", gate)
		}
		t.Logf("gate screen:\n%s", gate)
		writeFixture(t, filepath.Join(nm, "done"), "")
		a.exits()
	})
	t.Run("no delivery", func(t *testing.T) {
		a := liveAttach(t, 120, 50)
		a.send(`{"step":"agent","state":"started"}`)
		a.send(`{"step":"agent","says":"working-without-delivery"}`)
		s := a.sees(func(s string) bool { return strings.Contains(s, "working-without-delivery") })
		if regexp.MustCompile(`(?m)^│ \S+ (intent|rebase|review|test|document|lint|push|pr|ci) +\w+ │`).MatchString(s) {
			t.Fatalf("a run with no delivery shows gate rows:\n%s", s)
		}
		a.end()
	})
}

func assertPendingGatePipeline(t *testing.T, screen string) {
	t.Helper()
	rows := regexp.MustCompile(`(?m)^│ ○ (intent|rebase|review|test|document|lint|push|pr|ci) +pending │`).
		FindAllStringSubmatch(screen, -1)
	var got []string
	for _, row := range rows {
		got = append(got, row[1])
	}
	if strings.Join(got, " ") != "intent rebase review test document lint push pr ci" ||
		strings.Index(screen, "○ Delivery") > strings.Index(screen, "○ intent") {
		t.Fatalf("the screen lacks the whole pending gate pipeline below Delivery:\n%s", screen)
	}
}
