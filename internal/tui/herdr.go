package tui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// herdr tells herdr, when the screen runs in one of its panes, what the run does: herdr shows it in its Agents
// section as atm. It reads the run's frames on a screen of its own.
type herdr struct {
	pane, label, step, failedStep, last string
	m                                   *Model
}

// openHerdr is the run label's herdr agent, reported from start, startup or resume; nil outside herdr.
func openHerdr(label, start string) *herdr {
	pane := os.Getenv("HERDR_PANE_ID")
	if os.Getenv("HERDR_ENV") != "1" || pane == "" {
		return nil
	}
	h := &herdr{pane: pane, label: label, m: New(label, 0, 0)}
	h.call("report-agent-session", "--agent-session-id", label, "--session-start-source", start)
	h.report("working", h.issue())
	return h
}

// frame reports the run working on its issue, then on each step, from the frame f on.
func (h *herdr) frame(f run.Frame) {
	var e event
	_ = json.Unmarshal(f.Event, &e)
	h.m.apply(f)
	if e.Check != "" && e.State == "failed" {
		h.failedStep = h.m.failedAt(h.m.current(e.Step))
	} else if e.Check == "" && (e.State == "started" || e.State == "passed") {
		h.failedStep = ""
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
	state := "working"
	if h.m.waits() && !h.m.running() {
		state = "blocked"
	}
	h.report(state, msg)
}

func (h *herdr) report(state, msg string) {
	if state+"\n"+msg != h.last {
		h.last = state + "\n" + msg
		h.call("report-agent", "--state", state, "--message", msg)
	}
}

// end reports how the run ended.
func (h *herdr) end(o run.Outcome, err error) {
	if o.Ended.IsZero() {
		return
	}
	msg := h.issue() + " passed"
	if err != nil || o.Outcome != "passed" {
		msg = h.issue() + " failed at " + cmp.Or(h.failedStep, h.failedNode(o), h.step, "start")
	}
	h.call("report-agent", "--state", "idle", "--message", msg)
}

// failedNode names the failed node even if no running state was observed.
func (h *herdr) failedNode(o run.Outcome) string {
	if r := h.m.current(o.FailedNode); r != nil {
		return h.m.failedAt(r)
	}
	for i := len(h.m.rows) - 1; i >= 0; i-- {
		if r := h.m.rows[i]; !r.check && r.state == "failed" {
			return h.m.failedAt(r)
		}
	}
	return gateLabels[o.FailedNode]
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
	var err error
	label, err = resolve(root, label)
	if err != nil {
		return run.Outcome{}, err
	}
	if label == "" {
		return run.Outcome{}, errors.New("no run yet")
	}
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
	args = append([]string{"pane", cmd, h.pane, "--source", "atm", "--agent", "atm"}, args...)
	args = append(args, "--seq", strconv.FormatInt(time.Now().UnixNano(), 10))
	_, _ = herdrRun(args...)
}

// herdrRun runs herdr with args, a second at most, and returns its stdout.
func herdrRun(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return exec.CommandContext(ctx, cmp.Or(os.Getenv("HERDR_BIN_PATH"), "herdr"), args...).Output()
}

// OpenHerdrTab creates a tab labelled tabLabel in the workspace named workspace, cwd the repository, and returns
// the tab id tab create returned and its root pane. herdr's --workspace wants that id, not the label: workspace
// list matches label.
func OpenHerdrTab(workspace, tabLabel, cwd string) (string, string, error) {
	id, err := herdrWorkspaceID(workspace)
	if err != nil {
		return "", "", err
	}
	b, err := herdrRun("tab", "create", "--workspace", id, "--label", tabLabel, "--no-focus", "--cwd", cwd)
	if err != nil {
		return "", "", err
	}
	tab, pane, err := createdTab(b)
	if err == nil && pane == "" {
		err = fmt.Errorf("tab create: no root pane")
	}
	return tab, pane, err
}

// RunHerdrPane runs command in herdr pane pane.
func RunHerdrPane(pane, command string) error {
	_, err := herdrRun("pane", "run", pane, command)
	return err
}

// CloseHerdrTab closes the tab id tab create returned, gate pane included.
func CloseHerdrTab(id string) error {
	_, err := herdrRun("tab", "close", id)
	return err
}

func herdrWorkspaceID(label string) (string, error) {
	b, err := herdrRun("workspace", "list")
	if err != nil {
		return "", err
	}
	var listed struct {
		Result struct {
			Workspaces []struct {
				ID    string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &listed); err != nil {
		return "", err
	}
	for _, w := range listed.Result.Workspaces {
		if w.Label == label {
			return w.ID, nil
		}
	}
	return "", fmt.Errorf("no workspace %q", label)
}

func createdTab(b []byte) (tab, pane string, err error) {
	var resp struct {
		Result struct {
			Tab struct {
				ID string `json:"tab_id"`
			} `json:"tab"`
			Pane struct {
				ID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err = json.Unmarshal(b, &resp); err != nil || resp.Result.Tab.ID == "" {
		return "", "", fmt.Errorf("tab create: no tab id")
	}
	return resp.Result.Tab.ID, resp.Result.Pane.ID, nil
}

// step is the label of the node that runs, and of its proof or command that runs, if one does; "" when none.
func (m *Model) step() string {
	s, wait := "", ""
	for _, r := range m.activityRows() {
		switch {
		case r.state == "waiting" && !r.check:
			wait = r.label
		case r.state != "running":
		case !r.check:
			s = r.label
		case s != "":
			s += " · " + r.label
		}
	}
	if s == "" {
		return wait
	}
	return s
}

// waits says whether a gate step is waiting for a decision.
func (m *Model) waits() bool {
	for _, r := range m.activityRows() {
		if r.state == "waiting" {
			return true
		}
	}
	return false
}
