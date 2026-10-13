package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// overflowing is a running agent with 40 proofs under Checks and 60 thoughts: more than either box holds.
func overflowing(width, height int) *Model {
	evs := unit[:7:7]
	for i := range 40 {
		evs = append(evs, fmt.Sprintf(`{"step":"checks","check":"c%02d","state":"passed"}`, i))
	}
	for i := range 60 {
		evs = append(evs, fmt.Sprintf(`{"step":"agent","thinking":"thought %02d"}`, i))
	}
	return screen(width, height, evs...)
}

var proofs, thoughts = regexp.MustCompile(`c\d\d`), regexp.MustCompile(`thought \d\d`)

// shownOf is what of re the screen shows.
func shownOf(m *Model, re *regexp.Regexp) []string { return re.FindAllString(view(m), -1) }

func press(m *Model, keys ...tea.KeyType) {
	for _, k := range keys {
		m.Update(tea.KeyMsg{Type: k})
		view(m)
	}
}

func feed(m *Model, evs ...string) {
	for _, ev := range evs {
		m.Update(run.Frame{Event: []byte(ev)})
	}
}

func assertFitsViewport(t *testing.T, m *Model, width, height int) []string {
	t.Helper()
	s := m.View()
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		t.Fatalf("viewport used %d rows at %dx%d:\n%s", len(lines), width, height, ansi.Strip(s))
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > width {
			t.Fatalf("viewport line exceeds %d columns at %dx%d: %q", width, width, height, ansi.Strip(line))
		}
	}
	return strings.Split(ansi.Strip(s), "\n")
}

func topAgentLine(m *Model) string { return strings.Split(view(m), "\n")[1] }
func topPipelineLine(m *Model) string {
	line := []rune(strings.Split(view(m), "\n")[3])
	return string(line[:min(len(line), m.pipelineBounds.width)])
}

func TestBothBoxesScrollIndependentlyAndFollowOnlyAtTheBottom(t *testing.T) {
	m := overflowing(120, 24)
	atm, agent := shownOf(m, proofs), shownOf(m, thoughts)
	if len(atm) == 0 || len(atm) == 40 || agent[len(agent)-1] != "thought 59" || len(agent) == 60 {
		t.Fatalf("want both boxes overflowing at their latest:\n%s", view(m))
	}
	if !strings.Contains(m.View(), paint(cyan, "╭─ ")+bold(cyan, "Agent*")) {
		t.Fatalf("want the Agent box focused, its border lit:\n%q", m.View())
	}

	// The Agent box, focused: reading older lines, new ones do not move it, nor the ATM box.
	press(m, tea.KeyUp, tea.KeyUp, tea.KeyPgUp)
	held := shownOf(m, thoughts)
	if slices.Contains(held, "thought 59") || !slices.Equal(shownOf(m, proofs), atm) {
		t.Fatalf("up and page up scroll only the Agent box:\n%s", view(m))
	}
	feed(m, `{"step":"agent","thinking":"thought 60"}`)
	if !slices.Equal(shownOf(m, thoughts), held) || !slices.Equal(shownOf(m, proofs), atm) {
		t.Fatalf("new output moved what is being read:\n%s", view(m))
	}
	press(m, tea.KeyEnd)
	feed(m, `{"step":"agent","thinking":"thought 61"}`)
	if got := shownOf(m, thoughts); got[len(got)-1] != "thought 61" || !slices.Equal(shownOf(m, proofs), atm) {
		t.Fatalf("back at the bottom, the Agent box follows, alone:\n%s", view(m))
	}

}

