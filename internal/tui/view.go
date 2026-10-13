package tui

import (
	"cmp"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"

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
	"passed": {"✓", green}, "failed": {"✗", red}, "skipped": {"–", dim}, "not run": {"○", dim}, "unknown": {"?", yellow}}

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
	action := "detach"
	if m.done {
		action = "close"
	}
	return "\n\n  " + lipgloss.NewStyle().Bold(true).Render("q") + " " + paint(dim, action)
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
	if r.hasDuration || r.state == "running" {
		suffix = "  " + paint(dim, run.Human(took))
	}
	word := r.state
	status := bold(states[word][1], word)
	if r.note != "" {
		// The note gives way before the label: it gets only what the label, the duration and the state leave.
		room := width - lipgloss.Width(prefix) - lipgloss.Width(r.label) - lipgloss.Width(suffix) - lipgloss.Width(status) - 4
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
// findings, then the Log box while a node has output; "" for none. The Agent box takes every row the Log box,
// at most a third of them, leaves.
func (m *Model) right(width, height int) string {
	log := m.running() && len(m.output) > 0
	top := ""
	if m.agentRuns() {
		if log {
			logBox := m.logBox(width, min(len(m.output), max(1, height/3-2)))
			top = m.agentBox(width, max(1, height-lipgloss.Height(logBox)-3))
			return strings.Join([]string{top, logBox}, "\n\n")
		}
		return m.agentBox(width, max(1, height-2))
	}
	if len(m.findings) > 0 {
		top = m.findingsBox(width)
	}
	var boxes []string
	if top != "" {
		boxes = append(boxes, top)
	}
	if log {
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

var (
	listItem = regexp.MustCompile(`^( {0,3})([-*+]|\d+[.)])[ \t]+(.*)$`)
)

func markdown(line string) (string, bool, bool) {
	source := []byte(line)
	root := parser.New().Parse(source)
	for block := root.FirstChild(); block != nil; block = block.NextSibling() {
		if _, ok := block.(*ast.CodeBlock); ok {
			return displayText(line), true, true
		}
	}
	var out strings.Builder
	code := false
	var render func(ast.Node, lipgloss.Style)
	render = func(node ast.Node, style lipgloss.Style) {
		switch n := node.(type) {
		case *ast.Emphasis:
			style = style.Italic(true)
		case *ast.Strong:
			style = style.Bold(true)
		case *ast.AutoLink:
			out.WriteString(style.Render(displayText(n.Label.Value(source))))
			return
		case *ast.CodeBlock:
			code = true
			out.WriteString(paint(yellow, displayText(strings.TrimSuffix(n.Value.Str(source), "\n"))))
			return
		case *ast.HTMLBlock:
			out.WriteString(displayText(strings.TrimSuffix(n.Value.Str(source), "\n")))
			return
		case *ast.Text:
			out.WriteString(style.Render(displayText(n.Value.Value(source))))
			return
		case *ast.CodeSpan:
			code = true
			var value strings.Builder
			for _, index := range n.Value.Indices() {
				value.Write(source[index.Start:index.Stop])
			}
			out.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(yellow)).Render(displayText(value.String())))
			return
		case *ast.RawHTML:
			out.WriteString(displayText(n.Value.Value(source)))
			return
		}
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			render(child, style)
		}
	}
	render(root, lipgloss.NewStyle())
	return out.String(), code, false
}

func displayText(value string) string {
	var out strings.Builder
	column := 0
	for _, r := range value {
		if r == '\t' {
			spaces := 8 - column%8
			out.WriteString(strings.Repeat(" ", spaces))
			column += spaces
		} else if !unicode.IsControl(r) {
			out.WriteRune(r)
			column += ansi.StringWidth(string(r))
		}
	}
	return out.String()
}

func fence(line string) (byte, int, string, bool) {
	i := 0
	for i < len(line) && line[i] == ' ' && i < 4 {
		i++
	}
	if i > 3 || i == len(line) || line[i] != '`' && line[i] != '~' {
		return 0, 0, "", false
	}
	marker := line[i]
	start := i
	for i < len(line) && line[i] == marker {
		i++
	}
	if i-start < 3 {
		return 0, 0, "", false
	}
	return marker, i - start, strings.TrimSpace(line[i:]), true
}

