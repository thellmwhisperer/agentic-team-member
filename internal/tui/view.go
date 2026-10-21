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
	"github.com/yuin/goldmark/v2/extension"
	tableast "github.com/yuin/goldmark/v2/extension/ast"
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

// fold is the arrow that marks a row a click folds or unfolds, underlined like a link: ▸ folded, ▾ unfolded.
func fold(open bool) string {
	return "\x1b[4m" + map[bool]string{true: "▾", false: "▸"}[open] + "\x1b[0m"
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

// severities is each finding severity's icon and colour, shown on the screen.
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
	available := max(0, m.height-lipgloss.Height(m.footer()))
	if width < twoColumns || !m.sidebar() {
		atmHeight, rightHeight := available, 0
		if m.sidebar() {
			atmHeight, rightHeight = stackedHeights(available, m.rightMinimum(width))
		}
		atmBox := m.atm(width, atmHeight)
		m.pipelineBounds = bounds{width: width, height: lipgloss.Height(atmBox)}
		m.historyBounds = bounds{width: width}
		m.historyHeight = 0
		panels := []string{atmBox}
		if rightBox := m.right(width, rightHeight); rightBox != "" {
			m.historyBounds.y = lipgloss.Height(atmBox)
			m.historyBounds.height = m.historyHeight
			m.logBounds.y, m.logBounds.width = m.historyBounds.y+lipgloss.Height(rightBox)-m.logBounds.height, width
			panels = append(panels, rightBox)
		}
		return strings.Join(panels, "\n") + m.footer()
	}
	left := min(max(width/3, leftMin), leftMax)
	rightWidth := width - left - gap
	m.historyBounds = bounds{x: left + gap, width: rightWidth}
	m.pipelineBounds = bounds{width: left}
	m.historyHeight = 0
	leftBox := m.atm(left, available)
	rightBox := m.right(rightWidth, available)
	m.pipelineBounds.height = lipgloss.Height(leftBox)
	m.historyBounds.height = m.historyHeight
	m.logBounds.x, m.logBounds.y, m.logBounds.width = left+gap, lipgloss.Height(rightBox)-m.logBounds.height, rightWidth
	return columns(leftBox, rightBox, left) + m.footer()
}

const (
	boxBorderRows       = 2
	minimumBoxRows      = boxBorderRows + 1
	minimumPipelineRows = minimumBoxRows + 2
)

func stackedHeights(available, rightMinimum int) (int, int) {
	right := max(rightMinimum, available/3)
	atm := available - right
	if atm < minimumPipelineRows {
		atm = minimumPipelineRows
		right = available - atm
	}
	return max(1, atm), max(1, right)
}

func (m *Model) rightMinimum(width int) int {
	if m.agentVisible() {
		if len(m.output) > 0 || !m.agentRuns() && m.running() {
			return minimumBoxRows * 2
		}
		return minimumBoxRows
	}
	top := 0
	if len(m.findings) > 0 {
		top = lipgloss.Height(m.findingsBox(width))
	}
	if m.running() {
		return top + minimumBoxRows
	}
	return top
}

