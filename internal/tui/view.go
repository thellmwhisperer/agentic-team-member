package tui

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// The colours of DESIGN.md's roles, ANSI only, so they follow the terminal's theme.
const (
	red, green, yellow, blue, cyan, dim = "1", "2", "3", "4", "6", "8"
)

func init() { lipgloss.SetColorProfile(termenv.ANSI) }

func paint(color, s string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(s)
}

func bold(color, s string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(color)).Render(s)
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// states is each state's icon and colour; its word is its name. Running's icon is the spinner.
var states = map[string][2]string{"pending": {"○", dim}, "running": {"", blue}, "waiting": {"⏸", yellow},
	"passed": {"✓", green}, "failed": {"✗", red}, "skipped": {"–", dim}, "not run": {"○", dim}}

// icon is state's icon in its colour.
func (m *Model) icon(state string) string {
	s := states[state]
	if state == "running" {
		s[0] = spinner[m.spin%len(spinner)]
	}
	return paint(s[1], s[0])
}

// severities is each finding severity's icon and colour, as no-mistakes has them.
var severities = map[string][2]string{"error": {"E", red}, "warning": {"W", yellow}}

// Layout, as DESIGN.md has it: two columns from 100 columns, the left a third between 38 and 48; connectors
// between nodes from 30 rows.
const (
	twoColumns, leftMin, leftMax, gap, connectors = 100, 38, 48, 2, 30
)

// View is the screen: the ATM box, then the Agent or Findings box and the Log box, beside it from 100 columns,
// below it under that, and the footer.
func (m *Model) View() string {
	width := max(m.width, 40)
	if width < twoColumns || !m.sidebar() {
		body := m.atm(width)
		if more := m.right(width, m.height-2-lipgloss.Height(body)-1); more != "" {
			body += "\n\n" + more
		}
		return body + m.footer()
	}
	left := min(max(width/3, leftMin), leftMax)
	return columns(m.atm(left), m.right(width-left-gap, m.height-2), left) + m.footer()
}

func (m *Model) footer() string {
	if m.done {
		return ""
	}
	return "\n\n  " + lipgloss.NewStyle().Bold(true).Render("q") + " " + paint(dim, "detach")
}

// columns is left, every line padded to width, beside right.
func columns(left, right string, width int) string {
	l, r := strings.Split(left, "\n"), strings.Split(right, "\n")
	lines := make([]string, max(len(l), len(r)))
	for i := range lines {
		if i < len(l) {
			lines[i] = l[i] + strings.Repeat(" ", max(0, width-lipgloss.Width(l[i])))
		} else {
			lines[i] = strings.Repeat(" ", width)
		}
		if i < len(r) {
			lines[i] += strings.Repeat(" ", gap) + r[i]
		}
	}
	return strings.Join(lines, "\n")
}

// box is content in a rounded box of width with title in its top border.
func box(title, content string, width int) string {
	inner := width - 4
	fill := strings.Repeat("─", max(1, width-5-lipgloss.Width(title)))
	lines := []string{paint(dim, "╭─ ") + title + " " + paint(dim, fill+"╮")}
	for _, l := range strings.Split(content, "\n") {
		l = ansi.Truncate(l, inner, "…")
		lines = append(lines, paint(dim, "│")+" "+l+strings.Repeat(" ", max(0, inner-lipgloss.Width(l)))+" "+paint(dim, "│"))
	}
	return strings.Join(append(lines, paint(dim, "╰"+strings.Repeat("─", width-2)+"╯")), "\n")
}

// spread is left and, right-aligned in width, right, which gives way first when both do not fit.
func spread(left, right string, width int) string {
	room := width - lipgloss.Width(left) - 1
	if right = ansi.Truncate(right, max(0, room), "…"); right == "" {
		return left
	}
	return left + strings.Repeat(" ", max(1, room+1-lipgloss.Width(right))) + right
}

// atm is the ATM box: the issue and the run's state, then a row for each node and for each proof and command
// of the checks, a connector between nodes when the terminal is tall enough.
func (m *Model) atm(width int) string {
	inner := width - 4
	issue := m.label
	if m.title != "" {
		issue = m.title
		if m.number != 0 {
			issue = fmt.Sprintf("#%d %s", m.number, m.title)
		}
	}
	state := m.icon(m.state) + " " + bold(states[m.state][1], m.state)
	lines := []string{spread(paint(dim, ansi.Truncate(issue, inner-lipgloss.Width(state)-1, "…")), state, inner), ""}
	for i, r := range m.rows {
		if i > 0 && !r.check && m.height >= connectors {
			lines = append(lines, paint(dim, "│"))
		}
		lines = append(lines, m.row(r, inner))
	}
	return box(bold(cyan, "ATM"), strings.Join(lines, "\n"), width)
}

