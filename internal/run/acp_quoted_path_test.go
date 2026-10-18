package run

import (
	"encoding/json"
	"testing"
)

func TestACPQuotedPathPermissionAndReach(t *testing.T) {
	for _, quote := range []string{"'", `"`} {
		for _, c := range []struct {
			path, option string
			outside      bool
		}{
			{"/workspace/clone two/private.txt", "reject", true},
			{"/workspace/clone/private file.txt", "allow", false},
		} {
			for _, representation := range []string{"rawInput", "content"} {
				t.Run(quote+c.path+representation, func(t *testing.T) {
					command := "touch " + quote + c.path + quote
					call := map[string]any{"kind": "execute", "sessionUpdate": "tool_call"}
					if representation == "rawInput" {
						call["rawInput"] = map[string]any{"command": command}
					} else {
						call["content"] = []any{map[string]any{"type": "content", "content": map[string]any{
							"type": "text", "text": "$ " + command}}}
					}
					s := stream{clone: "/workspace/clone"}
					request := jsonLine(map[string]any{"id": 4, "method": "session/request_permission",
						"params": map[string]any{"toolCall": call, "options": []any{
							map[string]any{"optionId": "always", "kind": "allow_always"},
							map[string]any{"optionId": "allow", "kind": "allow_once"},
							map[string]any{"optionId": "reject", "kind": "reject_once"},
							map[string]any{"optionId": "later", "kind": "reject_once"},
						}}})
					var reply struct {
						Result struct{ Outcome struct{ OptionID string } }
					}
					if err := json.Unmarshal(s.answer(request), &reply); err != nil {
						t.Fatal(err)
					}
					if reply.Result.Outcome.OptionID != c.option {
						t.Fatalf("permission = %q, want %q", reply.Result.Outcome.OptionID, c.option)
					}
					s.acp(jsonLine(map[string]any{"method": "session/update", "params": map[string]any{"update": call}}))
					want := ""
					if c.outside {
						want = "execute " + command
					}
					if s.reach != want {
						t.Fatalf("post-step reach = %q, want %q", s.reach, want)
					}
				})
			}
		}
	}
}
