package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// screen is the screen of a run whose frames carry evs, one event each, at width by height.
func screen(width, height int, evs ...string) *Model {
	m := New("issue-1", width, height)
	for _, ev := range evs {
		m.apply(run.Frame{Event: []byte(ev)})
	}
	return m
}

// view is m's screen without its colours, its lines with no trailing spaces.
func view(m *Model) string {
	return regexp.MustCompile(`(?m) +$`).ReplaceAllString(ansi.Strip(m.View()), "")
}

const ts = `"ts":"2026-10-07T05:00:00Z"`

// unit is a run up to its first unit's checks, red/green passed and the test command running.
var unit = []string{
	`{"step":"issue","state":"started",` + ts + `}`,
	`{"step":"issue","state":"passed","title":"Retry on timeout","number":7,"type":"fix","duration_ms":120}`,
	`{"step":"contract","state":"started"}`, `{"step":"contract","state":"passed","duration_ms":3}`,
	`{"step":"clone","state":"started"}`, `{"step":"clone","state":"passed","sha":"0123456789abcdef","duration_ms":900}`,
	`{"step":"agent","state":"started"}`,
	`{"step":"agent","thinking":"Read the test first."}`,
	`{"step":"agent","id":"t1","tool":"Bash","detail":"go test ./...","result":"running"}`,
	`{"step":"agent","id":"t1","result":"failed"}`,
	`{"step":"agent","state":"passed","unit":1,"duration_ms":268000}`,
	`{"step":"checks","state":"started"}`,
	`{"step":"checks","check":"red/green","state":"started"}`,
	`{"step":"checks","check":"red/green","state":"passed","duration_ms":1500}`,
	`{"step":"checks","check":"test","state":"started"}`,
	`{"step":"checks","output":"=== RUN TestRetry"}`,
}

// order says whether each of want is in s, one after the other.
func order(s string, want ...string) bool {
	for _, w := range want {
		i := strings.Index(s, w)
		if i < 0 {
			return false
		}
		s = s[i+len(w):]
	}
	return true
}

func TestScreenHasARowForEachNodeOfTheGraph(t *testing.T) {
	m := screen(80, 40, unit...)
	s := view(m)
	if !order(s, "╭─ ATM ", "#7 Retry on timeout", "running", "✓ Issue  0.1 s", "fix · passed",
		"✓ Clone  0.9 s", "01234567 · passed", "✓ Contract", "✓ Agent · unit 1  4 min 28 s", "1 tools · passed",
		"Checks", "running", "✓ red/green  1.5 s", "test", "running", "○ Slop detector", "pending",
		"○ Delivery", "pending", "╰") {
		t.Fatalf("rows:\n%s", s)
	}
	// A chained unit repeats its rows after the unit before.
	for _, ev := range []string{`{"step":"checks","check":"test","state":"passed","duration_ms":10}`,
		`{"step":"checks","state":"passed","duration_ms":2000}`, `{"step":"agent","state":"started"}`} {
		m.apply(run.Frame{Event: []byte(ev)})
	}
	if s := view(m); !order(s, "Agent · unit 1", "Checks", "red/green", "test", "Agent · unit 2", "running",
		"○ Checks", "pending", "Slop detector") || strings.Count(s, "red/green") != 1 {
		t.Fatalf("chained unit:\n%s", s)
	}
}

