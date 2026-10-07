package run

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"testing"
)

// watched is a log a screen watches: it keeps the live events Run tells it apart.
type watched struct {
	bytes.Buffer
	live []obj
}

func (w *watched) watch(ev []byte) {
	var o obj
	_ = json.Unmarshal(ev, &o)
	w.live = append(w.live, o)
}

// has says whether one of evs has every field of want.
func has(evs []obj, want obj) bool {
	for _, ev := range evs {
		ok := true
		for k, v := range want {
			ok = ok && reflect.DeepEqual(ev[k], v)
		}
		if ok {
			return true
		}
	}
	return false
}

// The screen's rows and boxes come from what the run tells a screen that watches it: each tool call of the
// agent with its outcome and its thinking, each proof and command of the checks, and their output.
func TestRunTellsAWatchingScreenWhatItsAgentAndChecksDo(t *testing.T) {
	streams := map[string]string{
		"claude": `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"Read the test first."},` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true}]}}
`,
		"codex": `{"type":"item.completed","item":{"id":"r1","type":"reasoning","text":"Read the test first."}}
{"type":"item.started","item":{"id":"t1","type":"command_execution","command":"go test ./...","status":"in_progress"}}
{"type":"item.completed","item":{"id":"t1","type":"command_execution","command":"go test ./...","exit_code":1}}
`,
		"opencode": `{"type":"reasoning","part":{"text":"Read the test first."}}
{"type":"tool_use","part":{"callID":"t1","tool":"bash","state":{"status":"error","input":{"command":"go test ./..."}}}}
`,
		"pi": `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking",` +
			`"thinking":"Read the test first."}]}}
{"type":"tool_execution_start","toolCallId":"t1","toolName":"bash","args":{"command":"go test ./..."}}
{"type":"tool_execution_end","toolCallId":"t1","toolName":"bash","result":{"isError":true}}
`,
	}
	for harness, stream := range streams {
		t.Run(harness, func(t *testing.T) {
			repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "test", "echo suite-ok; sh a_test.sh"))
			t.Setenv("FAKE_AGENT_STREAM", stream)
			var w watched
			if err := Run("t", []string{"--harness", harness, issueFile(t, issue)}, &w, io.Discard); err != nil {
				t.Fatal(err)
			}
			for _, want := range []obj{
				{"step": "agent", "thinking": "Read the test first."},
				{"step": "agent", "id": "t1", "detail": "go test ./..."},
				{"step": "agent", "id": "t1", "result": "failed"},
				{"step": "checks", "output": "suite-ok"},
			} {
				if !has(w.live, want) {
					t.Fatalf("no live event with %v in %v", want, w.live)
				}
			}
			var checks [][2]any
			for _, ev := range w.live {
				if ev["step"] == "checks" && ev["check"] != nil {
					checks = append(checks, [2]any{ev["check"], ev["state"]})
				}
			}
			want := [][2]any{{"red/green", "started"}, {"red/green", "passed"}, {"test", "started"}, {"test", "passed"}}
			if !reflect.DeepEqual(checks, want) {
				t.Fatalf("check rows: %v, want %v", checks, want)
			}
			for _, ev := range events(t, &w.Buffer) {
				if ev["thinking"] != nil || ev["output"] != nil || ev["check"] != nil || ev["tool"] != nil {
					t.Fatalf("a live event went to the log: %v", ev)
				}
			}
		})
	}
}
