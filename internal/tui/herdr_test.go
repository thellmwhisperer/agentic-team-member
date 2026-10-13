package tui

import (
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func TestStepIsTheRunningNodeAndItsRunningCheck(t *testing.T) {
	steps := map[int]string{6: "Agent · unit 1", 11: "Checks", 12: "Checks · red/green", 13: "Checks", 14: "Checks · test"}
	for i, want := range steps {
		m := New("issue-1", 120, 40)
		for _, ev := range unit[:i+1] {
			m.apply(run.Frame{Event: []byte(ev)})
		}
		if got := m.step(); got != want {
			t.Errorf("after %d events, step %q, want %q", i+1, got, want)
		}
	}
}
