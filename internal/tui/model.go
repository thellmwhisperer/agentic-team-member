// Package tui is a run's screen on a terminal, with a row for each node of ATM's graph.
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
	step, label, state, note string // state: pending, running, waiting, passed, failed, skipped, not run or unknown
	check                    bool
	start                    time.Time
	took                     time.Duration
	hasDuration              bool
}

// activity is a line of the Agent box: a tool call, by its id, its result and the first line of its output, or
// what the agent says or thinks.
type activity struct {
	id, tool, detail, result, summary, thinking, says string
}

// finding is a row of the Findings box, with a severity, error or warning, a reference and
// a description.
type finding struct {
	severity, ref, text string
}

// Model is a run's screen: what the run's frames said so far, drawn at the terminal's size.
type Model struct {
	label, title, state           string
	agentLabel                    string // " · " and the agent's harness, model and, when set, effort
	number                        int
	rows                          []*row
	agent                         []*activity
	output                        []string
	findings                      []finding
	width, height, spin           int
	historyHeight                 int
	now                           time.Time
	keys                          chan<- run.Input // where the terminal's size goes, for the delivery command
	done, detached                bool
	outcome                       run.Outcome
	err                           error
	pipeline, history             pane
	pipelineFocus                 bool
	pipelineBounds, historyBounds bounds
	unfold                        bool            // t: every tool call of the Agent box on its own line
	unfoldThink                   bool            // h: every thinking message of the Agent box in full
	unfoldTitle                   bool            // i: every line of the issue's title
	open                          map[string]bool // a run of tool calls or thinking, by its key, clicked open or closed
	toggles                       []string        // what each shown row of the Agent box opens or closes on a click
}

type bounds struct{ x, y, width, height int }

type paneLine struct{ key, parent, text, toggle string }

type pane struct {
	top, last, h, part int
	anchor, parent     string
	held               bool
}

func (p *pane) show(lines []paneLine, h int) []string {
	oldAnchor, oldParent, oldPart, oldTop := p.anchor, p.parent, p.part, p.top
	p.h, p.last = h, max(0, len(lines)-h)
	if p.held && oldAnchor != "" {
		p.top = keyedLine(lines, oldAnchor, oldPart)
		if p.top < 0 && oldParent != "" {
			p.top = keyedLine(lines, oldParent, 0)
		}
		if p.top < 0 {
			p.top = min(oldTop, p.last)
		}
	}
	if !p.held || p.top >= p.last {
		p.top, p.held = p.last, false
	}
	if p.top < len(lines) {
		p.anchor, p.parent = lines[p.top].key, lines[p.top].parent
		p.part = countKey(lines, p.top, lines[p.top].key)
	}
	out := make([]string, 0, min(h, len(lines)-p.top))
	for _, line := range lines[p.top:min(len(lines), p.top+h)] {
		out = append(out, line.text)
	}
	return out
}

func keyedLine(lines []paneLine, key string, part int) int {
	first, count := -1, 0
	for i, line := range lines {
		if line.key == key {
			if first < 0 {
				first = i
			}
			count++
		}
	}
	if first < 0 {
		return -1
	}
	return first + min(part, count-1)
}

func countKey(lines []paneLine, at int, key string) int {
	n := 0
	for i := at - 1; i >= 0 && lines[i].key == key; i-- {
		n++
	}
	return n
}

func (p *pane) scroll(n int) {
	p.top = min(max(0, p.top+n), p.last)
	p.held = p.top < p.last
	p.anchor, p.parent, p.part = "", "", 0
}

// focused is the pane keys scroll: the Agent box's while it shows, unless the user moved to the ATM box.
func (m *Model) focused() *pane {
	if m.pipelineFocus || !m.agentRuns() {
		return &m.pipeline
	}
	return &m.history
}

// New is the screen of run label before its first frame, every node pending.
func New(label string, width, height int) *Model {
	m := &Model{label: label, state: "waiting", width: width, height: height, now: time.Now()}
	for _, n := range [][2]string{{"issue", "Issue"}, {"clone", "Clone"}, {"contract", "Contract"},
		{"agent", "Agent"}, {"checks", "Checks"}, {"ponytail", "Slop detector"}, {"delivery", "Delivery"}} {
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
			m.detached = !m.done
			return m, tea.Quit
		}
		m.key(msg.String())
	case tea.MouseMsg:
		m.wheel(msg)
	case tick:
		m.now, m.spin = time.Time(msg), m.spin+1
		if !m.done {
			return m, next()
		}
	case run.Frame:
		m.apply(msg)
	case ended:
		m.end(msg.o, msg.err)
		return m, tea.Quit // the run ended, or attach failed: Show leaves the last screen on the terminal
	}
	return m, nil
}

// key scrolls the focused box, or, on tab, focuses the other, or, on t, folds or unfolds every tool call,
// on h, every thinking message and, on i, the issue's title.
func (m *Model) key(k string) {
	p := m.focused()
	switch k {
	case "t":
		m.unfold, m.open = !m.unfold, nil
	case "h":
		m.unfoldThink, m.open = !m.unfoldThink, nil
	case "i":
		m.unfoldTitle = !m.unfoldTitle
	case "tab":
		if m.agentRuns() {
			m.pipelineFocus = !m.pipelineFocus
		}
	case "up":
		p.scroll(-1)
	case "down":
		p.scroll(1)
	case "pgup":
		p.scroll(-max(1, p.h-1))
	case "pgdown":
		p.scroll(max(1, p.h-1))
	case "home":
		p.scroll(-p.top)
	case "end":
		p.scroll(p.last)
	}
}