// The ATM box, focused with tab, the same; the Agent box keeps following.
func TestThePipelineScrollsWhenFocusedAndFollowsAtTheBottom(t *testing.T) {
	m := overflowing(120, 24)
	press(m, tea.KeyTab)
	if !strings.Contains(m.View(), paint(cyan, "╭─ ")+bold(cyan, "ATM*")) {
		t.Fatalf("want the ATM box focused, its border lit:\n%q", m.View())
	}
	press(m, tea.KeyHome)
	top := shownOf(m, proofs)
	if top[0] != "c00" {
		t.Fatalf("home goes to the ATM box's first rows:\n%s", view(m))
	}
	feed(m, `{"step":"checks","check":"c40","state":"passed"}`, `{"step":"agent","thinking":"thought 62"}`)
	if got := shownOf(m, thoughts); !slices.Equal(shownOf(m, proofs), top) || got[len(got)-1] != "thought 62" {
		t.Fatalf("the ATM box held, the Agent box following:\n%s", view(m))
	}
	for range 60 {
		press(m, tea.KeyDown)
	}
	feed(m, `{"step":"checks","check":"c41","state":"passed"}`)
	if got := shownOf(m, proofs); got[len(got)-1] != "c41" {
		t.Fatalf("back at the bottom, the ATM box follows:\n%s", view(m))
	}
}