// row is r's line: its icon, its label and how long it took, then, right-aligned, its note and its state.
func (m *Model) row(r *row, width int) string {
	prefix := m.icon(r.state) + " "
	if r.check {
		prefix = "  " + prefix
	}
	took := r.took
	if r.state == "running" && !r.start.IsZero() {
		took = m.now.Sub(r.start)
	}
	suffix := ""
	if took > 0 || r.state == "running" || r.state == "passed" || r.state == "failed" {
		suffix = "  " + paint(dim, run.Human(took))
	}
	word := r.state
	status := bold(states[word][1], word)
	if r.note != "" {
		room := width - lipgloss.Width(prefix) - lipgloss.Width(suffix) - lipgloss.Width(status) - 3
		if room > 0 {
			status = paint(dim, ansi.Truncate(r.note, room, "…")+" · ") + status
		}
	}
	labelWidth := max(0, width-lipgloss.Width(prefix)-lipgloss.Width(suffix)-lipgloss.Width(status)-1)
	left := prefix + ansi.Truncate(r.label, labelWidth, "…") + suffix
	left = ansi.Truncate(left, max(0, width-lipgloss.Width(status)-1), "…")
	return spread(left, status, width)
}

// sidebar says whether there is a box beside the ATM box to show.
func (m *Model) sidebar() bool {
	return m.agentRuns() || len(m.findings) > 0 || m.running() && len(m.output) > 0
}

// right is, at width in height rows, the Agent box while an agent runs, else the Findings box when there are
// findings, then the Log box while a node has output; "" for none.
func (m *Model) right(width, height int) string {
	agent := m.agentRuns()
	// ponytail: the Agent or Findings box gets half the rows and the Log box the rest; fitting each to its
	// content is the upgrade.
	top, half := "", max(3, height/2-2)
	switch {
	case agent:
		top = m.agentBox(width, half)
	case len(m.findings) > 0:
		top = m.findingsBox(width)
	}
	var boxes []string
	if top != "" {
		boxes = append(boxes, top)
	}
	if m.running() && len(m.output) > 0 {
		boxes = append(boxes, m.logBox(width, max(3, height-lipgloss.Height(top)-3)))
	}
	return strings.Join(boxes, "\n\n")
}

func (m *Model) running() bool {
	for _, r := range m.rows {
		if !r.check && r.state == "running" {
			return true
		}
	}
	return false
}

// agentRuns says whether the agent of a unit, or of the slop detector, runs.
func (m *Model) agentRuns() bool {
	for _, step := range []string{"agent", "ponytail"} {
		if r := m.current(step); r != nil && r.state == "running" {
			return true
		}
	}
	return false
}

// agentBox is the agent's last tool calls, each with its result, and its thinking, in at most lines lines.
func (m *Model) agentBox(width, lines int) string {
	var out []string
	for _, a := range m.agent {
		if a.thinking != "" {
			first, _, _ := strings.Cut(strings.TrimSpace(a.thinking), "\n")
			out = append(out, paint(dim, "∴ "+first))
			continue
		}
		state := cmp.Or(a.result, "running")
		out = append(out, m.icon(state)+" "+paint(states[state][1], state)+" "+a.tool+" "+paint(dim, a.detail))
	}
	if len(out) == 0 {
		out = []string{paint(dim, "starting")}
	}
	return box(bold(cyan, "Agent"), strings.Join(out[max(0, len(out)-lines):], "\n"), width)
}

// findingsBox is the findings as no-mistakes has them: a severity icon, a reference, the description below.
func (m *Model) findingsBox(width int) string {
	counts := map[string]int{}
	var out []string
	for i, f := range m.findings {
		counts[f.severity]++
		if i > 0 {
			out = append(out, "")
		}
		s := severities[f.severity]
		out = append(out, "  "+paint(s[1], s[0])+" "+f.ref)
		for _, l := range strings.Split(lipgloss.NewStyle().Width(max(10, width-8)).Render(f.text), "\n") {
			out = append(out, "    "+paint(dim, strings.TrimRight(l, " ")))
		}
	}
	title := bold(cyan, "Findings -")
	for _, sev := range []string{"error", "warning"} {
		if counts[sev] > 0 {
			title += " " + paint(severities[sev][1], fmt.Sprintf("%s %d", severities[sev][0], counts[sev]))
		}
	}
	return box(title, strings.Join(out, "\n"), width)
}

// logBox is the last lines of the running node's output, dim.
func (m *Model) logBox(width, lines int) string {
	out := m.output[max(0, len(m.output)-lines):]
	styled := make([]string, len(out))
	for i, l := range out {
		styled[i] = paint(dim, l)
	}
	return box(bold(cyan, "Log"), strings.Join(styled, "\n"), width)
}
