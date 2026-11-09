package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

var follow = run.FollowFrom // tests follow a run of their own

// Show shows the screen of run label, of the repository at root, on the terminal in and out until the user
// closes it or the run ends; after passed Delivery with a gate, until the user closes it, also once the gate
// ends; detached when the user left it going. The screen keeps the terminal all along: Delivery's command runs
// below it, and the gate shows in its pipeline.
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
	m := New(label, w, h)
	runs, _ := run.RecordedRuns(root)
	for _, r := range runs {
		if r.Run == label && r.BaseSHA != "" {
			c, _, _ := run.BaseConfig(root, r.BaseSHA, os.Getenv("HOME"), config.Agent{})
			if c.Delivery != "" {
				for _, name := range []string{"intent", "rebase", "review", "test", "document", "lint", "push", "pr", "ci"} {
					m.rows = append(m.rows, &row{step: "gate:" + name, label: name, state: "pending"})
				}
			}
			break
		}
	}
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen(), tea.WithMouseCellMotion())
	leave, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		// A run that waits for its launcher's answer is not over: the screen follows it again, from the frame
		// after the last it has, once a second, until it goes on or ends.
		for seen := 0; ; {
			o, err := follow(root, label, seen, leave, func(f run.Frame) { seen++; p.Send(f) })
			if err != nil || !o.Waits() {
				p.Send(ended{o, err})
				return
			}
			select {
			case <-leave:
				return
			case <-time.After(time.Second):
			}
		}
	}()
	_, err = p.Run()
	close(leave)
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
	return run.Attach(root, label, w)
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