func wrapMarkdown(text string, width int) string {
	lines := strings.Split(ansi.Wrap(text, width, ""), "\n")
	plain := ansi.Strip(text)
	offset := 0
	var wrapped []string
	for _, line := range lines {
		visible := ansi.Strip(line)
		start := strings.Index(plain[offset:], visible)
		if start < 0 {
			return ansi.Hardwrap(text, width, true)
		}
		line = plain[offset:offset+start] + line
		offset += start + len(visible)
		if ansi.StringWidth(line) > width {
			wrapped = append(wrapped, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
		} else {
			wrapped = append(wrapped, line)
		}
	}
	if offset < len(plain) {
		wrapped[len(wrapped)-1] += plain[offset:]
		if ansi.StringWidth(wrapped[len(wrapped)-1]) > width {
			last := strings.Split(ansi.Hardwrap(wrapped[len(wrapped)-1], width, true), "\n")
			wrapped = append(wrapped[:len(wrapped)-1], last...)
		}
	}
	return strings.Join(wrapped, "\n")
}

// agentTextRows is the Markdown of text in rows of width, the first behind prefix, the rest indented under
// it: fenced code kept as is, hard wrapped, lists with their continuation under their text. Terminal
// controls are stripped and tabs are expanded before they reach the terminal.
func agentTextRows(prefix, text string, width int) []string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(text))
	w := max(1, width-6)
	var rows []string
	var codeMarker byte
	codeLength := 0
	for _, line := range strings.Split(text, "\n") {
		var wrapped []string
		marker, length, info, isFence := fence(line)
		if codeLength > 0 {
			if isFence && marker == codeMarker && length >= codeLength && info == "" {
				codeMarker, codeLength = 0, 0
				continue
			}
			wrapped = strings.Split(paint(yellow, ansi.Hardwrap(displayText(line), w, true)), "\n")
		} else if isFence {
			codeMarker, codeLength = marker, length
			continue
		} else {
			lineText, hasCode, isBlock := markdown(line)
			if isBlock {
				wrappedText := ansi.Wrap(lineText, w, "")
				if hasCode {
					wrappedText = wrapMarkdown(lineText, w)
				}
				wrapped = strings.Split(wrappedText, "\n")
			} else if item := listItem.FindStringSubmatch(line); item != nil {
				lead := item[1] + cmp.Or(strings.Trim(item[2], "-*+"), "•") + " "
				bodyText, bodyHasCode, _ := markdown(item[3])
				bodyWidth := max(1, w-ansi.StringWidth(lead))
				wrappedText := ansi.Wrap(bodyText, bodyWidth, "")
				if bodyHasCode {
					wrappedText = wrapMarkdown(bodyText, bodyWidth)
				}
				body := strings.Split(wrappedText, "\n")
				wrapped = append([]string{lead + body[0]}, body[1:]...)
				for i := 1; i < len(wrapped); i++ {
					wrapped[i] = strings.Repeat(" ", ansi.StringWidth(lead)) + wrapped[i]
				}
			} else {
				wrappedText := ansi.Wrap(lineText, w, "")
				if hasCode {
					wrappedText = wrapMarkdown(lineText, w)
				}
				wrapped = strings.Split(wrappedText, "\n")
			}
		}
		for _, l := range wrapped {
			if rows == nil {
				rows = append(rows, prefix+l)
			} else {
				rows = append(rows, "  "+l)
			}
		}
	}
	return rows
}

// agentBox is what the agent says and thinks, the tool calls between two of those folded into one dim line
// under them, a failed one on its own row, in lines rows ending with its latest. What it says and thinks wraps,
// its continuation indented under its text.
func (m *Model) agentBox(width, lines int) string {
	var out, names []string
	counts, running := map[string]int{}, false
	runningDetail := ""
	flush := func() {
		if len(names) == 0 {
			return
		}
		n, parts := 0, make([]string, len(names))
		for i, t := range names {
			n, parts[i] = n+counts[t], t
			if counts[t] > 1 {
				parts[i] += fmt.Sprintf(" ×%d", counts[t])
			}
		}
		line := fmt.Sprintf("%d tool%s: %s", n, map[bool]string{true: "s"}[n > 1], strings.Join(parts, ", "))
		if running {
			if runningDetail != "" {
				line += " · " + runningDetail
			}
			out = append(out, "  "+m.icon("running")+" "+paint(dim, line))
		} else {
			out = append(out, "  "+paint(dim, "· "+line))
		}
		names, counts, running, runningDetail = nil, map[string]int{}, false, ""
	}
	for _, a := range m.agent {
		switch {
		case a.says != "" || a.thinking != "":
			flush()
			prefix := map[bool]string{true: "✎ ", false: "∴ "}[a.says != ""]
			out = append(out, agentTextRows(prefix, cmp.Or(a.says, a.thinking), width)...)
		case a.result == "failed":
			flush()
			out = append(out, m.icon("failed")+" "+paint(red, "failed")+" "+paint(dim, a.tool+" "+a.detail))
		default:
			if counts[a.tool]++; counts[a.tool] == 1 {
				names = append(names, a.tool)
			}
			if a.result == "" || a.result == "running" {
				running = true
				runningDetail, _, _ = strings.Cut(a.detail, "\n")
			}
		}
	}
	flush()
	if len(out) == 0 {
		out = []string{paint(dim, "starting")}
	}
	out = append(make([]string, max(0, lines-len(out))), out[max(0, len(out)-lines):]...)
	return box(bold(cyan, "Agent"), strings.Join(out, "\n"), width)
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
