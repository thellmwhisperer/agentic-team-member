package tui

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func eventGolden(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("../run/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func modelWithEvents(width int, events []string) *Model {
	m := New("issue-1", width, 40)
	for _, event := range append([]string{`{"step":"agent","state":"started"}`}, events...) {
		m.apply(run.Frame{Event: []byte(event)})
	}
	return m
}

func agentLines(m *Model, width int) []string {
	var lines []string
	for _, line := range strings.Split(ansi.Strip(m.agentBox(width, 40)), "\n") {
		if strings.HasPrefix(line, "│") {
			lines = append(lines, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│")))
		}
	}
	return lines
}

func checkAgentBox(t *testing.T, events, want []string, tools int) {
	t.Helper()
	m := modelWithEvents(240, events)
	if got := agentLines(m, 240); !reflect.DeepEqual(got, want) {
		t.Fatalf("Agent box lines:\n%q\nwant:\n%q", got, want)
	}
	m.apply(run.Frame{Event: []byte(`{"step":"agent","state":"passed","unit":1,"duration_ms":1000}`)})
	if got := view(m); !strings.Contains(got, "Agent · unit 1") ||
		!strings.Contains(got, fmt.Sprintf("%d tools · passed", tools)) {
		t.Fatalf("Agent tool count note:\n%s", got)
	}
}

func TestAgentBoxRendersRunParserEventGoldens(t *testing.T) {
	checkAgentBox(t, eventGolden(t, "pi-6-2.events.jsonl"), []string{
		"✎ I'll start by reading the project instructions and the ponytail skill, then look at the analytics package.",
		"· 1 tool: read",
		"∴ Exploring the packages directory, particularly the analytics package, to understand existing patterns.",
		"⠋ 1 tool: bash · ls packages",
	}, 2)
	checkAgentBox(t, eventGolden(t, "claude-261.events.jsonl"), []string{
		"✎ Writing the red tests first, then the fix in `reset`.",
		"· 1 tool: Edit",
	}, 1)
}

func TestAgentBoxRendersTheSameStoryForBothHarnesses(t *testing.T) {
	want := []string{
		"∴ Exploring the packages directory, particularly the analytics package, to understand existing patterns.",
		"✎ Writing the red tests first, then the fix in `reset`.",
		"· 1 tool: Edit",
	}
	for _, name := range []string{"same-story-claude.events.jsonl", "same-story-pi.events.jsonl"} {
		checkAgentBox(t, eventGolden(t, name), want, 1)
	}
}
