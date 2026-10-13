package run

import (
	"os"
	"strings"
	"testing"
)

func activityEvents(harness, lines string) (out []map[string]any) {
	s := stream{live: func(ev map[string]any) { out = append(out, ev) }}
	for _, l := range strings.Split(strings.TrimSpace(lines), "\n") {
		s.see(harness, []byte(l))
	}
	s.finish()
	return out
}

func fixture(t *testing.T, name string) string {
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const prose = "I'll start by reading the project instructions and the ponytail skill, " +
	"then look at the analytics package."
const thought = "Exploring the packages directory, particularly the analytics package, to understand existing patterns."

func TestPiActivityIncludesThinkingAndProse(t *testing.T) {
	got := activityEvents("pi", fixture(t, "pi-6-2.jsonl"))
	if len(got) != 6 || got[0]["says"] != prose || got[4]["thinking"] != thought ||
		got[1]["tool"] != "read" || got[1]["detail"] != "AGENTS.md" ||
		got[5]["tool"] != "bash" || got[5]["detail"] != "ls packages" {
		t.Fatalf("pi activity events: %#v", got)
	}
	for _, ev := range got {
		if ev["says"] == "# Agents\nRead this before touching the analytics package." {
			t.Fatalf("toolResult text became agent prose: %#v", ev)
		}
	}
}

func TestClaudeEmptyThinkingMakesNoActivity(t *testing.T) {
	got := activityEvents("claude", fixture(t, "claude-261.jsonl"))
	if len(got) != 3 || got[0]["says"] != "Writing the red tests first, then the fix in `reset`." ||
		got[1]["tool"] != "Edit" || got[1]["detail"] != "internal/run/checks_test.go" {
		t.Fatalf("claude activity events: %#v", got)
	}
}

// The same thinking, prose and tool call make the same events in claude and pi.
func TestClaudeAndPiTellTheSameStory(t *testing.T) {
	claude := strings.Replace(fixture(t, "claude-261.jsonl"), `"thinking":""`, `"thinking":"`+thought+`"`, 1)
	pi := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"` + thought +
		`"},{"type":"text","text":"Writing the red tests first, then the fix in ` + "`reset`" + `."},` +
		`{"type":"toolCall","id":"call-0","name":"Edit","arguments":{"path":"internal/run/checks_test.go"}}]}}
{"type":"tool_execution_start","toolCallId":"call-0","toolName":"Edit","args":{"path":"internal/run/checks_test.go"}}
{"type":"tool_execution_end","toolCallId":"call-0","toolName":"Edit","result":{},"isError":false}`
	want := []map[string]any{
		{"thinking": thought},
		{"says": "Writing the red tests first, then the fix in `reset`."},
		{"tool": "Edit", "detail": "internal/run/checks_test.go", "result": "running"},
	}
	for harness, events := range map[string][]map[string]any{"claude": activityEvents("claude", claude), "pi": activityEvents("pi", pi)} {
		if len(events) < 3 || events[0]["thinking"] != want[0]["thinking"] ||
			events[1]["says"] != want[1]["says"] || events[2]["tool"] != want[2]["tool"] ||
			events[2]["detail"] != want[2]["detail"] || events[2]["result"] != want[2]["result"] {
			t.Errorf("%s activity events:\n%#v\nwant prefix\n%#v", harness, events, want)
		}
	}
}