func (m *Model) footer() string {
	action := "detach"
	if m.done {
		action = "close"
	}
	gap := "\n\n  "
	if m.height < 10 {
		gap = "\n  "
	}
	if m.typing {
		prompt := map[bool]string{true: "answer", false: "fix"}[m.question() != ""] + " › "
		text := ansi.TruncateLeft(m.text, max(0, ansi.StringWidth(m.text)-(m.width-2-ansi.StringWidth(prompt)-1)), "")
		return gap + prompt + text + "█"
	}
	keys := lipgloss.NewStyle().Bold(true).Render("q") + " " + paint(dim, action)
	if m.agentRuns() {
		keys += "  " + lipgloss.NewStyle().Bold(true).Render("t") + " " +
			paint(dim, map[bool]string{true: "hide", false: "show"}[m.unfold]+" tool use")
		keys += "  " + lipgloss.NewStyle().Bold(true).Render("h") + " " +
			paint(dim, map[bool]string{true: "hide", false: "show"}[m.unfoldThink]+" thinking")
	}
	return gap + keys + m.gateKeys()
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

// box is content in a rounded box of width with title in its top border, its border in colour border.
func box(title, content string, width int, border string) string {
	inner := width - 4
	fill := strings.Repeat("─", max(1, width-5-lipgloss.Width(title)))
	lines := []string{paint(border, "╭─ ") + title + " " + paint(border, fill+"╮")}
	for _, l := range strings.Split(content, "\n") {
		l = ansi.Truncate(l, inner, "…")
		side := paint(border, "│")
		lines = append(lines, side+" "+l+strings.Repeat(" ", max(0, inner-lipgloss.Width(l)))+" "+side)
	}
	return strings.Join(append(lines, paint(border, "╰"+strings.Repeat("─", width-2)+"╯")), "\n")
}

// border is the colour of the border of the box of pane p: lit when keys scroll it.
func (m *Model) border(p *pane) string {
	return map[bool]string{true: cyan, false: dim}[m.focused() == p]
}

// spread is left and, right-aligned in width, right, which gives way first when both do not fit.
func spread(left, right string, width int) string {
	room := width - lipgloss.Width(left) - 1
	if right = ansi.Truncate(right, max(0, room), "…"); right == "" {
		return left
	}
	return left + strings.Repeat(" ", max(1, room+1-lipgloss.Width(right))) + right
}

// atm is the ATM box, height rows at most: the issue and the run's state, then a row for each node and for
// each Checks or Delivery part, a connector between nodes when the terminal is tall enough, the rows
// from its reading position.
func (m *Model) atm(width, height int) string {
	inner := width - 4
	issue := m.label
	if m.title != "" {
		issue = m.title
		if m.number != 0 {
			issue = fmt.Sprintf("#%d %s", m.number, m.title)
		}
	}
	state := m.icon(m.state) + " " + bold(states[m.state][1], m.state)
	// The title wraps beside the state: three lines and a hint until i shows them all.
	head := strings.Split(ansi.Wrap(issue, max(1, inner-lipgloss.Width(state)-1), ""), "\n")
	for i, l := range head {
		head[i] = paint(dim, l)
	}
	if len(head) > 3 {
		i := lipgloss.NewStyle().Bold(true).Render("i")
		if m.unfoldTitle {
			head = append(head, i+" "+paint(dim, "fold title"))
		} else {
			head = append(head[:3], paint(dim, "… ")+i+" "+paint(dim, "unfold title"))
		}
	}
	head[0] = spread(head[0], state, inner)
	// The title stays above the rows when it leaves them a line; else only its first line stays and the rest
	// scroll above the rows, so a long title never makes the box taller.
	lines, rows := append(head, ""), []paneLine(nil)
	if len(lines)+1 > height-boxBorderRows {
		lines = lines[:1]
		for i, l := range append(head[1:], "") {
			rows = append(rows, paneLine{key: fmt.Sprintf("title:%d", i), text: l})
		}
	}
	for i, r := range m.rows {
		if i > 0 && !r.check && m.height >= connectors {
			key := fmt.Sprintf("row:%p", r)
			rows = append(rows, paneLine{key: "connector:" + key, parent: key, text: paint(dim, "│")})
		}
		shown := *r
		if r.step == "agent" && !r.check {
			shown.label = "Agent"
		}
		rows = append(rows, paneLine{key: fmt.Sprintf("row:%p", r), text: m.row(&shown, inner)})
	}
	lines = append(lines, m.pipeline.show(rows, max(1, height-2-len(lines)))...)
	title := "ATM"
	if m.focused() == &m.pipeline {
		title += "*"
	}
	return box(bold(cyan, title), strings.Join(lines, "\n"), width, m.border(&m.pipeline))
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
	return m.agentVisible() || len(m.findings) > 0 || m.running() || m.gateSteps != nil
}

// right is, at width in height rows, the Gate box once the gate after Delivery shows its run, else the Agent
// box while an agent runs, else the Findings box when there are
// findings, then the Log box while a node runs; "" for none. The Agent box takes every row the Log box, shown
// with the agent's output, at most a third of them, leaves. The Log box is the last box.
func (m *Model) right(width, height int) string {
	m.logBounds = bounds{}
	top := ""
	if m.gateSteps != nil {
		return m.gateBox(width, max(1, height-boxBorderRows))
	}
	if m.agentVisible() {
		if len(m.output) > 0 || !m.agentRuns() && m.running() {
			logHeight := min(height/3-boxBorderRows, height-minimumBoxRows-boxBorderRows)
			if !m.agentRuns() {
				logHeight = height - minimumBoxRows*2 - boxBorderRows
			}
			logBox := m.logBox(width, max(1, logHeight))
			top = m.agentBox(width, max(1, height-lipgloss.Height(logBox)-boxBorderRows))
			return strings.Join([]string{top, logBox}, "\n")
		}
		return m.agentBox(width, max(1, height-boxBorderRows))
	}
	if len(m.findings) > 0 {
		top = m.findingsBox(width)
	}
	var boxes []string
	if top != "" {
		boxes = append(boxes, top)
	}
	if m.running() {
		boxes = append(boxes, m.logBox(width, max(1, height-lipgloss.Height(top)-boxBorderRows)))
	}
	return strings.Join(boxes, "\n")
}

func (m *Model) running() bool { return m.runningRow() != nil }

// runningRow is the row of the node that runs, nil when none does.
func (m *Model) runningRow() *row {
	for _, r := range m.rows {
		if !r.check && r.state == "running" {
			return r
		}
	}
	return nil
}

func (m *Model) agentVisible() bool {
	return m.agentRuns() || len(m.findings) == 0 && len(m.agent) > 0 && m.agent[len(m.agent)-1].report != nil
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

func markdownText(source []byte, root ast.Node) (string, bool) {
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
			if n.Value.IsOwned() {
				value.WriteString(n.Value.Str(source))
			} else {
				for _, index := range n.Value.Indices() {
					value.Write(source[index.Start:index.Stop])
				}
			}
			out.WriteString(paint(yellow, displayText(value.String())))
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
	return out.String(), code
}

func markdown(line string) (string, bool, bool) {
	source := []byte(line)
	root := parser.New().Parse(source)
	for block := root.FirstChild(); block != nil; block = block.NextSibling() {
		if _, ok := block.(*ast.CodeBlock); ok {
			return displayText(line), true, true
		}
	}
	text, code := markdownText(source, root)
	return text, code, false
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

func plainRows(text string, width int, code bool) []string {
	if code {
		text = wrapMarkdown(text, width)
	} else {
		text = ansi.Wrap(text, width, "")
	}
	return strings.Split(text, "\n")
}

func listRows(line string, width int) ([]string, bool) {
	item := listItem.FindStringSubmatch(line)
	if item == nil {
		return nil, false
	}
	lead := item[1] + cmp.Or(strings.Trim(item[2], "-*+"), "•") + " "
	bodyText, bodyHasCode, _ := markdown(item[3])
	bodyWidth := max(1, width-ansi.StringWidth(lead))
	body := plainRows(bodyText, bodyWidth, bodyHasCode)
	wrapped := append([]string{lead + body[0]}, body[1:]...)
	for i := 1; i < len(wrapped); i++ {
		wrapped[i] = strings.Repeat(" ", ansi.StringWidth(lead)) + wrapped[i]
	}
	return wrapped, true
}

// agentTextRows is the Markdown of text in rows of width, the first behind prefix, the rest indented under
// it: fenced code kept as is, hard wrapped, lists with their continuation under their text. Terminal
// controls are stripped and tabs are expanded before they reach the terminal.
func agentTextRows(prefix, text string, width int, code codeFence) ([]string, []int, codeFence) {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(text))
	w := max(1, width-6)
	var rows []string
	var sources []int
	for source, line := range strings.Split(text, "\n") {
		var wrapped []string
		marker, length, info, isFence := fence(line)
		if code.length > 0 {
			if isFence && marker == code.marker && length >= code.length && info == "" {
				code = codeFence{}
				continue
			}
			wrapped = strings.Split(paint(yellow, ansi.Hardwrap(displayText(line), w, true)), "\n")
		} else if isFence {
			code = codeFence{marker, length}
			continue
		} else {
			lineText, hasCode, isBlock := markdown(line)
			if isBlock {
				wrapped = plainRows(lineText, w, hasCode)
			} else if list, ok := listRows(line, w); ok {
				wrapped = list
			} else {
				wrapped = plainRows(lineText, w, hasCode)
			}
		}
		for _, l := range wrapped {
			sources = append(sources, source)
			if rows == nil {
				rows = append(rows, prefix+l)
			} else {
				rows = append(rows, "  "+l)
			}
		}
	}
	return rows, sources, code
}

// agentBox is what the agent says and thinks, and the tool calls between two of those, in lines rows ending with
// its latest. What it says wraps in full; what it thinks and the tool calls fold to one row until opened.
func (m *Model) agentBox(width, lines int) string {
	var out []paneLine
	var calls []*activity
	for _, a := range m.agent {
		if a.says == "" && a.thinking == "" && a.report == nil {
			calls = append(calls, a)
			continue
		}
		out, calls = m.toolLines(out, calls), nil
		if a.says != "" {
			texts, _, _ := agentTextRows("✎ ", a.says, width, codeFence{})
			for _, text := range texts {
				out = append(out, paneLine{key: fmt.Sprintf("activity:%p", a), text: text})
			}
		}
		if a.report != nil {
			out = m.reportLines(out, a, width)
		}
		if a.thinking == "" {
			continue
		}
		// Thinking folds like a run of tool calls: one row, its first line, until clicked or h opens it.
		key := fmt.Sprintf("activity:%p", a)
		if !m.unfolded(key) {
			out = append(out, paneLine{key: key, toggle: key, text: fold(false) + " ∴ " + firstMarkdown(a.thinking)})
			continue
		}
		texts, _, _ := agentTextRows(fold(true)+" ∴ ", a.thinking, width-2, codeFence{})
		for i, text := range texts {
			line := paneLine{key: key, text: text}
			if i == 0 {
				line.toggle = key
			}
			out = append(out, line)
		}
	}
	out = m.toolLines(out, calls)
	if len(out) == 0 {
		out = []paneLine{{key: "starting", text: paint(dim, "starting")}}
	}
	shown := m.history.show(out, lines)
	pad := max(0, lines-len(shown))
	shown = append(make([]string, pad), shown...)
	m.history.toggles = append(make([]string, pad), m.history.toggles...)
	title := "Agent" + m.agentLabel
	if m.focused() == &m.history {
		title += "*"
	}
	result := box(bold(cyan, ansi.Truncate(title, width-6, "…")), strings.Join(shown, "\n"), width, m.border(&m.history))
	m.historyHeight = lipgloss.Height(result)
	return result
}

// reportLines is out with the report of a: its Informe row, then, until a click folds it, its summary's Markdown
// and a folded row for its changed files, its commands, its test and its findings, each item a row once unfolded.
func (m *Model) reportLines(out []paneLine, a *activity, width int) []paneLine {
	r, key := a.report, fmt.Sprintf("report:%p", a)
	open := m.unfolded(key)
	out = append(out, paneLine{key: key, toggle: key, text: fold(open) + " Informe"})
	if !open {
		return out
	}
	for _, text := range summaryRows(r.Summary, width) {
		out = append(out, paneLine{key: key + ":summary", parent: key, text: text})
	}
	lines := func(items []string) (rows []string) {
		for _, item := range items {
			rows = append(rows, oneLine(item))
		}
		return rows
	}
	var findings []string
	for _, f := range r.Findings {
		severity := f.Severity
		findings = append(findings, paint(severities[severity][1], oneLine(severity))+" "+
			oneLine(f.File)+" "+paint(dim, oneLine(f.Description)))
	}
	for i, part := range []struct {
		title string
		rows  []string
	}{
		{fmt.Sprintf("Ficheros cambiados (%d)", len(r.ChangedFiles)), lines(r.ChangedFiles)},
		{fmt.Sprintf("Comandos ejecutados (%d)", len(r.CommandsRun)), lines(r.CommandsRun)},
		{"Test " + oneLine(r.TestFile), lines([]string{r.TestFile})},
		{fmt.Sprintf("Hallazgos (%d)", len(r.Findings)), findings},
	} {
		k := fmt.Sprintf("%s:%d", key, i)
		open := m.unfolded(k)
		out = append(out, paneLine{key: k, parent: key, toggle: k, text: "  " + fold(open) + " " + part.title})
		for _, row := range part.rows {
			if open {
				out = append(out, paneLine{key: k + ":" + row, parent: k, text: "      " + row})
			}
		}
	}
	return out
}

func summaryRows(text string, width int) []string {
	source := []byte(text)
	root := parser.New(parser.WithExtensions(extension.TableParser)).Parse(source)
	var rows []string
	start := 0
	prose := func(end int) {
		if end > start {
			lines, _, _ := agentTextRows("  ", text[start:end], width, codeFence{})
			rows = append(rows, lines...)
		}
	}
	for block := root.FirstChild(); block != nil; block = block.NextSibling() {
		if _, ok := block.(*tableast.Table); !ok {
			continue
		}
		header := block.FirstChild()
		prose(header.Pos())
		var cells [][]string
		var widths []int
		add := func(row ast.Node) {
			var values []string
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				value, _ := markdownText(source, cell)
				j := len(values)
				values = append(values, value)
				if j == len(widths) {
					widths = append(widths, 0)
				}
				widths[j] = max(widths[j], ansi.StringWidth(value))
			}
			cells = append(cells, values)
		}
		add(header)
		start = header.Pos()
		for range 2 {
			start += len(strings.SplitN(text[start:], "\n", 2)[0])
			if start < len(text) {
				start++
			}
		}
		if body := header.NextSibling(); body != nil {
			for row := body.FirstChild(); row != nil; row = row.NextSibling() {
				add(row)
				start = row.Pos() + len(strings.SplitN(text[row.Pos():], "\n", 2)[0])
			}
		}
		rows = append(rows, summaryTableRows(cells, widths)...)
	}
	prose(len(text))
	return rows
}

func summaryTableRows(cells [][]string, widths []int) []string {
	var rows []string
	for _, cells := range cells {
		var row string
		for j, cell := range cells {
			row += cell + strings.Repeat(" ", widths[j]-ansi.StringWidth(cell)+2)
		}
		rows = append(rows, "  "+strings.TrimRight(row, " "))
	}
	return rows
}

func (m *Model) toolLines(out []paneLine, calls []*activity) []paneLine {
	start := 0
	for i, a := range calls {
		if a.result == "failed" {
			out = appendToolGroup(m, out, calls[start:i])
			out = append(out, m.callLine(a, ""))
			start = i + 1
		}
	}
	return appendToolGroup(m, out, calls[start:])
}

func appendToolGroup(m *Model, out []paneLine, calls []*activity) []paneLine {
	if len(calls) == 0 {
		return out
	}
	var names []string
	counts, running, runningDetail := map[string]int{}, false, ""
	for _, a := range calls {
		if counts[a.tool]++; counts[a.tool] == 1 {
			names = append(names, a.tool)
		}
		if a.result == "" || a.result == "running" {
			running = true
			runningDetail, _, _ = strings.Cut(a.detail, "\n")
		}
	}
	parts := make([]string, len(names))
	for i, tool := range names {
		parts[i] = tool
		if counts[tool] > 1 {
			parts[i] += fmt.Sprintf(" ×%d", counts[tool])
		}
	}
	n := len(calls)
	line := fmt.Sprintf("%d tool%s: %s", n, map[bool]string{true: "s"}[n > 1], strings.Join(parts, ", "))
	key := fmt.Sprintf("tools:%p", calls[0])
	unfolded := m.unfolded(key)
	head := fold(unfolded) + " "
	if running {
		if !unfolded && runningDetail != "" {
			line += " · " + runningDetail
		}
		out = append(out, paneLine{key: key, toggle: key,
			text: head + m.icon("running") + " " + paint(dim, line)})
	} else {
		out = append(out, paneLine{key: key, toggle: key, text: head + paint(dim, "· "+line)})
	}
	if unfolded {
		for _, a := range calls {
			out = append(out, m.callLine(a, key))
		}
	}
	return out
}

// callLine is call a on one row: its state's icon and word, its tool, its detail and, after " · ", the first line
// of its output, without terminal controls.
func (m *Model) callLine(a *activity, run string) paneLine {
	state := cmp.Or(map[string]string{"passed": "passed", "failed": "failed"}[a.result], "running")
	word := cmp.Or(map[string]string{"passed": "ok"}[state], state)
	line := a.tool + " " + oneLine(a.detail)
	if summary := oneLine(a.summary); summary != "" {
		line += " · " + summary
	}
	return paneLine{key: fmt.Sprintf("activity:%p", a), parent: run,
		text: "    " + m.icon(state) + " " + paint(states[state][1], word) + " " + paint(dim, line)}
}

// oneLine is the first line of s, without terminal controls.
func oneLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return displayText(ansi.Strip(line))
}

