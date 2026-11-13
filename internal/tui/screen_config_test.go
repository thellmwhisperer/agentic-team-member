package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func TestScreenChoiceAtRunAndGateEnd(t *testing.T) {
	for _, mode := range []string{"", "close", "keep"} {
		for _, outcome := range []string{"passed", "failed"} {
			for _, gateKind := range []string{"none", "over", "absent"} {
				t.Run(mode+"/"+outcome+"/"+gateKind, func(t *testing.T) {
					m := New("done-1", 100, 40)
					m.keepScreen, m.asking = mode == "keep", gateKind != "none"
					o := run.Outcome{Outcome: outcome, Ended: time.Now()}
					_, cmd := m.Update(ended{o, nil})
					if m.asking {
						if cmd != nil {
							t.Fatal("screen quit before a gate answer asked after the run ended")
						}
						_, cmd = m.Update(gate{ok: gateKind == "over", over: true, afterEnd: true})
						if m.asking {
							t.Fatal("screen keeps asking after the gate ended or was absent")
						}
					}
					assertScreenQuit(t, cmd, mode != "keep")
					for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'q'}}, {Type: tea.KeyCtrlC}} {
						_, cmd = m.Update(key)
						assertScreenQuit(t, cmd, true)
						if m.detached || m.outcome.Outcome != outcome {
							t.Fatalf("leaving the completed screen lost its outcome: %+v", m)
						}
					}
				})
			}
		}
	}
}

func TestScreenChoiceWhenOpeningCompletedRun(t *testing.T) {
	for _, keep := range []bool{false, true} {
		m := New("done-1", 80, 40)
		m.keepScreen = keep
		m.end(run.Outcome{Outcome: "passed"}, nil)
		assertScreenQuit(t, m.Init(), !keep)
	}
}

func assertScreenQuit(t *testing.T, cmd tea.Cmd, want bool) {
	t.Helper()
	if !want {
		if cmd != nil {
			t.Fatal("screen: keep returned a command after the run and gate ended")
		}
		return
	}
	if cmd == nil {
		t.Fatal("screen did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("screen returned a command other than quit")
	}
}
