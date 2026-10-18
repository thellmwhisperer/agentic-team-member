package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

var follow = run.Follow // tests follow a run of their own

// Show shows the screen of run label, of the repository at root, on the terminal in and out until the user
// closes it or the run ends; detached when the user left it going. While delivery runs on the terminal, the
// terminal is the delivery command's: it shows its own screen there.
func Show(root, label string, in, out *os.File) (o run.Outcome, detached bool, err error) {
	label, err = resolve(root, label)
	if err != nil {
		return o, false, err
	}
	if label == "" {
		return o, false, errors.New("no run yet")
	}
	w, h, err := term.GetSize(int(out.Fd()))
	if err != nil {
		return o, false, err
	}
	keys := make(chan run.Input, 64)
	m := New(label, w, h)
	m.keys = keys
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen(), tea.WithMouseCellMotion())
	d := &delivery{p: p, in: in, out: out, keys: keys}
	done := make(chan struct{})
	go func() {
		defer close(done)
		o, err := follow(root, label, &[2]int{w, h}, keys, d.frame)
		d.giveBack()
		p.Send(ended{o, err})
	}()
	_, err = p.Run()
	close(keys)
	<-done
	if err != nil {
		return o, false, err
	}
	_, _ = fmt.Fprintln(out, m.View()) // the alternate screen is gone: the last one stays
	return m.outcome, m.detached, m.err
}

// Attach shows run label's plain lines on w until it ends.
func Attach(root, label string, w io.Writer) (run.Outcome, error) {
	label, err := resolve(root, label)
	if err != nil {
		return run.Outcome{}, err
	}
	if label == "" {
		return run.Outcome{}, errors.New("no run yet")
	}
	return run.Follow(root, label, nil, nil, func(f run.Frame) {
		if f.Line != nil {
			_, _ = fmt.Fprintln(w, *f.Line)
		}
		_, _ = w.Write(f.Raw)
	})
}

// resolve chooses the run that an empty-label attach will follow, once.
func resolve(root, label string) (string, error) {
	if label != "" {
		return label, nil
	}
	runs, err := run.Runs(root)
	if err != nil {
		return "", err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Ended.IsZero() {
			return runs[i].Run, nil
		}
	}
	if len(runs) > 0 {
		return runs[len(runs)-1].Run, nil
	}
	return "", nil
}

// delivery is the delivery command's turn at the terminal: from its first output to its end, the screen
// gives it the terminal, raw, and its keys.
type delivery struct {
	p     *tea.Program
	in    *os.File
	out   io.Writer
	keys  chan<- run.Input
	state *term.State
	keyed cancelreader.CancelReader
	read  chan struct{} // closed once keys are no longer read
	pty   bool          // the delivery command's terminal started and has written nothing yet
}

// frame takes frame f: the delivery command's output goes to the terminal, everything else to the screen.
func (d *delivery) frame(f run.Frame) {
	if f.PTY {
		d.pty = true
		return
	}
	if f.Raw != nil {
		if d.pty {
			d.pty = false
			d.take()
		}
		_, _ = d.out.Write(f.Raw)
		return
	}
	var e struct{ Step, State string }
	if json.Unmarshal(f.Event, &e) == nil && e.Step == "delivery" && e.State != "started" && e.State != "" {
		d.giveBack()
	}
	d.p.Send(f)
}

// take gives the terminal to the delivery command: the screen lets it go and its keys go to the command.
func (d *delivery) take() {
	_ = d.p.ReleaseTerminal()
	d.state, _ = term.MakeRaw(int(d.in.Fd()))
	d.read = make(chan struct{})
	var err error
	if d.keyed, err = cancelreader.NewReader(d.in); err != nil {
		close(d.read)
		return
	}
	go func() {
		defer close(d.read)
		b := make([]byte, 256)
		for {
			n, err := d.keyed.Read(b)
			if n > 0 {
				d.keys <- run.Input{Keys: append([]byte(nil), b[:n]...)}
			}
			if err != nil {
				return
			}
		}
	}()
}

// giveBack gives the terminal back to the screen, which redraws and takes the mouse again, once the delivery
// command had it.
func (d *delivery) giveBack() {
	if d.read == nil {
		return
	}
	if d.keyed != nil {
		d.keyed.Cancel()
	}
	<-d.read
	if d.state != nil {
		_ = term.Restore(int(d.in.Fd()), d.state)
	}
	d.read, d.keyed, d.state = nil, nil, nil
	_ = d.p.RestoreTerminal()
	d.p.Send(tea.EnableMouseCellMotion())
}