// firstMarkdown is the first line of s, its Markdown rendered.
func firstMarkdown(s string) string {
	text, _, _ := markdown(oneLine(s))
	return text
}

// findingsBox is the findings shown on the screen: a severity icon, a reference, the description below.
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
	return box(title, strings.Join(out, "\n"), width, dim)
}

// logRows is the running node's output: for each command, a header row with its row in the ATM box, then, unfolded,
// its lines in Markdown as the Agent box has them; until it has some, the node's name and how long it has run.
func (m *Model) logRows(width int) []paneLine {
	if len(m.output) == 0 {
		r := m.runningRow()
		return []paneLine{{key: "starting", text: paint(dim, r.label+" · "+run.Human(m.now.Sub(r.start)))}}
	}
	var out []paneLine
	for i := 0; i < len(m.output); {
		o, lines, keys := m.output[i], []string(nil), []string(nil)
		for ; i < len(m.output) && m.output[i].step == o.step && m.output[i].check == o.check; i++ {
			for j, text := range strings.Split(m.output[i].text, "\n") {
				lines = append(lines, text)
				keys = append(keys, fmt.Sprintf("log:%p:%d", m.output[i], j))
			}
		}
		texts, sources, _ := agentTextRows("  ", strings.Join(lines, "\n"), width, o.code)
		key := "log:" + o.step + "/" + o.check
		if r := m.command(o.step, o.check); r != nil {
			header, open := *r, m.unfolded(key)
			header.check = false
			if !open && len(texts) > 0 && len(texts[0]) > 2 {
				header.label += " · " + strings.TrimPrefix(texts[0], "  ")
			}
			out = append(out, paneLine{key: key, toggle: key, text: fold(open) + " " + m.row(&header, width-6)})
			if !open {
				continue
			}
		}
		for j, text := range texts {
			out = append(out, paneLine{key: keys[sources[j]], parent: key, text: text})
		}
	}
	return out
}

// logBox is the Log box, at most lines rows of logRows from its reading position.
func (m *Model) logBox(width, lines int) string {
	rows := m.logRows(width)
	title := "Log"
	if m.focused() == &m.log {
		title += "*"
	}
	shown := m.log.show(rows, min(len(rows), lines))
	result := box(bold(cyan, title), strings.Join(shown, "\n"), width, m.border(&m.log))
	m.logBounds.height = lipgloss.Height(result)
	return result
}
