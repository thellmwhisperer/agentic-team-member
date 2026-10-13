package run

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func activityEvents(harness, lines string) (out []map[string]any) {
	s := stream{live: func(ev map[string]any) { out = append(out, ev) }}
	for _, l := range strings.Split(strings.TrimSpace(lines), "\n") {
		s.see(harness, []byte(l))
	}
	s.finish()
	return out
}

func goldenEvents(t *testing.T, name string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func assertFixtureEvents(t *testing.T, harness, fixtureName, goldenName string) {
	t.Helper()
	got := activityEvents(harness, fixture(t, fixtureName))
	want := goldenEvents(t, goldenName)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s activity events:\n%#v\nwant:\n%#v", harness, got, want)
	}
}

func TestPiActivityMatchesItsEventGolden(t *testing.T) {
	assertFixtureEvents(t, "pi", "pi-6-2.jsonl", "pi-6-2.events.jsonl")
}

func TestClaudeActivityMatchesItsEventGolden(t *testing.T) {
	assertFixtureEvents(t, "claude", "claude-261.jsonl", "claude-261.events.jsonl")
}

func TestClaudeAndPiTellTheSameStory(t *testing.T) {
	thought := "Exploring the packages directory, particularly the analytics package, to understand existing patterns."
	claude := strings.Replace(fixture(t, "claude-261.jsonl"), `"thinking":""`, `"thinking":"`+thought+`"`, 1)
	pi := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"` + thought +
		`"},{"type":"text","text":"Writing the red tests first, then the fix in ` + "`reset`" + `."},` +
		`{"type":"toolCall","id":"call-0","name":"Edit","arguments":{"path":"internal/run/checks_test.go"}}]}}
{"type":"tool_execution_start","toolCallId":"call-0","toolName":"Edit","args":{"path":"internal/run/checks_test.go"}}
{"type":"tool_execution_end","toolCallId":"call-0","toolName":"Edit","result":{},"isError":false}`
	for harness, fixture := range map[string]struct{ input, golden string }{
		"claude": {claude, "same-story-claude.events.jsonl"},
		"pi":     {pi, "same-story-pi.events.jsonl"},
	} {
		got, want := activityEvents(harness, fixture.input), goldenEvents(t, fixture.golden)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s activity events:\n%#v\nwant:\n%#v", harness, got, want)
		}
	}
}