// wheel scrolls the box under the pointer: the ATM box, or the Agent box while it shows. A click on a row of
// folded tool calls or thinking in the Agent box unfolds it, and on its first row once unfolded folds it again.
func (m *Model) wheel(msg tea.MouseMsg) {
	var p *pane
	if m.pipelineBounds.contains(msg.X, msg.Y) {
		p = &m.pipeline
	} else if m.historyBounds.contains(msg.X, msg.Y) {
		p = &m.history
	} else {
		return
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		p.scroll(-3)
	case tea.MouseButtonWheelDown:
		p.scroll(3)
	case tea.MouseButtonLeft:
		row := msg.Y - m.historyBounds.y - 1 // under the box's top border
		if p == &m.history && msg.Action == tea.MouseActionPress && row >= 0 && row < len(m.toggles) &&
			m.toggles[row] != "" {
			if m.open == nil {
				m.open = map[string]bool{}
			}
			m.open[m.toggles[row]] = !m.unfolded(m.toggles[row])
		}
	}
}

// unfolded says whether the run of tool calls or the thinking message by key shows in full.
func (m *Model) unfolded(key string) bool {
	if open, ok := m.open[key]; ok {
		return open
	}
	if strings.HasPrefix(key, "tools:") {
		return m.unfold
	}
	return m.unfoldThink
}

func (b bounds) contains(x, y int) bool {
	return x >= b.x && x < b.x+b.width && y >= b.y && y < b.y+b.height
}

// event is every field of a node's or a live event the screen reads.
type event struct {
	Step, State, Error, TS, Title, Type, SHA, Branch string
	Check, ID, Tool, Detail, Result, Summary         string
	Thinking, Says                                   string
	ResumedFrom                                      string `json:"resumed_from"`
	Output                                           *string
	Number                                           int
	DurationMS                                       int64 `json:"duration_ms"`
	Kept                                             bool
	Agent                                            *run.AgentSettings
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
	m.settings(e.Agent)
	switch {
	case e.ID != "" || e.Tool != "":
		m.tool(e)
	case e.Thinking != "" || e.Says != "":
		m.agent = append(m.agent, &activity{thinking: e.Thinking, says: e.Says})
	case e.Output != nil:
		m.output = append(m.output[max(0, len(m.output)-199):], ansi.Strip(*e.Output))
	case m.current(e.Step) == nil:
		return
	case e.Check != "":
		r := m.checkRow(e.Step, e.Check)
		r.state, r.took, r.start, r.hasDuration = e.State, time.Duration(e.DurationMS)*time.Millisecond, at, true
		if e.State == "started" {
			r.state = "running"
		}
	case e.State == "started":
		m.started(e.Step, at)
	case e.State != "":
		m.ended(e)
	}
	if m.running() {
		m.state = "running"
	}
}

// settings names agent a, when the event carries it, in the Agent box's title.
func (m *Model) settings(a *run.AgentSettings) {
	if a == nil {
		return
	}
	m.agentLabel = " · " + run.Outcome{Agent: a}.AgentLabel()
	if a.Effort != "" {
		m.agentLabel += " · " + a.Effort
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
	if step == "agent" && r.state == "pending" {
		r.label = "Agent · unit 1"
	}
	if step == "agent" && r.state != "pending" { // a chained unit: its rows again, after the last unit's
		i := slices.IndexFunc(m.rows, func(r *row) bool { return r.step == "ponytail" })
		m.rows = slices.Insert(m.rows, i, &row{step: "agent", label: fmt.Sprintf("Agent · unit %d", m.units()+1)},
			&row{step: "checks", label: "Checks", state: "pending"})
		r = m.rows[i]
	}
	if step == "agent" || step == "ponytail" {
		m.pipelineFocus = false
		m.agent, m.history, m.open = nil, pane{}, nil
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
	r.hasDuration = e.State != "pending" || e.DurationMS > 0
	switch e.Step {
	case "issue":
		m.title, m.number, r.note = e.Title, e.Number, e.Type
	case "clone":
		r.note = e.SHA[:min(8, len(e.SHA))]
	case "agent":
		r.note = fmt.Sprintf("%d tools", m.tools())
		if e.ResumedFrom != "" { // its agent's work, picked up: no agent call
			r.note = "resumed from " + e.ResumedFrom
		}
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
	for to, v := range map[*string]string{&a.tool: e.Tool, &a.detail: e.Detail, &a.result: e.Result,
		&a.summary: e.Summary} {
		if v != "" {
			*to = v
		}
	}
}

// end keeps how the run ended on screen until the user closes it.
func (m *Model) end(o run.Outcome, err error) {
	cold := !slices.ContainsFunc(m.rows, func(r *row) bool { return r.state != "pending" })
	if cold {
		m.restoreReport(o.Report, o)
	}
	m.done, m.outcome, m.err = true, o, err
	m.state = map[bool]string{true: "failed", false: o.Outcome}[err != nil || o.Outcome != "passed"]
	for _, r := range m.rows {
		if r.state == "pending" {
			r.state = map[bool]string{true: "skipped", false: "not run"}[o.Outcome == "passed"]
		}
	}
	if why := o.Reason; o.Outcome == "failed" && why != "" {
		if cold || o.FailedNode == "" {
			m.findings = append(m.findings, finding{"error", "atm run " + o.Issue, why})
		}
	}
	if err != nil {
		m.findings = append(m.findings, finding{"error", "atm", err.Error()})
	}
}
