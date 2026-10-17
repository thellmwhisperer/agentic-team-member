package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func pressH(m *Model) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}}) }

// rawRow is the screen row, colours and all, that shows text.
func rawRow(t *testing.T, m *Model, text string) string {
	t.Helper()
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(ansi.Strip(line), text) {
			return line
		}
	}
	t.Fatalf("no row with %q:\n%s", text, view(m))
	return ""
}

func TestClickableRowsStartWithAnUnderlinedArrow(t *testing.T) {
	m := screen(160, 40, `{"step":"agent","state":"started"}`,
		`{"step":"agent","says":"I will run the tests."}`,
		`{"step":"agent","id":"c1","tool":"bash","detail":"ls","result":"passed"}`)
	row := rawRow(t, m, "1 tool: bash")
	if !strings.Contains(ansi.Strip(row), "│ ▸") || !strings.Contains(row, "\x1b[4m▸") {
		t.Fatalf("a folded tool group row must start with an underlined ▸:\n%q", row)
	}
	if rest := row[strings.Index(row, "▸")+len("▸"):]; strings.Contains(rest, "\x1b[4m") {
		t.Fatalf("only the arrow of a clickable row is underlined:\n%q", row)
	}
	if says := rawRow(t, m, "I will run the tests."); strings.ContainsAny(ansi.Strip(says), "▸▾") ||
		strings.Contains(says, "\x1b[4m") {
		t.Fatalf("a ✎ row has no arrow and no underline:\n%q", says)
	}
	click(t, m, "1 tool: bash")
	if row = rawRow(t, m, "1 tool: bash"); !strings.Contains(ansi.Strip(row), "│ ▾") ||
		!strings.Contains(row, "\x1b[4m▾") {
		t.Fatalf("an unfolded tool group row must start with an underlined ▾:\n%q", row)
	}
}

func TestThinkingFoldsLikeToolCalls(t *testing.T) {
	m := screen(160, 40, `{"step":"agent","state":"started"}`,
		`{"step":"agent","thinking":"First thought.\nSecond thought."}`)
	if got := agentLines(m, 160); !reflect.DeepEqual(got, []string{"▸ ∴ First thought."}) {
		t.Fatalf("a thinking message is folded to one row by default:\n%q", got)
	}
	click(t, m, "∴ First thought.")
	if got := agentLines(m, 160); !reflect.DeepEqual(got, []string{"▾ ∴ First thought.", "Second thought."}) {
		t.Fatalf("a click unfolds a thinking message to its full text:\n%q", got)
	}
	click(t, m, "∴ First thought.")
	if got := agentLines(m, 160); !reflect.DeepEqual(got, []string{"▸ ∴ First thought."}) {
		t.Fatalf("a second click folds the thinking message back to one row:\n%q", got)
	}
	long := screen(40, 40, `{"step":"agent","state":"started"}`,
		`{"step":"agent","thinking":"`+strings.Repeat("x", 60)+`"}`)
	if got := agentLines(long, 40); len(got) != 1 || !strings.HasSuffix(got[0], "…") {
		t.Fatalf("a folded thinking message is cut to the box width with …:\n%q", got)
	}
}

func TestFooterHintsFollowShowAndHide(t *testing.T) {
	m := screen(160, 40, `{"step":"agent","state":"started"}`,
		`{"step":"agent","thinking":"A thought."}`)
	if s := view(m); !strings.Contains(s, "t show tool use") || !strings.Contains(s, "h show thinking") {
		t.Fatalf("hints while everything is folded:\n%s", s)
	}
	pressH(m)
	if s := view(m); !strings.Contains(s, "h hide thinking") {
		t.Fatalf("h unfolds every thinking message and its hint follows:\n%s", s)
	}
	if got := agentLines(m, 160); !reflect.DeepEqual(got, []string{"▾ ∴ A thought."}) {
		t.Fatalf("h unfolds every thinking message:\n%q", got)
	}
	pressT(m)
	if s := view(m); !strings.Contains(s, "t hide tool use") {
		t.Fatalf("t unfolds every tool call and its hint follows:\n%s", s)
	}
	pressT(m)
	if s := view(m); !strings.Contains(s, "t show tool use") {
		t.Fatalf("a second t folds every tool call again:\n%s", s)
	}
	pressH(m)
	if s := view(m); !strings.Contains(s, "h show thinking") {
		t.Fatalf("a second h folds every thinking message again:\n%s", s)
	}
}
