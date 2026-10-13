package tui

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// herdr tells herdr, when the screen runs in one of its panes, what the run does: herdr shows it in its Agents
// section as atm. It reads the run's frames on a screen of its own.
type herdr struct {
	sync.Mutex
	pane, label, step, failedStep, last string
	m                                   *Model
	released                            bool
}

// openHerdr is the run label's herdr agent, reported from start, startup or resume; nil outside herdr.
func openHerdr(label, start string) *herdr {
	pane := os.Getenv("HERDR_PANE_ID")
	if os.Getenv("HERDR_ENV") != "1" || pane == "" {
		return nil
	}
	h := &herdr{pane: pane, label: label, m: New(label, 0, 0)}
	h.call("report-agent-session", "--agent-session-id", label, "--session-start-source", start)
	h.report(h.issue())
	return h
}

// frame reports the run working on its issue, then on each step, from the frame f on.
func (h *herdr) frame(f run.Frame) {
	var e event
	_ = json.Unmarshal(f.Event, &e)
	h.m.apply(f)
	if e.Check != "" && e.State == "failed" {
		h.failedStep = h.m.failedAt(h.m.current(e.Step))
	}
	if s := h.m.step(); s != "" {
		h.step = s
	}
	msg := h.issue()
	if h.m.title != "" {
		msg += " " + h.m.title
	}
	if h.step != "Issue" {
		msg = h.issue() + " " + h.step
	}
	h.report(msg)
}

func (h *herdr) report(msg string) {
	if msg != h.last {
		h.last = msg
		h.call("report-agent", "--state", "working", "--message", msg)
	}
}

// end reports how the run ended.
func (h *herdr) end(o run.Outcome, err error) {
	if o.Ended.IsZero() {
		return
	}
	msg := h.issue() + " passed"
	if err != nil || o.Outcome != "passed" {
		msg = h.issue() + " failed at " + cmp.Or(h.failedStep, h.step, "start")
	}
	h.call("report-agent", "--state", "idle", "--message", msg)
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

func followWithHerdr(root, label, start string, size *[2]int, in <-chan run.Input, fn func(run.Frame),
	followFn func(string, string, *[2]int, <-chan run.Input, func(run.Frame)) (run.Outcome, error)) (run.Outcome, error) {
	label, err := resolve(root, label)
	if err != nil {
		return run.Outcome{}, err
	}
	h := openHerdr(label, start)
	if h != nil {
		defer h.release()
		original := fn
		fn = func(f run.Frame) { h.frame(f); original(f) }
	}
	o, err := followFn(root, label, size, in, fn)
	if h != nil {
		h.end(o, err)
	}
	return o, err
}

// Attach shows plain run lines while reporting the same run to herdr.
func Attach(root, label, start string, w io.Writer) (run.Outcome, error) {
	return followWithHerdr(root, label, start, nil, nil, func(f run.Frame) {
		if f.Line != nil {
			_, _ = fmt.Fprintln(w, *f.Line)
		}
		_, _ = w.Write(f.Raw)
	}, run.Follow)
}

// release takes atm out of herdr's Agents section; nothing is reported after.
func (h *herdr) release() {
	h.call("release-agent")
	h.Lock()
	h.released = true
	h.Unlock()
}

// issue is the issue's number, or the run's label for an issue file.
func (h *herdr) issue() string {
	if h.m.number != 0 {
		return fmt.Sprintf("#%d", h.m.number)
	}
	return h.label
}

// call runs herdr pane cmd with args, a second at most; whatever happens to it changes nothing.
// ponytail: on the screen's path, a hung herdr holds the screen a second per call.
func (h *herdr) call(cmd string, args ...string) {
	h.Lock()
	defer h.Unlock()
	if h.released {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	args = append([]string{"pane", cmd, h.pane, "--source", "atm", "--agent", "atm"}, args...)
	args = append(args, "--seq", strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = exec.CommandContext(ctx, cmp.Or(os.Getenv("HERDR_BIN_PATH"), "herdr"), args...).Run()
}

// step is the label of the node that runs, and of its proof or command that runs, if one does; "" when none.
func (m *Model) step() string {
	s := ""
	for _, r := range m.rows {
		switch {
		case r.state != "running":
		case !r.check:
			s = r.label
		case s != "":
			s += " · " + r.label
		}
	}
	return s
}
