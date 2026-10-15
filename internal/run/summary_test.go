package run

import "testing"

// Each harness's tool call ends with the first line of its output, or of its error.
func TestToolCallsEndWithTheFirstLineOfTheirOutput(t *testing.T) {
	for _, harness := range []string{"pi", "claude", "codex", "opencode", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			assertFixtureEvents(t, harness, "summary-"+harness+".jsonl", "summary-"+harness+".events.jsonl")
		})
	}
}
