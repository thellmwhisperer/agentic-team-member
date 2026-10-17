package tui

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func pressT(m *Model) { m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}) }

// click is a left click on the screen's row that has text in its Agent box.
func click(t *testing.T, m *Model, text string) {
	t.Helper()
	lines := strings.Split(view(m), "\n")
	y := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, text) })
	if y < 0 {
		t.Fatalf("no row with %q:\n%s", text, view(m))
	}
	clickRow(m, y)
}

func clickRow(m *Model, y int) {
	m.Update(tea.MouseMsg{X: m.historyBounds.x + 3, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	view(m)
}

func TestTUnfoldsAndFoldsEveryToolCall(t *testing.T) {
	m := modelWithEvents(240, eventGolden(t, "pi-6-2.events.jsonl"))
	today := agentLines(m, 240)
	if !slices.Contains(today, "▸ · 1 tool: read") {
		t.Fatalf("folded lines changed: %q", today)
	}
	pressT(m)
	got := agentLines(m, 240)
	if !slices.ContainsFunc(got, func(l string) bool { return strings.Contains(l, "ok read AGENTS.md") }) ||
		!slices.ContainsFunc(got, func(l string) bool { return strings.Contains(l, "running bash ls packages") }) {
		t.Fatalf("t did not unfold every call:\n%q", got)
	}
	pressT(m)
	if got := agentLines(m, 240); !reflect.DeepEqual(got, today) {
		t.Fatalf("second t did not fold back:\n%q\nwant:\n%q", got, today)
	}
}

func TestUnfoldedFailedCallShowsItsError(t *testing.T) {
	m := modelWithEvents(240, eventGolden(t, "cursor.events.jsonl"))
	pressT(m)
	got := agentLines(m, 240)
	if !slices.Contains(got, "✗ failed shell cat nope.txt · cat: nope.txt: No such file or directory") {
		t.Fatalf("failed call without its state and error:\n%q", got)
	}
}

func TestUnfoldedCallShowsItsSummaryOrNone(t *testing.T) {
	m := modelWithEvents(240, []string{
		`{"id":"c1","tool":"bash","detail":"ls","result":"passed","summary":"a.go"}`,
		`{"id":"c2","tool":"read","detail":"b.go","result":"passed"}`,
	})
	pressT(m)
	want := []string{"▾ · 2 tools: bash, read", "✓ ok bash ls · a.go", "✓ ok read b.go"}
	if got := agentLines(m, 240); !reflect.DeepEqual(got, want) {
		t.Fatalf("unfolded lines:\n%q\nwant:\n%q", got, want)
	}
}

func TestUnfoldedCallIsCutToThePanel(t *testing.T) {
	m := modelWithEvents(40, []string{`{"id":"c1","tool":"bash","detail":"ls","result":"passed","summary":"` +
		strings.Repeat("x", 100) + `"}`})
	pressT(m)
	for _, l := range strings.Split(m.agentBox(40, 10), "\n") {
		if w := ansi.StringWidth(l); w != 40 {
			t.Fatalf("row %q is %d wide, want 40", ansi.Strip(l), w)
		}
	}
}

func TestClickUnfoldsOneGroupAndTFoldsOrUnfoldsAll(t *testing.T) {
	m := screen(160, 40, `{"step":"agent","state":"started"}`,
		`{"step":"agent","id":"c1","tool":"bash","detail":"echo a\necho b\necho c","result":"passed","summary":"a\nb"}`,
		`{"step":"agent","says":"next"}`,
		`{"step":"agent","id":"c2","tool":"read","detail":"x.go","result":"passed","summary":"package x"}`)
	view(m)
	click(t, m, "1 tool: bash")
	s := view(m)
	if !strings.Contains(s, "ok bash echo a · a") || strings.Contains(s, "echo b") || strings.Contains(s, "x.go") {
		t.Fatalf("a click unfolds its group only, each call on one row:\n%s", s)
	}
	lines := strings.Split(s, "\n")
	at := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "ok bash echo a") })
	if !strings.Contains(lines[at+1], "✎ next") {
		t.Fatalf("the call takes more than one row:\n%s", s)
	}
	clickRow(m, at+2) // the next group's line
	if s := view(m); !strings.Contains(s, "ok read x.go · package x") {
		t.Fatalf("a click on the next group's row did not unfold it:\n%s", s)
	}
	clickRow(m, at) // a call: nothing
	if s := view(m); !strings.Contains(s, "ok bash echo a") || !strings.Contains(s, "ok read x.go") {
		t.Fatalf("a click on a call changed the box:\n%s", s)
	}
	click(t, m, "1 tool: read")
	if s := view(m); strings.Contains(s, "ok read x.go") || !strings.Contains(s, "ok bash echo a") {
		t.Fatalf("a click on an unfolded group's line folds only it:\n%s", s)
	}
	pressT(m)
	if s := view(m); !strings.Contains(s, "ok bash echo a") || !strings.Contains(s, "ok read x.go") {
		t.Fatalf("t did not unfold every call:\n%s", s)
	}
	pressT(m)
	if s := view(m); strings.Contains(s, "ok bash") || strings.Contains(s, "ok read") {
		t.Fatalf("a second t did not fold every call:\n%s", s)
	}
}

