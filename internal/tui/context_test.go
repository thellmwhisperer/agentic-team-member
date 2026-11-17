package tui

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// agentRow is the Agent row of m's ATM box, without its border.
func agentRow(t *testing.T, m *Model) string {
	t.Helper()
	for _, line := range strings.Split(ansi.Strip(m.atm(80, 40)), "\n") {
		if strings.Contains(line, "Agent") {
			return strings.TrimSpace(strings.Trim(line, "│"))
		}
	}
	t.Fatalf("no Agent row:\n%s", ansi.Strip(m.atm(80, 40)))
	return ""
}

var contextOnRow = regexp.MustCompile(`Agent  (\S+ / \S+)  `)

// The Agent row shows, while it runs, the context of the worker's last model call and its window, one figure
// per model call of the recorded pi and claude streams; ? when the harness says no window.
func TestAgentRowContext(t *testing.T) {
	for name, want := range map[string][]string{
		"pi-context.events.jsonl":     {"26.9K / 262K", "26.9K / 262K", "27.5K / 262K"},
		"claude-context.events.jsonl": {"22.4K / ?", "22.4K / ?", "23.3K / ?"},
	} {
		m := New("issue-1", 120, 40)
		m.apply(run.Frame{Event: []byte(`{"step":"agent","state":"started","ts":"2026-11-17T21:50:29Z"}`)})
		m.now = m.current("agent").start.Add(286 * time.Second)
		if row := agentRow(t, m); strings.Contains(row, "/") {
			t.Fatalf("%s: a row before any model call shows a context: %q", name, row)
		}
		var got []string
		for _, line := range eventGolden(t, name) {
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatal(err)
			}
			ev["step"] = "agent" // as the run hands it to the screen
			b, _ := json.Marshal(ev)
			m.apply(run.Frame{Event: b})
			if _, ok := ev["context"]; ok {
				row := agentRow(t, m)
				if !strings.Contains(row, "4 min 46 s") || !strings.HasSuffix(row, "running") {
					t.Fatalf("%s: Agent row %q", name, row)
				}
				got = append(got, contextOnRow.FindStringSubmatch(row)[1:]...)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: Agent row contexts %q, want %q", name, got, want)
		}
		m.apply(run.Frame{Event: []byte(`{"step":"agent","state":"passed","duration_ms":1000}`)})
		if row := agentRow(t, m); strings.Contains(row, "/") {
			t.Fatalf("%s: an ended Agent row shows a context: %q", name, row)
		}
	}
}

func TestAgentRowContextFigures(t *testing.T) {
	for _, c := range []struct {
		used, window int
		want         string
	}{
		{26872, 262144, "26.9K / 262K"},
		{999, 0, "999 / ?"},
		{1500, 200000, "1.5K / 200K"},
		{999600, 1000000, "1M / 1M"},
	} {
		if got := contextUsed(c.used, c.window); got != c.want {
			t.Errorf("contextUsed(%d, %d) = %q, want %q", c.used, c.window, got, c.want)
		}
	}
}

// In an ATM box too narrow for it, the context gives way: the label stays whole.
func TestAgentRowContextGivesWayToTheLabel(t *testing.T) {
	m := New("issue-1", 100, 24)
	m.apply(run.Frame{Event: []byte(`{"step":"agent","state":"started","ts":"2026-11-17T21:50:29Z"}`)})
	m.now = m.current("agent").start.Add(286 * time.Second)
	for width, want := range map[int]string{
		36: "⠋ Agent  4 min 46 s",
		42: "⠋ Agent  26.9K / 262K  4 min 46 s",
	} {
		if row := ansi.Strip(m.row(&row{step: "agent", label: "Agent", state: "running",
			start: m.current("agent").start, used: 26872, window: 262144}, width)); !strings.HasPrefix(row, want+" ") ||
			!strings.HasSuffix(row, "running") {
			t.Errorf("%d wide: Agent row %q, want it to start with %q", width, row, want)
		}
	}
}
