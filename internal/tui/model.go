// Package tui is a run's screen on a terminal: no-mistakes' screen, built to its internal/tui/DESIGN.md, with
// a row for each node of ATM's graph. ATM and no-mistakes run one after the other on the same branch.
package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// row is a row of the ATM box: a node of the graph, or, when check, a proof or command of the node before.
type row struct {
	step, label, state, note string // state: pending, running, waiting, passed, failed, skipped or not run
	check                    bool
	start                    time.Time
	took                     time.Duration
}

// activity is a line of the Agent box: a tool call, by its id, and its result, or the agent's thinking.
type activity struct {
	id, tool, detail, result, thinking string
}

// finding is a row of the Findings box, as no-mistakes has them: a severity, error or warning, a reference and
// a description.
type finding struct {
	severity, ref, text string
}

// Model is a run's screen: what the run's frames said so far, drawn at the terminal's size.
type Model struct {
	label, title, state string
	number              int
	rows                []*row
	agent               []*activity
	output              []string
	findings            []finding
	width, height, spin int
	now                 time.Time
	keys                chan<- run.Input // where the terminal's size goes, for the delivery command
	done, detached      bool
	outcome             run.Outcome
	err                 error
}

// New is the screen of run label before its first frame, every node pending.
func New(label string, width, height int) *Model {
	m := &Model{label: label, state: "waiting", width: width, height: height, now: time.Now()}
	for _, n := range [][2]string{{"issue", "Issue"}, {"clone", "Clone"}, {"contract", "Contract"},
		{"agent", "Agent · unit 1"}, {"checks", "Checks"}, {"ponytail", "Slop detector"}, {"delivery", "Delivery"}} {
		m.rows = append(m.rows, &row{step: n[0], label: n[1], state: "pending"})
	}
	return m
}

type tick time.Time

// ended is the run's end, as Follow returns it.
type ended struct {
	o   run.Outcome
	err error
}

func (m *Model) Init() tea.Cmd { return next() }

func next() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tick(t) })
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		select {
		case m.keys <- run.Input{Size: &[2]int{msg.Width, msg.Height}}:
		default: // ponytail: a size dropped while the queue is full; the next one gets through
		}
	case tea.KeyMsg:
		if s := msg.String(); s == "q" || s == "ctrl+c" {
			m.detached = true
			return m, tea.Quit
		}
	case tick:
		m.now, m.spin = time.Time(msg), m.spin+1
		return m, next()
	case run.Frame:
		m.apply(msg)
	case ended:
		m.end(msg.o, msg.err)
		return m, tea.Quit
	}
	return m, nil
}

// event is every field of a node's or a live event the screen reads.
type event struct {
	Step, State, Error, TS, Title, Type, SHA, Branch string
	Check, ID, Tool, Detail, Result, Thinking        string
	Output                                           *string
	Number                                           int
	DurationMS                                       int64 `json:"duration_ms"`
	Kept                                             bool
	Report                                           struct {
		Findings []struct{ File, Family, Finding string }
	}
}

// apply reads frame f, when it carries an event: a node's start or end, or what happens inside it.
func (m *Model) apply(f run.Frame) {
	var e event
	if f.Event == nil || json.Unmarshal(f.Event, &e) != nil {
		return
	}
	at, _ := time.Parse(time.RFC3339Nano, e.TS)
	switch {
	case e.Check != "":
		r := m.checkRow(e.Step, e.Check)
		r.state, r.took, r.start = e.State, time.Duration(e.DurationMS)*time.Millisecond, at
		if e.State == "started" {
			r.state = "running"
		}
	case e.ID != "" || e.Tool != "":
		m.tool(e)
	case e.Thinking != "":
		m.agent = append(m.agent, &activity{thinking: e.Thinking})
	case e.Output != nil:
		m.output = append(m.output[max(0, len(m.output)-199):], ansi.Strip(*e.Output))
	case e.State == "started":
		m.started(e.Step, at)
	case e.State != "":
		m.ended(e)
	}
}

// current is the last row of node step.
func (m *Model) current(step string) *row {
	for i := len(m.rows) - 1; i >= 0; i-- {
		if r := m.rows[i]; !r.check && r.step == step {
			return r
		}
	}
	return nil
}

