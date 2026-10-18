package run

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

func TestPiModelErrorReportsProviderMessage(t *testing.T) {
	const providerError = "The model is currently at capacity"
	const failed = `{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"` +
		providerError + `"}}`
	const completed = `{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[` +
		`{"type":"text","text":"{\"test_file\":\"a_test.go\"}"}]}}`
	for _, step := range []struct{ name, skill string }{
		{"worker", "ponytail"},
		{"ponytail", "ponytail-review"},
	} {
		for _, tc := range []struct {
			name, messages, want string
			proof                bool
		}{
			{"error before skill", failed, "pi: " + providerError, false},
			{"error after skill", failed, "pi: " + providerError, true},
			{"error after success", completed + "\n" + failed, "pi: " + providerError, true},
			{"recovered", failed + "\n" + completed, "", true},
			{"normal", completed, "", true},
			{"missing skill", completed, "agent left no proof", false},
		} {
			t.Run(step.name+"/"+tc.name, func(t *testing.T) {
				s := &stream{skill: step.skill, live: func(map[string]any) {}}
				if tc.proof {
					s.see("pi", []byte(fmt.Sprintf(`{"type":"tool_execution_start","toolCallId":"s1","toolName":"read",`+
						`"args":{"path":".atm/skills/%s/SKILL.md"}}`, step.skill)))
					s.see("pi", []byte(`{"type":"tool_execution_end","toolCallId":"s1","toolName":"read","isError":false}`))
				}
				for _, line := range strings.Split(tc.messages, "\n") {
					s.see("pi", []byte(line))
				}
				s.see("pi", []byte(`{"type":"agent_settled"}`))
				a := agent{Agent: config.Agent{Harness: "pi"}}
				ev := map[string]any{}
				invalid, err := a.reported(step.name, step.skill, "pi.jsonl", "", "exit code 0", "", nil,
					s, false, testFile, ev)
				if tc.want == "" {
					if err != nil || invalid != nil || ev["report"] == nil {
						t.Fatalf("want successful report, got %v, %v, %v", ev, invalid, err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) || invalid != nil {
					t.Fatalf("want fatal %q, got invalid=%v, error=%v", tc.want, invalid, err)
				}
				if strings.HasPrefix(tc.want, "pi:") {
					if want := "exit code 0; " + tc.want + "; log: pi.jsonl"; err.Error() != want {
						t.Fatalf("want %q, got %q", want, err)
					}
				}
			})
		}
	}
}
