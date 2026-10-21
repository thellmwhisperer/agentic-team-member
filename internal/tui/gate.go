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
		for _, text := range agentTextRows("  ", strings.Join(log, "\n"), width) {
			out = append(out, paneLine{key: key, text: text})
		}
		for _, f := range s.findings {
			out = append(out, paneLine{key: key, text: "  " + paint(yellow, f["severity"]) + " " +
				f["file"] + " " + paint(dim, oneLine(f["description"]))})
		}
	}
	shown := m.history.show(out, lines)
	result := box(bold(cyan, "Gate"), strings.Join(shown, "\n"), width, m.border(&m.history))
	m.historyHeight = lipgloss.Height(result)
	return result
}
