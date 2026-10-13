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

func TestBothBoxesScrollIndependentlyAndFollowOnlyAtTheBottom(t *testing.T) {
	m := overflowing(120, 24)
	atm, agent := shownOf(m, proofs), shownOf(m, thoughts)
	if len(atm) == 0 || len(atm) == 40 || agent[len(agent)-1] != "thought 59" || len(agent) == 60 {
		t.Fatalf("want both boxes overflowing at their latest:\n%s", view(m))
	}
	if !strings.Contains(m.View(), paint(cyan, "╭─ ")+bold(cyan, "Agent")) {
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
	if !strings.Contains(m.View(), paint(cyan, "╭─ ")+bold(cyan, "ATM")) {
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
	// Stacked, the ATM box is above the Agent box.
	m = overflowing(80, 40)
	atm, agent = shownOf(m, proofs), shownOf(m, thoughts)
	m.Update(tea.MouseMsg{X: 70, Y: 35, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if !slices.Equal(shownOf(m, proofs), atm) || slices.Equal(shownOf(m, thoughts), agent) {
		t.Fatalf("stacked, the wheel low on the screen scrolls the Agent box:\n%s", view(m))
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
