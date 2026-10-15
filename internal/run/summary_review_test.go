package run

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestToolSummaryPreservesAnEmptyFirstLine(t *testing.T) {
	for harness, format := range map[string]string{
		"pi":       `{"type":"tool_execution_end","toolCallId":"c","toolName":"bash","result":{"content":[{"type":"text","text":%s}]}}`,
		"claude":   `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":%s}]}}`,
		"codex":    `{"type":"item.completed","item":{"id":"c","type":"command_execution","status":"completed","aggregated_output":%s}}`,
		"opencode": `{"type":"tool_use","part":{"callID":"c","tool":"bash","state":{"status":"completed","output":%s}}}`,
		"cursor":   `{"type":"tool_call","subtype":"completed","call_id":"c","tool_call":{"shellToolCall":{"result":{"success":{"stdout":%s}}}}}`,
	} {
		t.Run(harness, func(t *testing.T) {
			for _, tc := range []struct{ output, want string }{
				{"\nanalytics\nweb\n", ""},
				{" \t\r\nanalytics\n", ""},
				{" analytics \r\nweb\n", "analytics"},
			} {
				encoded, err := json.Marshal(tc.output)
				if err != nil {
					t.Fatal(err)
				}
				var e event
				if err := json.Unmarshal([]byte(fmt.Sprintf(format, encoded)), &e); err != nil {
					t.Fatal(err)
				}
				got := e.activity(harness)
				if len(got) != 1 {
					t.Fatalf("expected one tool event: %v", got)
				}
				summary, _ := got[0]["summary"].(string)
				if summary != tc.want {
					t.Fatalf("criterion 2 requires the first line of %q: got %q, want %q", tc.output, summary, tc.want)
				}
			}
		})
	}
}
