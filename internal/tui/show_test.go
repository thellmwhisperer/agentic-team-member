package tui

import (
	"bytes"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
	"golang.org/x/term"
)

func TestDeliveryTakesTheTerminalAtItsStartEvent(t *testing.T) {
	in, out, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	defer func() { _ = out.Close() }()
	p := tea.NewProgram(New("issue-1", 80, 24), tea.WithInput(in), tea.WithOutput(out))
	d := &delivery{p: p, in: in, out: &bytes.Buffer{}, keys: make(chan run.Input, 1)}
	d.frame(run.Frame{PTY: true})
	if d.read == nil {
		t.Fatal("the delivery start event did not transfer terminal input")
	}
	d.keyed.Cancel()
	<-d.read
	if d.state != nil {
		_ = term.Restore(int(d.in.Fd()), d.state)
	}
}
