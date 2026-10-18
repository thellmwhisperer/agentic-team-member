//go:build unix

package run

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestHermesFixtureTellsTheBox(t *testing.T) {
	recorded := strings.ReplaceAll(fixture(t, "hermes-acp.jsonl"), "<clone>", "/workspace/clone")
	if recorded != strings.ReplaceAll(hermesTranscript, "<clone>", "/workspace/clone") {
		t.Fatal("the contract test's transcript is not the recorded fixture")
	}
	// Chunks of one message, split as an agent streams them, join into one line.
	first, rest, _ := strings.Cut(recorded, "\n")
	split := strings.Replace(first, "command to create file**", "", 1) + "\n" +
		strings.Replace(first, "**Executing shell ", "", 1) + "\n" + rest
	want := []map[string]any{{"thinking": "**Executing shell command to create file**"},
		toolCall("tc-1", "execute", "terminal: cat nope.txt + 1 command", "running"), toolCall("tc-1", "", "", "passed"),
		{"thinking": "**Verifying file reading function**"}, toolCall("tc-2", "read", "read_file: hola.txt", "running"),
		{"says": "DONE"}}
	for _, lines := range []string{recorded, split} {
		if got := activityEvents("hermes", lines); !reflect.DeepEqual(got, want) {
			t.Fatalf("hermes activity events:\n%#v\nwant:\n%#v", got, want)
		}
	}
	s := stream{live: func(map[string]any) {}}
	for _, l := range strings.Split(strings.TrimSpace(recorded), "\n") {
		s.see("hermes", []byte(l))
	}
	if s.final != "\n\nDONE" {
		t.Fatalf("final %q, want the message after the last tool call", s.final)
	}
}

func TestACPPermissionAnswers(t *testing.T) {
	for _, c := range []struct{ name, clone, toolCall, want string }{
		{"command reading outside", "/workspace/clone",
			`{"rawInput":{"command":"/workspace/clone/bin/reader /etc/hosts"}}`, "ao"},
		{"command writing outside", "/workspace/clone",
			`{"rawInput":{"command":"/workspace/clone/bin/reader > /etc/hosts"}}`, "ro"},
		{"path in a clone with a space", "/workspace/my clone", `{"rawInput":{"path":"/workspace/my clone/a.txt"}}`, "ao"},
		{"relative path inside", "/workspace/clone", `{"locations":[{"path":"src/a.txt"}]}`, "ao"},
		{"relative path outside", "/workspace/clone", `{"rawInput":{"file_path":"../other/a.txt"}}`, "ro"},
		{"content-only fetch", "/workspace/clone",
			`{"content":[{"type":"content","content":{"type":"text","text":"$ curl -s example.com"}}]}`, "ao"},
		{"outside path with the clone as its prefix", "/workspace/clone",
			`{"rawInput":{"path":"/workspace/clone two/a.txt"}}`, "ro"},
		{"command inside", "/workspace/clone",
			`{"content":[{"type":"content","content":{"type":"text","text":"$ cat a.txt"}}],"rawInput":{"command":"cat a.txt"}}`,
			"ao"},
	} {
		t.Run(c.name, func(t *testing.T) {
			params := `{"sessionId":"s1","toolCall":` + c.toolCall + `,"options":[` +
				`{"optionId":"aa","kind":"allow_always"},{"optionId":"ra","kind":"reject_always"},` +
				`{"optionId":"ao","kind":"allow_once"},{"optionId":"ro","kind":"reject_once"}]}`
			s := stream{clone: c.clone}
			got, _ := json.Marshal(s.permit(json.RawMessage(params)))
			if want := `{"outcome":{"optionId":"` + c.want + `","outcome":"selected"}}`; string(got) != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

// A read-only review's permission gate allows the scratch directory its brief names, and only it: the same
// single allowance the post-step reach check makes for that review stream.
func TestACPScratchPermission(t *testing.T) {
	const scratch = "/runs/12-24/scope-review-unit2-scratch"
	s := stream{clone: "/workspace/clone", scratch: scratch}
	for _, c := range []struct{ name, toolCall, want string }{
		{"file under the scratch", `{"rawInput":{"path":"` + scratch + `/golden.md"}}`, "ao"},
		{"the scratch itself", `{"rawInput":{"path":"` + scratch + `"}}`, "ao"},
		{"command writing under the scratch",
			`{"rawInput":{"command":"cp plan.md ` + scratch + `/plan.md"}}`, "ao"},
		{"path escaping the scratch with ..",
			`{"rawInput":{"path":"` + scratch + `/../outside.md"}}`, "ro"},
		{"a directory whose name starts like the scratch",
			`{"rawInput":{"path":"` + scratch + `2/x.md"}}`, "ro"},
		{"another run's scratch", `{"rawInput":{"path":"/runs/12-25/scope-review-unit2-scratch/x.md"}}`, "ro"},
		{"command writing elsewhere outside",
			`{"rawInput":{"command":"cp plan.md /runs/12-24/other/x"}}`, "ro"},
	} {
		t.Run(c.name, func(t *testing.T) {
			permitAnswer(t, s, c.toolCall,
				`{"optionId":"ao","kind":"allow_once"},{"optionId":"ro","kind":"reject_once"}`, c.want)
		})
	}
}

// permitAnswer asks one session/request_permission with the given options and checks the option permit selects.
func permitAnswer(t *testing.T, s stream, toolCall, options, want string) {
	t.Helper()
	params := `{"sessionId":"s1","toolCall":` + toolCall + `,"options":[` + options + `]}`
	got, _ := json.Marshal(s.permit(json.RawMessage(params)))
	if selected := `{"outcome":{"optionId":"` + want + `","outcome":"selected"}}`; string(got) != selected {
		t.Fatalf("got %s, want %s", got, selected)
	}
}
