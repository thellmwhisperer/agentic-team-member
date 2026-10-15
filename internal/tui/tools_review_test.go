package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFoldedFailureShowsErrorOnOneRow(t *testing.T) {
	m := modelWithEvents(240, eventGolden(t, "cursor.events.jsonl"))
	if got := agentLines(m, 240); !slices.Contains(got,
		"✗ failed shell cat nope.txt · cat: nope.txt: No such file or directory") {
		t.Fatalf("criterion 4 requires the folded error: %q", got)
	}
	m = screen(240, 40, `{"step":"agent","state":"started"}`,
		`{"id":"f","tool":"shell","detail":"cat missing\necho extra","result":"failed","summary":"missing\nmore"}`,
		`{"id":"b","tool":"read","detail":"b.go","result":"passed"}`)
	s := view(m)
	rows := strings.Split(s, "\n")
	at := slices.IndexFunc(rows, func(row string) bool {
		return strings.Contains(row, "failed shell cat missing · missing")
	})
	if at < 0 || at+1 >= len(rows) || !strings.Contains(rows[at+1], "1 tool: read") ||
		strings.Contains(s, "echo extra") || strings.Contains(s, "more") {
		t.Fatalf("criterion 2 requires one row per call:\n%s", s)
	}
	clickRow(m, at+1)
	if s := view(m); !strings.Contains(s, "ok read b.go") {
		t.Fatalf("row after the failure must still be clickable:\n%s", s)
	}
}

func TestFailureSeparatesIndependentAnchoredToolGroups(t *testing.T) {
	evs := []string{
		`{"id":"a","tool":"read","detail":"a.go","result":"passed"}`,
		`{"id":"f","tool":"shell","detail":"false","result":"failed","summary":"failed command"}`,
		`{"id":"b","tool":"bash","detail":"echo b","result":"passed"}`,
	}
	t.Run("independent clicks", func(t *testing.T) {
		m := modelWithEvents(240, evs)
		for _, group := range []struct{ header, shown, hidden string }{
			{"1 tool: bash", "ok bash echo b", "ok read a.go"},
			{"1 tool: read", "ok read a.go", "ok bash echo b"},
		} {
			click(t, m, group.header)
			if s := view(m); !strings.Contains(s, group.shown) || strings.Contains(s, group.hidden) {
				t.Fatalf("criterion 3 requires independent groups:\n%s", s)
			}
			click(t, m, group.header)
			if s := view(m); strings.Contains(s, group.shown) || strings.Contains(s, group.hidden) {
				t.Fatalf("group did not fold independently:\n%s", s)
			}
		}
	})
	t.Run("anchored after failure", func(t *testing.T) {
		m := modelWithEvents(240, evs)
		for i := range 60 {
			feed(m, fmt.Sprintf(`{"says":"tail %02d"}`, i))
		}
		view(m)
		press(m, tea.KeyHome, tea.KeyDown, tea.KeyDown)
		top := topAgentLine(m)
		if !strings.Contains(top, "1 tool: bash") {
			t.Fatalf("expected to be reading the second group: %s", top)
		}
		for range 2 {
			click(t, m, "1 tool: bash")
			if got := topAgentLine(m); got != top {
				t.Fatalf("criterion 8 requires the same top row: %q, want %q", got, top)
			}
		}
		for range 2 {
			pressT(m)
			if got := topAgentLine(m); got != top {
				t.Fatalf("t moved the reading position: %q, want %q", got, top)
			}
		}
	})
}

func TestStandaloneFailureClickIsInert(t *testing.T) {
	m := modelWithEvents(240, eventGolden(t, "cursor.events.jsonl"))
	for range 2 {
		before := view(m)
		click(t, m, "failed shell cat nope.txt")
		if after := view(m); after != before {
			t.Fatalf("criterion 3 allows toggling only group headers:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		pressT(m)
	}
}
