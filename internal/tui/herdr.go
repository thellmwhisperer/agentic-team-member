package tui

import (
	"cmp"
	"context"
	"fmt"
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
	pane, label, step, last string
	m                       *Model
	released                bool
}

// openHerdr is the run label's herdr agent, reported from start, startup or resume; nil outside herdr.
func openHerdr(root, label, start string) *herdr {
	pane := os.Getenv("HERDR_PANE_ID")
	if os.Getenv("HERDR_ENV") != "1" || pane == "" {
		return nil
	}
	if label == "" { // the run atm attach follows: the last one that runs, else the last one
		runs, _ := run.Runs(root)
		for _, o := range runs {
			if label == "" || o.Ended.IsZero() {
				label = o.Run
			}
		}
	}
	h := &herdr{pane: pane, label: label, m: New(label, 0, 0)}
	h.call("report-agent-session", "--agent-session-id", label, "--session-start-source", start)
	return h
}

// frame reports the run working on its issue, then on each step, from the frame f on.
func (h *herdr) frame(f run.Frame) {
	h.m.apply(f)
	if s := h.m.step(); s != "" {
		h.step = s
	}
	if h.m.title == "" {
		return
	}
	msg := h.issue() + " " + h.m.title
	if h.step != "Issue" {
		msg = h.issue() + " " + h.step
	}
	if msg != h.last {
		h.last = msg
		h.call("report-agent", "--state", "working", "--message", msg)
	}
}

// end reports how the run ended.
func (h *herdr) end(o run.Outcome, err error) {
	msg := h.issue() + " passed"
	if err != nil || o.Outcome != "passed" {
		msg = h.issue() + " failed at " + cmp.Or(h.step, "start")
	}
	h.call("report-agent", "--state", "idle", "--message", msg)
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