func TestTheWheelScrollsTheBoxUnderThePointer(t *testing.T) {
	m := overflowing(120, 24)
	atm, agent := shownOf(m, proofs), shownOf(m, thoughts)
	m.Update(tea.MouseMsg{X: 5, Y: 10, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if got := shownOf(m, proofs); slices.Equal(got, atm) || !slices.Equal(shownOf(m, thoughts), agent) {
		t.Fatalf("the wheel over the ATM box scrolls it, though the Agent box has focus:\n%s", view(m))
	}
	m.Update(tea.MouseMsg{X: 80, Y: 10, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if slices.Equal(shownOf(m, thoughts), agent) {
		t.Fatalf("the wheel over the Agent box scrolls it:\n%s", view(m))
	}
	m.Update(tea.MouseMsg{X: 80, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if !slices.Equal(shownOf(m, thoughts), agent) {
		t.Fatalf("the wheel back down returns to the latest:\n%s", view(m))
	}
	m = overflowing(80, 40)
	atm, agent = shownOf(m, proofs), shownOf(m, thoughts)
	m.Update(tea.MouseMsg{X: 70, Y: 35, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if !slices.Equal(shownOf(m, proofs), atm) || slices.Equal(shownOf(m, thoughts), agent) {
		t.Fatalf("stacked, the wheel low on the screen scrolls the Agent box:\n%s", view(m))
	}
	before := shownOf(m, thoughts)
	for _, point := range [][2]int{{40, 23}, {40, 39}} {
		m.Update(tea.MouseMsg{X: point[0], Y: point[1], Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	}
	if !slices.Equal(shownOf(m, thoughts), before) {
		t.Fatalf("the gap and footer do not scroll either box:\n%s", view(m))
	}
	m = overflowing(120, 24)
	feed(m, `{"step":"output","output":"log line"}`)
	before = shownOf(m, thoughts)
	m.Update(tea.MouseMsg{X: 80, Y: 19, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if !slices.Equal(shownOf(m, thoughts), before) {
		t.Fatalf("the Log does not scroll Agent history:\n%s", view(m))
	}
}

func TestResizeKeepsBothBoxesInBordersAndTheirReadingPosition(t *testing.T) {
	m := overflowing(120, 24)
	press(m, tea.KeyTab, tea.KeyHome)
	for _, size := range [][2]int{{80, 40}, {60, 30}, {150, 50}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		s := view(m)
		lines := strings.Split(s, "\n")
		if len(lines) > size[1] || shownOf(m, proofs)[0] != "c00" || len(shownOf(m, thoughts)) == 0 {
			t.Fatalf("at %v, want both boxes on screen, the ATM box still at its top:\n%s", size, s)
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > size[0] {
				t.Fatalf("at %v, a line past the screen: %q", size, l)
			}
		}
	}
	// So tall that every row fits: the bottom is reached, the ATM box follows again.
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 120})
	view(m)
	m.Update(tea.WindowSizeMsg{Width: 150, Height: 24})
	feed(m, `{"step":"checks","check":"c40","state":"passed"}`)
	if got := shownOf(m, proofs); got[len(got)-1] != "c40" {
		t.Fatalf("want the ATM box following once it showed its bottom:\n%s", view(m))
	}
}

func TestViewportFitsActualHeightWhenBothBoxesOverflow(t *testing.T) {
	m := overflowing(80, 40)
	s := view(m)
	if got := len(strings.Split(s, "\n")); got > 40 {
		t.Fatalf("80×40 viewport used %d rows:\n%s", got, s)
	}
	if !strings.Contains(s, "q detach") || len(shownOf(m, proofs)) == 0 || len(shownOf(m, thoughts)) == 0 {
		t.Fatalf("both overflowing boxes and the footer must fit:\n%s", s)
	}
}

func TestStackedAgentAndLogFitWithinTerminalHeight(t *testing.T) {
	m := overflowing(80, 24)
	feed(m, `{"step":"output","output":"retained log line"}`)
	s := view(m)
	if got := len(strings.Split(s, "\n")); got > 24 {
		t.Fatalf("80×24 viewport used %d rows:\n%s", got, s)
	}
	if !strings.Contains(s, "q detach") || !strings.Contains(s, "Agent") || !strings.Contains(s, "Log") || !strings.Contains(s, "retained log line") {
		t.Fatalf("both boxes, the Log, and footer must fit:\n%s", s)
	}
	for _, line := range strings.Split(s, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("80×24 viewport has an overwide line: %q", line)
		}
	}

	m = overflowing(80, 16)
	feed(m, `{"step":"output","output":"retained log line"}`)
	if s := view(m); len(strings.Split(s, "\n")) > 16 {
		t.Fatalf("minimum stacked viewport exceeded 16 rows:\n%s", s)
	}
}

func TestStackedPanelsUseRenderedRowsForBoundsAndScrolling(t *testing.T) {
	for _, height := range []int{15, 24} {
		m := overflowing(80, height)
		feed(m, `{"step":"output","output":"retained log line"}`)
		lines := assertFitsViewport(t, m, 80, height)
		agentY := m.historyBounds.y
		logY := agentY + m.historyBounds.height
		if agentY != m.pipelineBounds.height || !strings.Contains(lines[agentY], "Agent*") || !strings.Contains(lines[logY], "Log") {
			t.Fatalf("panel bounds must match their rendered top rows at 80x%d:\n%s", height, strings.Join(lines, "\n"))
		}
		before := shownOf(m, thoughts)
		pipeline := shownOf(m, proofs)
		for _, y := range []int{agentY, agentY + m.historyBounds.height - 1} {
			m.Update(tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
		}
		if slices.Equal(shownOf(m, thoughts), before) || !slices.Equal(shownOf(m, proofs), pipeline) {
			t.Fatalf("wheel on Agent border rows must scroll Agent at 80x%d:\n%s", height, view(m))
		}
		held := shownOf(m, thoughts)
		for _, y := range []int{logY, height - 1} {
			m.Update(tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
		}
		if !slices.Equal(shownOf(m, thoughts), held) || !slices.Equal(shownOf(m, proofs), pipeline) {
			t.Fatalf("wheel on Log or footer must not scroll Agent at 80x%d:\n%s", height, view(m))
		}
	}
}

func TestStackedFindingsAndLogUseDistinctRenderedRows(t *testing.T) {
	for _, width := range []int{80, 120} {
		for _, height := range []int{15, 24} {
			m := screen(width, height, unit...)
			m.findings = []finding{{severity: "error", ref: "view.go:1", text: "retained finding"}}
			feed(m, `{"step":"agent","state":"passed"}`, `{"step":"output","output":"retained log line"}`)
			lines := assertFitsViewport(t, m, width, height)
			findingsY, logY := -1, -1
			for i, line := range lines {
				if strings.Contains(line, "Findings -") {
					findingsY = i
				}
				if strings.Contains(line, "Log") {
					logY = i
				}
			}
			if findingsY < 0 || logY <= findingsY || strings.Contains(lines[findingsY], "Log") {
				t.Fatalf("Findings and Log must occupy distinct rows at %dx%d:\n%s", width, height, strings.Join(lines, "\n"))
			}
		}
	}
}

func TestTwoColumnAgentAndLogFitShortTerminal(t *testing.T) {
	m := overflowing(120, 9)
	feed(m, `{"step":"output","output":"retained log line"}`)
	lines := assertFitsViewport(t, m, 120, 9)
	s := strings.Join(lines, "\n")
	if !strings.Contains(s, "q detach") || !strings.Contains(s, "ATM") || !strings.Contains(s, "Agent") || !strings.Contains(s, "Log") {
		t.Fatalf("both panels, the Log, and footer must fit:\n%s", s)
	}
}

func TestTabDoesNotLatchFocusBeforeAgentStarts(t *testing.T) {
	m := screen(120, 24, unit...)
	if m.agentRuns() {
		t.Fatal("fixture must have no running Agent history")
	}
	press(m, tea.KeyTab)
	feed(m, `{"step":"agent","state":"started"}`)
	s := m.View()
	if !strings.Contains(s, paint(cyan, "╭─ ")+bold(cyan, "Agent*")) || strings.Contains(s, bold(cyan, "ATM*")) {
		t.Fatalf("new Agent history must get the default focus:\n%s", s)
	}
	press(m, tea.KeyTab)
	if !strings.Contains(m.View(), bold(cyan, "ATM*")) {
		t.Fatalf("Tab must still switch focus while Agent history is available:\n%s", m.View())
	}
}

func TestNewAgentAndPonytailInvocationsRestoreAgentFocus(t *testing.T) {
	m := overflowing(120, 24)
	press(m, tea.KeyTab)
	feed(m, `{"step":"agent","thinking":"same invocation"}`)
	if !strings.Contains(m.View(), bold(cyan, "ATM*")) {
		t.Fatalf("new Agent activity must preserve the user's focus choice:\n%s", m.View())
	}
	feed(m, `{"step":"agent","state":"passed"}`, `{"step":"agent","state":"started"}`)
	if !strings.Contains(m.View(), bold(cyan, "Agent*")) || strings.Contains(m.View(), bold(cyan, "ATM*")) {
		t.Fatalf("a successive Agent invocation must restore Agent focus:\n%s", m.View())
	}
	press(m, tea.KeyTab)
	feed(m, `{"step":"agent","state":"passed"}`, `{"step":"ponytail","state":"started"}`)
	if !strings.Contains(m.View(), bold(cyan, "Agent*")) || strings.Contains(m.View(), bold(cyan, "ATM*")) {
		t.Fatalf("a new Ponytail invocation must restore Agent focus:\n%s", m.View())
	}
}

func TestHeldRowsStayAnchoredAcrossConnectorAndWrapReflow(t *testing.T) {
	m := overflowing(120, 24)
	press(m, tea.KeyTab, tea.KeyHome)
	for range 4 {
		press(m, tea.KeyDown)
	}
	before := strings.Split(view(m), "\n")
	if len(before) < 4 || !strings.Contains(before[3], "Checks") {
		t.Fatalf("fixture must hold the Checks row before connectors appear:\n%s", strings.Join(before, "\n"))
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	after := strings.Split(view(m), "\n")
	if len(after) < 4 || !strings.Contains(after[3], "Checks") {
		t.Fatalf("connector insertion moved the selected ATM row:\n%s", strings.Join(after, "\n"))
	}

	m = New("issue-1", 120, 24)
	feed(m, `{"step":"agent","state":"started"}`)
	for i := range 5 {
		feed(m, fmt.Sprintf(`{"step":"agent","thinking":"preceding message %02d %s"}`, i, strings.Repeat("x", 45)))
	}
	feed(m, `{"step":"agent","thinking":"anchor identity marker"}`)
	for i := range 40 {
		feed(m, fmt.Sprintf(`{"step":"agent","thinking":"later message %02d"}`, i))
	}
	view(m)
	press(m, tea.KeyHome)
	for range 5 {
		press(m, tea.KeyDown)
	}
	if got := view(m); !strings.Contains(got, "anchor identity marker") {
		t.Fatalf("fixture must select the anchored Agent message:\n%s", got)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if got := view(m); !strings.Contains(got, "anchor identity marker") {
		t.Fatalf("wrapping moved the selected Agent message:\n%s", got)
	}
}

func TestHeldTopTracksSecondToolGroupAfterNewOutput(t *testing.T) {
	m := New("issue-1", 120, 24)
	feed(m, `{"step":"agent","state":"started"}`,
		`{"step":"agent","id":"t1","tool":"first-tool","result":"passed"}`,
		`{"step":"agent","thinking":"group separator"}`,
		`{"step":"agent","id":"t2","tool":"second-tool","result":"passed"}`)
	for i := range 40 {
		feed(m, fmt.Sprintf(`{"step":"agent","thinking":"later %02d"}`, i))
	}
	view(m)
	press(m, tea.KeyHome, tea.KeyDown, tea.KeyDown)
	if line := topAgentLine(m); !strings.Contains(line, "second-tool") {
		t.Fatalf("the selected top row must be the second tool group: %q", line)
	}
	feed(m, `{"step":"agent","thinking":"new output"}`)
	if line := topAgentLine(m); !strings.Contains(line, "second-tool") || strings.Contains(line, "first-tool") {
		t.Fatalf("new output moved the selected second tool group: %q", line)
	}
}

func TestHeldWrappedPartClampsToItsMessageWhenWidened(t *testing.T) {
	m := New("issue-1", 120, 24)
	feed(m, `{"step":"agent","state":"started"}`, `{"step":"agent","thinking":"WRAP-ANCHOR `+strings.Repeat("word ", 18)+`"}`)
	for i := range 30 {
		feed(m, fmt.Sprintf(`{"step":"agent","thinking":"later %02d"}`, i))
	}
	view(m)
	press(m, tea.KeyHome, tea.KeyDown)
	if line := topAgentLine(m); strings.Contains(line, "WRAP-ANCHOR") {
		t.Fatalf("the fixture must select a continuation row: %q", line)
	}
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 24})
	if line := topAgentLine(m); !strings.Contains(line, "WRAP-ANCHOR") {
		t.Fatalf("widening must keep the same message at the top: %q", line)
	}
}

func TestHeldConnectorMapsToItsRowWhenConnectorsDisappear(t *testing.T) {
	m := overflowing(120, 30)
	view(m)
	press(m, tea.KeyTab, tea.KeyHome)
	for range 7 {
		press(m, tea.KeyDown)
	}
	if line := topPipelineLine(m); strings.Trim(line, "│ ") != "" {
		t.Fatalf("the fixture must select the Checks connector: %q", line)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 29})
	if line := topPipelineLine(m); !strings.Contains(line, "Checks") {
		t.Fatalf("when its connector disappears, the Checks row must stay at the top: %q", line)
	}
	feed(m, `{"step":"checks","check":"new check","state":"passed"}`)
	if line := topPipelineLine(m); !strings.Contains(line, "Checks") {
		t.Fatalf("new rows moved the surviving Checks row: %q", line)
	}
}

func TestACompletedRunScrollsItsPipelineAndQStillCloses(t *testing.T) {
	m := overflowing(120, 24)
	feed(m, `{"step":"agent","state":"passed"}`)
	m.end(run.Outcome{Outcome: "passed"}, nil)
	before := shownOf(m, proofs)
	press(m, tea.KeyPgUp)
	if slices.Equal(shownOf(m, proofs), before) || !strings.Contains(view(m), "q close") {
		t.Fatalf("a completed run's pipeline scrolls with its keys:\n%s", view(m))
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); cmd == nil || m.detached {
		t.Fatal("q no longer closes a completed run")
	}
}
