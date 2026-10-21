package tui

import (
	"cmp"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// gate is what no-mistakes, the gate after Delivery, shows of the run of the delivered branch: its steps, and
// whether it ended; not ok when it shows no run.
type gate struct {
	steps    []gateStep
	ok, over bool
}

// gateStep is a step of the gate: its state as the screen names it, its log and its findings.
type gateStep struct {
	name, state string
	log         []string
	findings    []map[string]string
}

// gateStates is each no-mistakes step status as the screen names it; any other is unknown.
var gateStates = map[string]string{"pending": "pending", "running": "running", "awaiting_approval": "waiting",
	"fixing": "running", "completed": "passed", "failed": "failed", "skipped": "skipped"}

// askGate asks no-mistakes, in clone, for the run of its branch, and for the log of each step it started.
// ponytail: every started step's log on every ask, one no-mistakes call each; ask only the changed ones if it lags.
func askGate(clone string) tea.Msg {
	run, tables, err := axi(clone, "status")
	if err != nil || run["run.status"] == "" {
		return gate{}
	}
	g := gate{ok: true, over: slices.Contains([]string{"completed", "failed", "cancelled"}, run["run.status"])}
	for _, s := range tables["run.steps"] {
		step := gateStep{name: s["step"], state: cmp.Or(gateStates[s["status"]], "unknown")}
		if step.state != "pending" && step.state != "skipped" {
			_, tables, _ := axi(clone, "logs", "--step", step.name, "--full")
			for _, l := range tables["log"] {
				step.log = append(step.log, l["line"])
			}
			step.findings = tables["findings"]
		}
		if step.name == run["gate.step"] {
			for _, f := range tables["gate.findings"] {
				if !slices.ContainsFunc(step.findings, func(shown map[string]string) bool { return shown["id"] == f["id"] }) {
					step.findings = append(step.findings, f)
				}
			}
		}
		g.steps = append(g.steps, step)
	}
	return g
}

// axi is no-mistakes axi args in dir, read as TOON.
func axi(dir string, args ...string) (map[string]string, map[string][]map[string]string, error) {
	cmd := exec.Command("no-mistakes", append([]string{"axi"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	values, tables := toon(string(out))
	return values, tables, err
}

// toon reads a TOON document: each value by its dotted path, and each table's rows by column.
func toon(doc string) (map[string]string, map[string][]map[string]string) {
	values, tables := map[string]string{}, map[string][]map[string]string{}
	var path, cols []string
	table := ""
	for _, l := range strings.Split(doc, "\n") {
		s := strings.TrimLeft(l, " ")
		depth := (len(l) - len(s)) / 2
		if s == "" {
			continue
		}
		if cols != nil && depth > len(path) {
			row := map[string]string{}
			for i, v := range fields(s) {
				if i < len(cols) {
					row[cols[i]] = v
				}
			}
			tables[table] = append(tables[table], row)
			continue
		}
		cols, path = nil, path[:min(depth, len(path))]
		key, value, _ := strings.Cut(s, ":")
		if name, shape, ok := strings.Cut(key, "["); ok && strings.HasSuffix(shape, "}") {
			_, header, _ := strings.Cut(strings.TrimSuffix(shape, "}"), "{")
			cols, table = strings.Split(header, ","), strings.Join(append(path, name), ".")
			continue
		}
		if value = strings.TrimSpace(value); value == "" {
			path = append(path, key)
			continue
		}
		values[strings.Join(append(path, key), ".")] = unquote(value)
	}
	return values, tables
}

// fields is a TOON table row's values: split at commas outside quotes, each unquoted.
func fields(row string) (out []string) {
	quoted, start := false, 0
	for i := 0; i < len(row); i++ {
		switch {
		case quoted && row[i] == '\\':
			i++
		case row[i] == '"':
			quoted = !quoted
		case !quoted && row[i] == ',':
			out, start = append(out, unquote(row[start:i])), i+1
		}
	}
	return append(out, unquote(row[start:]))
}

func unquote(v string) string {
	if u, err := strconv.Unquote(v); err == nil {
		return u
	}
	return v
}

// gate puts the gate's steps in the ATM box, below ATM's, and keeps what they emit for the right column.
func (m *Model) gate(g gate) {
	m.gateSteps = g.steps
	for _, s := range g.steps {
		r := m.current("gate:" + s.name)
		if r == nil {
			r = &row{step: "gate:" + s.name, label: s.name}
			m.rows = append(m.rows, r)
		}
		r.state = s.state
	}
}

// ask asks the gate again after wait while g shows it goes on, and else ends the screen.
// ponytail: one failed ask ends the screen, as a run's end does; ask again first if the gate stutters.
func (m *Model) ask(g gate, wait time.Duration) tea.Cmd {
	if !g.ok || g.over {
		return tea.Quit
	}
	clone := m.clone
	return tea.Tick(wait, func(time.Time) tea.Msg { return askGate(clone) })
}

// gateKey answers the gate from the footer, through no-mistakes' own commands in the clone: a, f and s approve,
// fix and skip the step waiting; x, then y, aborts the gate; enter takes a line, the answer to the reviewer's
// question while one shows, else a fix's instructions. ok when the key was the gate's.
func (m *Model) gateKey(k tea.KeyMsg) (cmd tea.Cmd, ok bool) {
	switch {
	case m.typing:
		switch k.Type {
		case tea.KeyEnter, tea.KeyEsc, tea.KeyBackspace, tea.KeyRunes, tea.KeySpace:
			return m.typed(k), true
		}
		return nil, false
	case m.aborting:
		m.aborting = false
		if k.String() == "y" {
			return m.respond("abort"), true
		}
		return nil, false
	}
	m.fixSelected = false
	waiting := m.waiting() != nil
	switch s := k.String(); {
	case s == "x" && m.gateSteps != nil:
		m.aborting = true
	case s == "enter" && (waiting || m.question() != ""):
		m.typing, m.text = true, ""
	case waiting && (s == "a" || s == "s"):
		return m.respond("respond", "--action", map[string]string{"a": "approve", "s": "skip"}[s]), true
	case waiting && s == "f":
		m.fixSelected = true
	default:
		return nil, false
	}
	return nil, true
}

// typed takes k into the line: enter sends it, esc drops it, backspace deletes its last character.
func (m *Model) typed(k tea.KeyMsg) tea.Cmd {
	switch k.Type {
	case tea.KeyEnter:
		m.typing = false
		return m.send(m.text)
	case tea.KeyEsc:
		m.typing = false
	case tea.KeyBackspace:
		m.text = string([]rune(m.text)[:max(0, len([]rune(m.text))-1)])
	case tea.KeyRunes, tea.KeySpace:
		m.text += string(k.Runes)
	}
	return nil
}

// gateKeys is the footer's keys for the gate while it shows.
func (m *Model) gateKeys() string {
	key := func(k, what string) string {
		return "  " + lipgloss.NewStyle().Bold(true).Render(k) + " " + paint(dim, what)
	}
	switch {
	case m.aborting:
		return key("y", "abort the gate")
	case m.fixSelected:
		return key("enter", "fix with a line")
	case m.question() != "":
		return key("enter", "answer") + key("x", "abort")
	case m.waiting() != nil:
		return key("a", "approve") + key("f", "fix") + key("s", "skip") + key("x", "abort") +
			key("enter", "fix with a line")
	case m.gateSteps != nil:
		return key("x", "abort")
	}
	return ""
}

// waiting is the gate's step waiting for an answer, nil for none.
func (m *Model) waiting() *gateStep {
	for i := range m.gateSteps {
		if m.gateSteps[i].state == "waiting" {
			return &m.gateSteps[i]
		}
	}
	return nil
}

// question is the id of the first question the gate's reviewer asked, a question-<id> finding; empty for none.
func (m *Model) question() string {
	for _, s := range m.gateSteps {
		for _, f := range s.findings {
			if strings.HasPrefix(f["id"], "question-") && !slices.Contains(m.answered, f["id"]) {
				return f["id"]
			}
		}
	}
	return ""
}

type gateAnswer string

// send answers the reviewer's question with line while one shows, and else fixes with line.
func (m *Model) send(line string) tea.Cmd {
	if q := m.question(); q != "" {
		clone := m.clone
		return func() tea.Msg {
			if _, _, err := axi(clone, "answer", "--question", q, "--answer", line); err == nil {
				return gateAnswer(q)
			}
			return nil
		}
	}
	return m.fix(line)
}

// fix fixes the waiting step's findings with line as instructions: those named by ids before a colon
// ("<id>,<id>: <text>"), ignoring the others shown, or else every one shown.
func (m *Model) fix(line string) tea.Cmd {
	var shown []string
	if w := m.waiting(); w != nil {
		for _, f := range w.findings {
			shown = append(shown, f["id"])
		}
	}
	fix, ignore := shown, []string(nil)
	if head, text, ok := strings.Cut(line, ":"); ok {
		named := strings.Split(head, ",")
		for i := range named {
			named[i] = strings.TrimSpace(named[i])
		}
		if !slices.ContainsFunc(named, func(id string) bool { return !slices.Contains(shown, id) }) {
			fix, line = named, strings.TrimSpace(text)
			ignore = slices.DeleteFunc(slices.Clone(shown), func(id string) bool { return slices.Contains(named, id) })
		}
	}
	args := []string{"respond", "--action", "fix", "--findings", strings.Join(fix, ",")}
	if len(ignore) > 0 {
		args = append(args, "--ignore", strings.Join(ignore, ","))
	}
	if line != "" {
		args = append(args, "--instructions", line)
	}
	return m.respond(args...)
}

// respond runs no-mistakes axi args in the clone, aside, as respond blocks until the gate's next stop; the screen
// sees what it did on its next ask.
// ponytail: a refused answer shows nowhere but in the gate staying parked; paint its error if that confuses.
func (m *Model) respond(args ...string) tea.Cmd {
	clone := m.clone
	return func() tea.Msg {
		_, _, _ = axi(clone, args...)
		return nil
	}
}

// gateBox is what the gate's started steps emit, in lines rows: a header row for each, a click folding it as
// thinking folds, then its log rendered as the Agent box renders Markdown, its first line only while folded,
// then its findings, one line each.
func (m *Model) gateBox(width, lines int) string {
	var out []paneLine
	for _, s := range m.gateSteps {
		if s.state == "pending" || s.state == "skipped" {
			continue
		}
		key := "gate:" + s.name
		open := m.unfolded(key)
		out = append(out, paneLine{key: key, toggle: key, text: fold(open) + " " + m.icon(s.state) + " " + s.name})
		log := s.log
		if !open && len(log) > 0 {
			log = log[:1]
		}
		texts, _, _ := agentTextRows("  ", strings.Join(log, "\n"), width, codeFence{})
		for _, text := range texts {
			out = append(out, paneLine{key: key, text: text})
		}
		for _, f := range s.findings {
			out = append(out, paneLine{key: key, text: "  " + f["id"] + " " + paint(yellow, f["severity"]) + " " +
				f["file"] + " " + paint(dim, oneLine(f["description"]))})
		}
	}
	shown := m.history.show(out, lines)
	result := box(bold(cyan, "Gate"), strings.Join(shown, "\n"), width, m.border(&m.history))
	m.historyHeight = lipgloss.Height(result)
	return result
}
