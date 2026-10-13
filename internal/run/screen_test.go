package run

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
func has(evs []obj, want obj) bool { return index(evs, want) >= 0 }

// index is the position of the first of evs with every field of want, -1 for none.
func index(evs []obj, want obj) int {
	for i, ev := range evs {
		ok := true
		for k, v := range want {
			ok = ok && reflect.DeepEqual(ev[k], v)
		}
		if ok {
			return i
		}
	}
	return -1
}

// watch is what a screen watching a run of harness, whose agent writes stream, in a repo with yml, sees.
func watch(t *testing.T, harness, stream, yml string) *watched {
	t.Helper()
	repo(t, "https://example.com/owner/repo.git", yml)
	t.Setenv("FAKE_AGENT_STREAM", stream)
	var w watched
	if err := Run("t", []string{"--harness", harness, issueFile(t, issue)}, &w, io.Discard); err != nil {
		t.Fatal(err)
	}
	return &w
}

// checkRows are the rows a fix's checks tell the screen, in order: install first.
var checkRows = [][2]any{{"install", "started"}, {"install", "passed"}, {"red/green", "started"},
	{"red/green", "passed"}, {"test", "started"}, {"test", "passed"}}

// The screen's rows and boxes come from what the run tells a screen that watches it: each tool call of the
// agent with its outcome, what it says and its thinking, each proof and command of the checks, and their output.
func TestRunTellsAWatchingScreenWhatItsAgentAndChecksDo(t *testing.T) {
	streams := map[string]string{
		"claude": `{"type":"assistant","message":{"content":[{"type":"text","text":"I'll run the tests."},` +
			`{"type":"thinking","thinking":"Read the test first."},` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true}]}}
