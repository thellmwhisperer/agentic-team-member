package tui

import (
	"fmt"
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
	if !order(s, "╭─ ATM* ", "#7 Retry on timeout", "✓ Issue  0.1 s", "fix · passed",
		"✓ Clone  0.9 s", "01234567 · passed", "✓ Contract", "✓ Agent · unit 1  4 min 28 s", "1 tools · passed",
		"Checks", "✓ red/green  1.5 s", "test", "○ Slop detector", "pending", "○ Delivery", "pending", "╰") {
		t.Fatalf("rows:\n%s", s)
	}
	// The agent started again, as a run kept before follow-ups were retired replays it, runs on its own row.
	for _, ev := range []string{`{"step":"checks","check":"test","state":"passed","duration_ms":10}`,
		`{"step":"checks","state":"passed","duration_ms":2000}`, `{"step":"agent","state":"started"}`} {
		m.apply(run.Frame{Event: []byte(ev)})
	}
	if s := view(m); !order(s, "Agent · unit 1", "running", "Checks", "red/green", "test", "○ Slop detector",
		"pending", "○ Delivery", "pending") || strings.Count(s, "red/green") != 1 || strings.Count(s, "Agent ·") != 1 ||
		strings.Count(s, "Checks") != 1 || strings.Contains(s, "unit 2") {
		t.Fatalf("agent started again:\n%s", s)
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
	atmTitle := regexp.MustCompile(`(?m)^╭─ ATM\*? ─+╮$`)
	agentTitle := regexp.MustCompile(`(?m)^╭─ Agent\*? ─+╮$`)
	if !atmTitle.MatchString(s) || !agentTitle.MatchString(s) {
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

func TestScreenCutsARowsNoteBeforeItsName(t *testing.T) {
	evs := []string{`{"step":"agent","state":"started"}`}
	for i := range 30 {
		evs = append(evs, fmt.Sprintf(`{"step":"agent","id":"t%d","tool":"Bash","result":"passed"}`, i))
	}
	evs = append(evs, `{"step":"agent","state":"passed","unit":1,"duration_ms":676000}`,
		`{"step":"ponytail","state":"started"}`,
		`{"step":"ponytail","state":"passed","kept":true,"duration_ms":695000,"report":{"findings":[`+
			`{"file":"a.go","family":"speculative_feature","finding":"dropped two lines"}]}}`,
		`{"step":"delivery","state":"started"}`,
		`{"step":"delivery","state":"passed","duration_ms":1379000,`+
			`"branch":"atm/go-port-report-json-keeps-every-units-proofs-and-the-install"}`)
	// Run 218-32's screen: 138 columns, a 46-column ATM box beside the Findings box.
	m := screen(138, 40, evs...)
	m.end(run.Outcome{Outcome: "passed"}, nil)
	s := view(m)
	for _, want := range []string{`✓ Agent · unit 1  11 min 16 s .* passed │`, `✓ Slop detector  11 min 35 s .*passed │`,
		`✓ Delivery  22 min 59 s .*passed │`} {
		if !regexp.MustCompile(want).MatchString(s) {
			t.Fatalf("want %q whole on its row:\n%s", want, s)
		}
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

func TestScreenShowsWhatTheAgentSaysBetweenItsDimmerToolCalls(t *testing.T) {
	m := screen(120, 40, append(unit[:7:7], `{"step":"agent","says":"I'll run the tests.\nThen fix."}`,
		`{"step":"agent","id":"t1","tool":"Bash","detail":"go test ./...","result":"running"}`,
		`{"step":"agent","id":"t1","result":"passed"}`, `{"step":"agent","says":"Green; done."}`)...)
	if s := view(m); !order(s, "╭─ Agent", "✎ I'll run the tests.", "▸ · 1 tool: Bash", "✎ Green; done.") {
		t.Fatalf("agent box:\n%s", s)
	}
	if s := m.View(); !strings.Contains(s, paint(dim, "· 1 tool: Bash")) ||
		strings.Contains(s, paint(dim, "✎ I'll run the tests.")) {
		t.Fatalf("want the tool calls dim and the prose not:\n%q", s)
	}
	m.apply(run.Frame{Event: []byte(`{"step":"agent","state":"passed","duration_ms":1000}`)})
	if s := view(m); !strings.Contains(s, "1 tools · passed") {
		t.Fatalf("want only the tool calls counted:\n%s", s)
	}
}

func TestAResumedUnitsAgentRowNamesTheRunItResumes(t *testing.T) {
	m := screen(120, 40, append(unit[:7:7],
		`{"step":"agent","state":"passed","resumed_from":"retry-1","duration_ms":4}`)...)
	if s := view(m); !strings.Contains(s, "resumed from retry-1 · passed") || strings.Contains(s, "tools") {
		t.Fatalf("resumed agent row:\n%s", s)
	}
}

func TestAgentBoxFoldsToolCallsBetweenWhatTheAgentSaysIntoOneDimLine(t *testing.T) {
	m := screen(120, 40, append(unit[:7:7], `{"step":"agent","thinking":"Plan the fix."}`,
		`{"step":"agent","says":"Writing the red tests first."}`,
		`{"step":"agent","id":"t1","tool":"Edit","detail":"model_test.go","result":"passed"}`,
		`{"step":"agent","id":"t2","tool":"Bash","detail":"go test ./...","result":"passed"}`,
		`{"step":"agent","id":"t3","tool":"Bash","detail":"go vet ./...","result":"passed"}`,
		`{"step":"agent","says":"Red on behaviour. Now the fix."}`,
		`{"step":"agent","id":"t4","tool":"Bash","detail":"go build ./...","result":"failed"}`,
		`{"step":"agent","id":"t5","tool":"Read","detail":"view.go\nmodel.go","result":"running"}`)...)
	s := view(m)
	if !order(s, "∴ Plan the fix.", "✎ Writing the red tests first.", "▸ · 3 tools: Edit, Bash ×2",
		"✎ Red on behaviour. Now the fix.", "✗ failed Bash go build ./...", "⠋ 1 tool: Read · view.go") ||
		strings.Contains(s, "✓ passed Bash") || strings.Contains(s, "model.go") {
		t.Fatalf("agent box:\n%s", s)
	}
	if s := m.View(); !strings.Contains(s, paint(dim, "· 3 tools: Edit, Bash ×2")) ||
		strings.Contains(s, paint(dim, "∴ Plan the fix.")) {
		t.Fatalf("want the tool batch dim and the thinking not:\n%q", s)
	}

	s = view(screen(120, 40, append(unit[:7:7], `{"step":"agent","says":"Working."}`,
		`{"step":"agent","id":"t1","tool":"skill","detail":"","result":"running"}`)...))
	if !strings.Contains(s, "⠋ 1 tool: skill") || strings.Contains(s, "⠋ 1 tool: skill ·") {
		t.Fatalf("want an empty-detail running tool to keep its spinner without a detail suffix:\n%s", s)
	}
}

func TestAgentBoxFillsTheColumnAndWrapsWhatTheAgentSaysToItsLatestLine(t *testing.T) {
	lines := strings.Split(view(screen(120, 40, unit[:7]...)), "\n")
	if len(lines) < 4 || len(lines) > 40 || !strings.Contains(lines[len(lines)-3], "╰") ||
		!strings.Contains(lines[len(lines)-1], "q detach") {
		t.Fatalf("want the Agent box down to the footer:\n%s", strings.Join(lines, "\n"))
	}
	words := strings.Repeat("lorem ipsum ", 16) + "omega" // three rows of the 74 columns inside the box
	s := view(screen(120, 40, append(unit[:7:7], `{"step":"agent","says":"`+words+`"}`)...))
	rows := regexp.MustCompile(`(?m)│ ✎ lorem.*│\n.*│   lorem.*│\n.*│   .*omega +│$`)
	if strings.Contains(s, "…") || !rows.MatchString(s) {
		t.Fatalf("want the long line whole over three rows:\n%s", s)
	}
	evs := unit[:7:7]
	for i := range 60 {
		evs = append(evs, fmt.Sprintf(`{"step":"agent","thinking":"thought %d"}`, i))
	}
	lines = strings.Split(view(screen(120, 40, evs...)), "\n")
	if !strings.Contains(lines[35], "∴ thought 59 ") || !strings.Contains(lines[36], "╰") {
		t.Fatalf("want the last line on the box's last row:\n%s", strings.Join(lines, "\n"))
	}
}

func TestAgentAndLogBoxesShareTheColumnHeight(t *testing.T) {
	evs := append(unit[:7:7], `{"step":"checks","output":"one log line"}`)
	lines := strings.Split(view(screen(120, 40, evs...)), "\n")
	if len(lines) < 4 || len(lines) > 40 || !strings.Contains(lines[len(lines)-3], "╰") ||
		!strings.Contains(strings.Join(lines, "\n"), "one log line") || !strings.Contains(lines[len(lines)-1], "q detach") {
		t.Fatalf("want Agent and Log boxes to fit above the footer:\n%s", strings.Join(lines, "\n"))
	}
}

func TestStackedAgentBoxFitsAboveTheFooter(t *testing.T) {
	lines := strings.Split(view(screen(80, 40, unit[:7]...)), "\n")
	if len(lines) < 4 || len(lines) > 40 || !strings.Contains(lines[len(lines)-3], "╰") ||
		!strings.Contains(lines[len(lines)-1], "q detach") {
		t.Fatalf("want the stacked Agent box above the footer:\n%s", strings.Join(lines, "\n"))
	}
}

func TestAgentBoxShowsEveryLogicalLine(t *testing.T) {
	s := view(screen(120, 40, append(unit[:7:7], `{"step":"agent","says":"first line\nsecond line"}`)...))
	if !order(s, "✎ first line", "  second line") {
		t.Fatalf("want every line the agent says:\n%s", s)
	}
}

// Frames retained by an older server may name steps the current screen does not know.
func TestScreenIgnoresUnknownSteps(t *testing.T) {
	m := screen(100, 40,
		`{"step":"external-stage","state":"started"}`,
		`{"step":"external-stage","check":"external-check","state":"started"}`,
		`{"step":"external-stage","state":"failed","error":"external failure"}`,
	)
	if len(m.rows) != 7 || len(m.findings) != 0 {
		t.Fatalf("unknown step changed the screen: %s", view(m))
	}
}
