package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// A pane whose root process is atm attach <run> exits with the run.
func TestCompletedScreenQuitsWhenTheRunEnds(t *testing.T) {
	for _, outcome := range []string{"passed", "failed"} {
		m := New("done-1", 80, 40)
		_, cmd := m.Update(ended{run.Outcome{Outcome: outcome, Ended: time.Now()}, nil})
		if cmd == nil {
			t.Fatalf("%s: the screen stayed open after the run ended", outcome)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok || m.detached || m.outcome.Outcome != outcome {
			t.Fatalf("%s: the run's end did not quit with its outcome: detached=%v %+v", outcome, m.detached, m.outcome)
		}
	}
}