`,
		"codex": `{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"I'll run the tests."}}
{"type":"item.completed","item":{"id":"r1","type":"reasoning","text":"Read the test first."}}
{"type":"item.started","item":{"id":"t1","type":"command_execution","command":"go test ./...","status":"in_progress"}}
{"type":"item.completed","item":{"id":"t1","type":"command_execution","command":"go test ./...","exit_code":1}}
`,
		"opencode": `{"type":"text","part":{"text":"I'll run the tests."}}
{"type":"reasoning","part":{"text":"Read the test first."}}
{"type":"tool_use","part":{"callID":"t1","tool":"bash","state":{"status":"error","input":{"command":"go test ./..."}}}}
`,
		"pi": `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"I'll run the tests."},` +
			`{"type":"thinking","thinking":"Read the test first."}]}}
{"type":"tool_execution_start","toolCallId":"t1","toolName":"bash","args":{"command":"go test ./..."}}
{"type":"tool_execution_end","toolCallId":"t1","toolName":"bash","result":{"isError":true}}
`,
	}
	for harness, stream := range streams {
		t.Run(harness, func(t *testing.T) {
			yml := atmSet(atmInstall("true"), "test", "echo suite-ok; sh a_test.sh")
			w := watch(t, harness, stream, yml)
			for _, want := range []obj{
				{"step": "agent", "thinking": "Read the test first."},
				{"step": "agent", "says": "I'll run the tests."},
				{"step": "agent", "id": "t1", "detail": "go test ./..."},
				{"step": "agent", "id": "t1", "result": "failed"},
				{"step": "checks", "output": "suite-ok"},
			} {
				if !has(w.live, want) {
					t.Fatalf("no live event with %v in %v", want, w.live)
				}
			}
			if index(w.live, obj{"says": "I'll run the tests."}) > index(w.live, obj{"id": "t1"}) {
				t.Fatalf("what the agent says came after its tool call: %v", w.live)
			}
			var checks [][2]any
			for _, ev := range w.live {
				if ev["step"] == "checks" && ev["check"] != nil {
					checks = append(checks, [2]any{ev["check"], ev["state"]})
				}
			}
			if !reflect.DeepEqual(checks, checkRows) {
				t.Fatalf("check rows: %v, want %v", checks, checkRows)
			}
			for _, ev := range events(t, &w.Buffer) {
				if ev["thinking"] != nil || ev["output"] != nil || ev["check"] != nil || ev["tool"] != nil {
					t.Fatalf("a live event went to the log: %v", ev)
				}
			}
		})
	}
}

func TestOnlyTheAgentMessageIsShownInACombinedStream(t *testing.T) {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	says := map[string]func(string) string{ // a line where the agent says text
		"claude": func(s string) string {
			return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":` + q(s) + `}]}}`
		},
		"codex": func(s string) string {
			return `{"type":"item.completed","item":{"id":"m","type":"agent_message","text":` + q(s) + `}}`
		},
		"opencode": func(s string) string { return `{"type":"text","part":{"text":` + q(s) + `}}` },
		"pi": func(s string) string {
			return `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + q(s) + `}]}}`
		},
	}
	report := `{"test_file":"a_test.go","summary":"done"}`
	skill := `"content":[{"type":"text","text":"Base directory for this skill: /x"}]}}`
	tool := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use",` +
		`"id":"t1","name":"Bash","input":{"command":"go test ./..."}}]}}`
	userSkill := `{"type":"user","message":{"role":"user",` + skill
	streams := map[string][]string{
		"claude": {userSkill,
			says["claude"]("I'll run the tests."), tool,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
			says["claude"](report), `{"type":"result","result":` + q(report) + `}`},
		"codex": {userSkill, says["codex"]("I'll run the tests."),
			`{"type":"item.started","item":{"id":"t1","type":"command_execution",` +
				`"command":"go test ./...","status":"in_progress"}}`,
			`{"type":"item.completed","item":{"id":"t1","type":"command_execution","command":"go test ./...","exit_code":0}}`,
			says["codex"](report)},
		"opencode": {userSkill, says["opencode"]("I'll run the tests."),
			`{"type":"tool_use","part":{"callID":"t1","tool":"bash","state":{"status":"completed",` +
				`"input":{"command":"go test ./..."}}}}`,
			says["opencode"](report)},
		"pi": {`{"type":"message_end","message":{"role":"user",` + skill,
			says["pi"]("I'll run the tests."),
			`{"type":"tool_execution_start","toolCallId":"t1","toolName":"bash","args":{"command":"go test ./..."}}`,
			`{"type":"tool_execution_end","toolCallId":"t1","toolName":"bash","result":{"isError":false}}`,
			`{"type":"message_end","message":{"role":"toolResult","content":[{"type":"text","text":"ok"}]}}`,
			says["pi"](report)},
	}
	for harness, lines := range streams {
		t.Run(harness, func(t *testing.T) {
			var said []any
			s := stream{live: func(ev map[string]any) {
				if ev["says"] != nil {
					said = append(said, ev["says"])
				}
			}}
			for _, line := range lines {
				s.see(harness, []byte(line))
			}
			if harness == "codex" {
				s.final = report
			}
			s.finish()
			if !reflect.DeepEqual(said, []any{"I'll run the tests."}) {
				t.Fatalf("the combined stream says %q, want only the agent message", said)
			}
		})
	}
}

func TestIntermediateJSONMessagesRemainWholeForEveryHarness(t *testing.T) {
	message := `Config is {"name":"x"}; next I'll test it`
	tool := map[string]string{
		"claude": `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use",` +
			`"id":"t1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		"codex": `{"type":"item.completed","item":{"id":"t1","type":"command_execution",` +
			`"command":"go test ./...","exit_code":0}}`,
		"opencode": `{"type":"tool_use","part":{"callID":"t1","tool":"bash","state":` +
			`{"status":"completed","input":{"command":"go test ./..."}}}}`,
		"pi": `{"type":"tool_execution_start","toolCallId":"t1","toolName":"bash","args":{"command":"go test ./..."}}`,
	}
	encode := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	says := map[string]string{
		"claude": `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text",` +
			`"text":` + encode(message) + `}]}}`,
		"codex":    `{"type":"item.completed","item":{"id":"m","type":"agent_message","text":` + encode(message) + `}}`,
		"opencode": `{"type":"text","part":{"text":` + encode(message) + `}}`,
		"pi": `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text",` +
			`"text":` + encode(message) + `}]}}`,
	}
	for harness := range says {
		t.Run(harness, func(t *testing.T) {
			var got []any
			s := stream{live: func(ev map[string]any) {
				if ev["says"] != nil {
					got = append(got, ev["says"])
				}
			}}
			s.see(harness, []byte(says[harness]))
			s.see(harness, []byte(tool[harness]))
			if !reflect.DeepEqual(got, []any{message}) {
				t.Fatalf("intermediate message %q, want %q", got, message)
			}
		})
	}
}