func TestCallClickedOpenWhileRunningShowsItsErrorOnceItFails(t *testing.T) {
	m := screen(160, 40, `{"step":"agent","state":"started"}`,
		`{"step":"agent","id":"c1","tool":"bash","detail":"go test","result":"running"}`)
	view(m)
	click(t, m, "1 tool: bash")
	if s := view(m); !strings.Contains(s, "running bash go test") {
		t.Fatalf("running call not unfolded:\n%s", s)
	}
	feed(m, `{"step":"agent","id":"c1","result":"failed","summary":"FAIL TestX\nmore"}`)
	if s := view(m); !strings.Contains(s, "✗ failed bash go test · FAIL TestX") || strings.Contains(s, "more") {
		t.Fatalf("failed call without its error:\n%s", s)
	}
}

func TestFooterAnnouncesTWhileTheAgentBoxShows(t *testing.T) {
	m := screen(160, 40, `{"step":"agent","state":"started"}`)
	if s := view(m); !strings.Contains(s, "q detach") || !strings.Contains(s, "t show tool use") {
		t.Fatalf("footer:\n%s", s)
	}
	feed(m, `{"step":"agent","state":"passed"}`)
	if s := view(m); strings.Contains(s, "tool use") {
		t.Fatalf("footer announces t without the Agent box:\n%s", s)
	}
}

func TestUnfoldingKeepsTheReadingPosition(t *testing.T) {
	var evs []string
	for i := range 40 {
		evs = append(evs, fmt.Sprintf(`{"thinking":"thought %02d"}`, i),
			fmt.Sprintf(`{"id":"c%d","tool":"bash","detail":"ls","result":"passed"}`, i))
	}
	m := modelWithEvents(160, evs)
	agentLines(m, 160)
	for range 20 {
		m.Update(tea.KeyMsg{Type: tea.KeyUp})
	}
	top := agentLines(m, 160)[0]
	pressT(m)
	if got := agentLines(m, 160)[0]; got != top {
		t.Fatalf("unfolding moved the view from %q to %q", top, got)
	}
	pressT(m)
	if got := agentLines(m, 160)[0]; got != top {
		t.Fatalf("folding moved the view from %q to %q", top, got)
	}
}

func TestToolSummaryDoesNotPassTerminalControlsThrough(t *testing.T) {
	m := modelWithEvents(60, []string{`{"id":"c1","tool":"bash","detail":"ls\u001b[2J","result":"passed",` +
		`"summary":"safe\u001b]0;title\u0007 \u001b[2Jtext\r\b"}`})
	pressT(m)
	raw := m.agentBox(60, 10)
	if got := agentLines(m, 60); !slices.Contains(got, "✓ ok bash ls · safe text") {
		t.Fatalf("Agent box lines: %q", got)
	}
	for _, c := range []string{"\x1b]", "\x1b[2J", "\a", "\r", "\b"} {
		if strings.Contains(raw, c) {
			t.Fatalf("Agent box passes %q through:\n%q", c, raw)
		}
	}
}
