package run

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// boxLines is what the Agent box shows of harness's stream: a ∴ line per thinking event, a ✎ line per says
// event, and a line per tool call, by its first event.
func boxLines(harness, lines string) []string {
	var out []string
	seen := map[string]bool{}
	s := stream{live: func(ev map[string]any) {
		switch {
		case ev["thinking"] != nil:
			out = append(out, "∴ "+ev["thinking"].(string))
		case ev["says"] != nil:
			out = append(out, "✎ "+ev["says"].(string))
		case !seen[ev["id"].(string)]:
			seen[ev["id"].(string)] = true
			out = append(out, ev["tool"].(string)+" "+ev["detail"].(string))
		}
	}}
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

func TestPiThinkingAndProseReachTheBox(t *testing.T) {
	got := boxLines("pi", fixture(t, "pi-6-2.jsonl"))
	want := []string{"✎ " + prose, "read AGENTS.md", "∴ " + thought, "bash ls packages"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("box lines:\n%q\nwant\n%q", got, want)
	}
}

func TestClaudeEmptyThinkingMakesNoLine(t *testing.T) {
	got := boxLines("claude", fixture(t, "claude-261.jsonl"))
	want := []string{"✎ Writing the red tests first, then the fix in `reset`.", "Edit internal/run/checks_test.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("box lines:\n%q\nwant\n%q", got, want)
	}
}

// The same thinking, prose and tool call make the same box lines in claude and in pi.
func TestClaudeAndPiTellTheSameStory(t *testing.T) {
	claude := strings.Replace(fixture(t, "claude-261.jsonl"), `"thinking":""`, `"thinking":"`+thought+`"`, 1)
	pi := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"` + thought +
		`"},{"type":"text","text":"Writing the red tests first, then the fix in ` + "`reset`" + `."},` +
		`{"type":"toolCall","id":"call-0","name":"Edit","arguments":{"path":"internal/run/checks_test.go"}}]}}
{"type":"tool_execution_start","toolCallId":"call-0","toolName":"Edit","args":{"path":"internal/run/checks_test.go"}}
{"type":"tool_execution_end","toolCallId":"call-0","toolName":"Edit","result":{},"isError":false}`
	want := []string{"∴ " + thought, "✎ Writing the red tests first, then the fix in `reset`.", "Edit internal/run/checks_test.go"}
	for harness, lines := range map[string][]string{"claude": boxLines("claude", claude), "pi": boxLines("pi", pi)} {
		if !reflect.DeepEqual(lines, want) {
			t.Errorf("%s box lines:\n%q\nwant\n%q", harness, lines, want)
		}
	}
}