func TestClaudeFinalReportSplitAcrossTextBlocks(t *testing.T) {
	report := `{"test_file":"a_test.go"}`
	final := "All green.\n" + report
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var got []any
	s := stream{live: func(ev map[string]any) {
		if ev["says"] != nil {
			got = append(got, ev["says"])
		}
	}}
	message := []byte(`{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"text","text":"All green."},{"type":"text","text":` + q(report) + `}]}}`)
	if !json.Valid(message) {
		t.Fatalf("invalid Claude message: %s", message)
	}
	s.see("claude", message)
	if !reflect.DeepEqual(got, []any{"All green."}) {
		t.Fatalf("Claude prose before result %q, want %q", got, "All green.")
	}
	s.see("claude", []byte(`{"type":"result","result":`+q(final)+`}`))
	s.finish()
	if !reflect.DeepEqual(got, []any{"All green."}) {
		t.Fatalf("final Claude text %q, want only the prose", got)
	}
}

// A tool call's path or command shows relative to the clone; the log keeps what the agent sent.
func TestRunTellsAWatchingScreenPathsRelativeToTheClone(t *testing.T) {
	cmd := `{"command":"cd <clone>/ && go test ./..."}`
	streams := map[string]string{
		"claude": `{"type":"assistant","message":{"content":[` +
			`{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"<clone>/a/b.go"}},` +
			`{"type":"tool_use","id":"t2","name":"Bash","input":` + cmd + `},` +
			`{"type":"tool_use","id":"t3","name":"Glob","input":{"path":"<clone>/"}}]}}` + "\n",
		"pi": `{"type":"tool_execution_start","toolCallId":"t1","toolName":"read","args":{"path":"<clone>/a/b.go"}}` +
			"\n" + `{"type":"tool_execution_start","toolCallId":"t2","toolName":"bash","args":` + cmd + `}` + "\n",
		"codex": `{"type":"item.started","item":{"id":"t2","type":"command_execution",` + cmd[1:] + `}` + "\n",
	}
	for harness, stream := range streams {
		t.Run(harness, func(t *testing.T) {
			w := watch(t, harness, stream, atmYAML)
			read := harness != "codex"
			_, clone, log := agentStep(t, &w.Buffer)
			if read && !has(w.live, obj{"id": "t1", "detail": "a/b.go"}) ||
				!has(w.live, obj{"id": "t2", "detail": "cd ./ && go test ./..."}) {
				t.Fatalf("want details relative to the clone in %v", w.live)
			}
			if harness == "claude" && !has(w.live, obj{"id": "t3", "detail": "."}) {
				t.Fatalf("want a trailing clone-root separator to show as dot in %v", w.live)
			}
			if b, _ := os.ReadFile(log); read && !strings.Contains(string(b), filepath.ToSlash(clone)+"/a/b.go") {
				t.Fatalf("want the log to keep the agent's path:\n%s", b)
			}
		})
	}
}

func TestRunRelativizesNativeAndSlashClonePaths(t *testing.T) {
	clone := filepath.Join("repo", ".atm", "clones", "run")
	for _, path := range []struct {
		root, separator string
	}{
		{clone, string(filepath.Separator)},
		{filepath.ToSlash(clone), "/"},
	} {
		var got string
		live := relative(clone, func(ev map[string]any) { got, _ = ev["detail"].(string) })
		live(obj{"detail": path.root + path.separator + "a" + path.separator + "b.go"})
		want := "a" + path.separator + "b.go"
		if got != want {
			t.Errorf("relative detail = %q, want %q", got, want)
		}
		outside := path.root + "-backup"
		live(obj{"detail": outside})
		if got != outside {
			t.Errorf("outside path = %q, want %q", got, outside)
		}
	}
}
