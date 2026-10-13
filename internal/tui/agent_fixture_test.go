package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func agentFixture(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("../run/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return agentEvents(t, string(b))
}

func agentEvents(t *testing.T, input string) []string {
	t.Helper()
	type block struct {
		Type, Role, Text, Thinking, ID, Name string
		ToolUseID                            string `json:"tool_use_id"`
		Arguments, Input                     json.RawMessage
	}
	var events []string
	add := func(event any) {
		b, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, string(b))
	}
	add(map[string]any{"step": "agent", "state": "started"})
	for _, line := range strings.Split(strings.TrimSpace(input), "\n") {
		var e struct {
			Type    string
			Message struct {
				Role    string
				Content []block
			}
			ToolCallID, ToolName string
			Args                 json.RawMessage
			IsError              bool
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		switch e.Type {
		case "assistant", "user":
			for _, b := range e.Message.Content {
				switch b.Type {
				case "thinking":
					add(map[string]any{"step": "agent", "thinking": b.Thinking})
				case "text":
					if e.Type == "assistant" {
						add(map[string]any{"step": "agent", "says": b.Text})
					}
				case "tool_use":
					add(map[string]any{"step": "agent", "id": b.ID, "tool": b.Name,
						"detail": fixtureDetail(b.Input), "result": "running"})
				case "tool_result":
					add(map[string]any{"step": "agent", "id": b.ToolUseID, "result": "passed"})
				}
			}
		case "message_end":
			for _, b := range e.Message.Content {
				if e.Message.Role != "assistant" {
					continue
				}
				switch b.Type {
				case "thinking":
					add(map[string]any{"step": "agent", "thinking": b.Thinking})
				case "text":
					add(map[string]any{"step": "agent", "says": b.Text})
				case "toolCall":
					add(map[string]any{"step": "agent", "id": b.ID, "tool": b.Name,
						"detail": fixtureDetail(b.Arguments), "result": "running"})
				}
			}
		case "tool_execution_start":
			add(map[string]any{"step": "agent", "id": e.ToolCallID, "tool": e.ToolName,
				"detail": fixtureDetail(e.Args), "result": "running"})
		case "tool_execution_end":
			result := "passed"
			if e.IsError {
				result = "failed"
			}
			add(map[string]any{"step": "agent", "id": e.ToolCallID, "result": result})
		}
	}
	return events
}

func fixtureDetail(raw json.RawMessage) string {
	var args map[string]string
	_ = json.Unmarshal(raw, &args)
	for _, key := range []string{"command", "file_path", "path"} {
		if args[key] != "" {
			return args[key]
		}
	}
	return ""
}

func TestAgentBoxRendersHarnessFixtures(t *testing.T) {
	pi := view(screen(120, 40, agentFixture(t, "pi-6-2.jsonl")...))
	if !order(pi, "✎ I'll start by reading the project instructions", "· 1 tool: read", "∴ Exploring the packages directory", "⠋ 1 tool: bash") ||
		strings.Contains(pi, "# Agents") {
		t.Fatalf("pi Agent box:\n%s", pi)
	}
	claude := view(screen(120, 40, agentFixture(t, "claude-261.jsonl")...))
	if !order(claude, "✎ Writing the red tests first", "· 1 tool: Edit") || strings.Contains(claude, "∴") {
		t.Fatalf("claude Agent box:\n%s", claude)
	}
}

func TestAgentBoxRendersTheSameStoryForBothHarnesses(t *testing.T) {
	thought := "Exploring the packages directory, particularly the analytics package, to understand existing patterns."
	prose := "Writing the red tests first, then the fix in `reset`."
	claude := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"` + thought + `"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + prose + `"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Edit","input":{"file_path":"internal/run/checks_test.go"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","is_error":false,"content":"ok"}]}}`
	pi := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"` + thought + `"}]}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"` + prose + `"}]}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call-0","name":"Edit","arguments":{"path":"internal/run/checks_test.go"}}]}}
{"type":"tool_execution_start","toolCallId":"call-0","toolName":"Edit","args":{"path":"internal/run/checks_test.go"}}
{"type":"tool_execution_end","toolCallId":"call-0","toolName":"Edit","result":{},"isError":false}`
	want := []string{
		"∴ " + thought,
		"✎ " + prose,
		"· 1 tool: Edit",
	}
	for harness, input := range map[string]string{"claude": claude, "pi": pi} {
		got := view(screen(200, 40, agentEvents(t, input)...))
		if !order(got, want...) {
			t.Errorf("%s Agent box:\n%s\nwant sequence\n%q", harness, got, want)
		}
	}
}