func (m *Model) started(step string, at time.Time) {
	m.state, m.output = "running", nil
	r := m.current(step)
	if r == nil {
		return
	}
	if step == "agent" && r.state != "pending" { // a chained unit: its rows again, after the last unit's
		i := slices.IndexFunc(m.rows, func(r *row) bool { return r.step == "ponytail" })
		m.rows = slices.Insert(m.rows, i, &row{step: "agent", label: fmt.Sprintf("Agent · unit %d", m.units()+1)},
			&row{step: "checks", label: "Checks", state: "pending"})
		r = m.rows[i]
	}
	if step == "agent" || step == "ponytail" {
		m.agent = nil
	}
	r.state, r.start = "running", at
}

// units is how many units have a row.
func (m *Model) units() (n int) {
	for _, r := range m.rows {
		if !r.check && r.step == "agent" {
			n++
		}
	}
	return n
}

// tools is how many tool calls the Agent box has.
func (m *Model) tools() (n int) {
	for _, a := range m.agent {
		if a.tool != "" {
			n++
		}
	}
	return n
}

func (m *Model) ended(e event) {
	r := m.current(e.Step)
	if r == nil {
		return
	}
	r.state, r.took = e.State, time.Duration(e.DurationMS)*time.Millisecond
	switch e.Step {
	case "issue":
		m.title, m.number, r.note = e.Title, e.Number, e.Type
	case "clone":
		r.note = e.SHA[:min(8, len(e.SHA))]
	case "agent":
		r.note = fmt.Sprintf("%d tools", m.tools())
	case "ponytail":
		r.note = map[bool]string{true: fmt.Sprintf("%d cuts", len(e.Report.Findings)), false: "no cut"}[e.Kept]
		for _, f := range e.Report.Findings {
			if e.Kept {
				m.findings = append(m.findings, finding{"warning", f.File + " · " + f.Family, f.Finding})
			}
		}
	case "delivery":
		r.note = e.Branch
	}
	if e.State == "failed" {
		why, _, _ := strings.Cut(e.Error, "\n")
		m.findings = append(m.findings, finding{"error", m.failedAt(r), why})
	}
}

// failedAt is the failed node r's label, and its failed proof or command's, if one failed.
func (m *Model) failedAt(r *row) string {
	for _, c := range m.rows[slices.Index(m.rows, r)+1:] {
		if !c.check {
			break
		}
		if c.state == "failed" {
			return r.label + " · " + c.label
		}
	}
	return r.label
}

// checkRow is the row of proof or command name under the current row of node step, added when new.
func (m *Model) checkRow(step, name string) *row {
	i := slices.Index(m.rows, m.current(step))
	if i < 0 {
		i = len(m.rows) - 1
	}
	for i++; i < len(m.rows) && m.rows[i].check; i++ {
		if m.rows[i].label == name {
			return m.rows[i]
		}
	}
	r := &row{step: step, label: name, check: true}
	m.rows = slices.Insert(m.rows, i, r)
	return r
}

// tool notes a tool call, or its result, by its id; its empty fields leave the call's as they were.
func (m *Model) tool(e event) {
	i := slices.IndexFunc(m.agent, func(a *activity) bool { return e.ID != "" && a.id == e.ID })
	if i < 0 {
		m.agent = append(m.agent, &activity{id: e.ID})
		i = len(m.agent) - 1
	}
	a := m.agent[i]
	for to, v := range map[*string]string{&a.tool: e.Tool, &a.detail: e.Detail, &a.result: e.Result} {
		if v != "" {
			*to = v
		}
	}
}

// end closes the screen on how the run ended: the nodes it never ran are skipped when it passed.
func (m *Model) end(o run.Outcome, err error) {
	m.done, m.outcome, m.err = true, o, err
	m.state = map[bool]string{true: "failed", false: o.Outcome}[err != nil || o.Outcome != "passed"]
	for _, r := range m.rows {
		if r.state == "pending" {
			r.state = map[bool]string{true: "skipped", false: "not run"}[o.Outcome == "passed"]
		}
	}
	if why := o.Reason; o.Outcome == "failed" && o.FailedNode == "" && why != "" {
		m.findings = append(m.findings, finding{"error", "atm run " + o.Issue, why})
	}
	if err != nil {
		m.findings = append(m.findings, finding{"error", "atm", err.Error()})
	}
}