func TestScreenLaysOutAsNoMistakes(t *testing.T) {
	running := append(unit[:8:8], `{"step":"agent","id":"t1","tool":"Bash","detail":"go test ./...","result":"running"}`)
	for _, c := range []struct {
		width, left int
	}{{100, 38}, {120, 40}, {150, 48}} {
		first, _, _ := strings.Cut(view(screen(c.width, 40, running...)), "\n")
		if i := strings.Index(first, "╭─ Agent"); len([]rune(first[:max(i, 0)])) != c.left+gap {
			t.Fatalf("at %d columns, want the Agent box beside a %d-column ATM box: %q", c.width, c.left, first)
		}
	}
	s := view(screen(99, 40, running...))
	if !regexp.MustCompile(`(?m)^╭─ ATM ─+╮$`).MatchString(s) || !regexp.MustCompile(`(?m)^╭─ Agent ─+╮$`).MatchString(s) {
		t.Fatalf("under 100 columns, want the boxes stacked:\n%s", s)
	}
	connector := regexp.MustCompile(`(?m)^│ │ +│$`)
	if s := view(screen(80, 30, running...)); len(connector.FindAllString(s, -1)) != 6 {
		t.Fatalf("at 30 rows, want a connector between each two of 7 nodes:\n%s", s)
	}
	if s := view(screen(80, 29, running...)); connector.MatchString(s) {
		t.Fatalf("under 30 rows, want no connector:\n%s", s)
	}
	if s := view(screen(100, 40, unit...)); !strings.Contains(s, "4 min 28 s") || !strings.Contains(s, "passed") {
		t.Fatalf("at 100 columns, want the full state and duration:\n%s", s)
	}
}

func TestScreenShowsTheAgentThenTheLogOfTheRunningNode(t *testing.T) {
	s := view(screen(120, 40, unit[:10]...))
	if !order(s, "╭─ Agent", "∴ Read the test first.", "✗ failed Bash go test ./...") || !strings.Contains(s, "q detach") {
		t.Fatalf("agent box:\n%s", s)
	}
	s = view(screen(120, 40, unit...))
	if strings.Contains(s, "╭─ Agent") || !order(s, "╭─ Log", "=== RUN TestRetry") {
		t.Fatalf("log box, no agent box, once the agent ended:\n%s", s)
	}
	s = view(screen(120, 40, append(unit, `{"step":"checks","check":"test","state":"passed"}`,
		`{"step":"checks","state":"passed"}`)...))
	if strings.Contains(s, "╭─ Log") {
		t.Fatalf("no log box once no node runs:\n%s", s)
	}
}

func TestScreenGivesAFailedNodeAndTheSlopCutsAFindingsBox(t *testing.T) {
	failed := append(unit, `{"step":"checks","check":"test","state":"failed"}`,
		`{"step":"checks","state":"failed","error":"test: exit status 1\nFAIL TestRetry"}`)
	m := screen(80, 40, failed...)
	m.end(run.Outcome{Outcome: "failed", FailedNode: "checks"}, nil)
	s := view(m)
	if !order(s, "✗ failed", "✗ Checks", "failed", "✓ red/green", "✗ test", "failed", "○ Slop detector", "not run",
		"╭─ Findings - E 1", "E Checks · test", "test: exit status 1") || strings.Contains(s, "FAIL TestRetry") {
		t.Fatalf("findings of a failed node:\n%s", s)
	}
	if strings.Contains(s, "q detach") {
		t.Fatalf("an ended run has no footer:\n%s", s)
	}
	cut := `{"step":"ponytail","state":"passed","kept":true,"report":{"findings":[` +
		`{"file":"a.go","family":"speculative_feature","finding":"dropped two lines"}]}}`
	m = screen(80, 40, `{"step":"ponytail","state":"started"}`, cut)
	m.end(run.Outcome{Outcome: "passed"}, nil)
	if s := view(m); !order(s, "✓ passed", "✓ Slop detector", "1 cuts · passed", "– Delivery", "skipped",
		"╭─ Findings - W 1", "W a.go · speculative_feature", "dropped two lines") {
		t.Fatalf("slop cuts:\n%s", s)
	}
}

func TestScreenStatesHaveAnIconAWordAndANSIColours(t *testing.T) {
	m := screen(80, 40)
	if s := view(m); !strings.Contains(s, "⏸ waiting") || !order(s, "○ Issue", "pending") {
		t.Fatalf("before the first frame:\n%s", s)
	}
	m = screen(80, 40, unit...)
	s := m.View()
	for _, code := range []string{"\x1b[32m✓", "\x1b[34m⠋", "\x1b[90m○", "\x1b[1;36mATM"} {
		if !strings.Contains(s, code) {
			t.Fatalf("want %q in the screen:\n%q", code, s)
		}
	}
	if regexp.MustCompile(`\x1b\[[0-9;]*[34]8;[25];`).MatchString(s) {
		t.Fatalf("a colour outside ANSI 1 to 8:\n%q", s)
	}
}
