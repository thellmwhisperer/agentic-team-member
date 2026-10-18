package run

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCursorActivityMatchesItsEventGolden(t *testing.T) {
	assertFixtureEvents(t, "cursor", "cursor.jsonl", "cursor.events.jsonl")
	s := stream{live: func(map[string]any) {}}
	for _, l := range strings.Split(strings.TrimSpace(fixture(t, "cursor.jsonl")), "\n") {
		s.see("cursor", []byte(l))
	}
	if s.final != "DONE" || s.failure != "" {
		t.Fatalf("final %q, failure %q: want DONE and none", s.final, s.failure)
	}
	s.see("cursor", []byte(`{"type":"result","is_error":true,"result":"rate limited"}`))
	if s.failure != "rate limited" {
		t.Fatalf("failure %q, want rate limited", s.failure)
	}
}

func TestRunFailsWithCursorsErrorResult(t *testing.T) {
	for _, mode := range []string{"", "fail"} {
		t.Run("exit"+map[string]string{"": "0", "fail": "3"}[mode], func(t *testing.T) {
			repo(t, "https://example.com/owner/repo.git", atmYAML)
			t.Setenv("FAKE_AGENT", mode)
			t.Setenv("FAKE_AGENT_STREAM", `{"type":"result","is_error":true,"result":"rate limited"}`+"\n")
			var out bytes.Buffer
			err := Run("t", []string{"--harness", "cursor", issueFile(t, issue)}, &out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "rate limited") {
				t.Fatalf("want the step failed with cursor's result, got %v", err)
			}
			exit := "exit code " + map[string]string{"": "0", "fail": "3"}[mode]
			if !strings.Contains(err.Error(), exit) {
				t.Fatalf("lost process failure: %v", err)
			}
			if end, _, _ := agentStep(t, &out); end["state"] != "failed" || end["exit"] != exit {
				t.Fatalf("end event: %v", end)
			}
		})
	}
}

func cursorCall(kind, subtype, id string, args obj, result obj) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // keep <clone> literal for the fake process's path substitution
	_ = enc.Encode(obj{"type": "tool_call", "subtype": subtype, "call_id": id,
		"tool_call": obj{"hookAdditionalContexts": []obj{}, "completedAtMs": 42,
			kind + "ToolCall": obj{"args": args, "result": result}}})
	return b.String()
}

func TestCursorReachNormalization(t *testing.T) {
	const why = "the agent reached outside its clone: "
	runReachCases(t, []reachCase{
		{"cursor shell remote", "cursor", cursorCall("shell", "started", "c1",
			obj{"command": "curl https://example.com"}, nil), why + "shell curl https://example.com"},
		{"cursor shell multiline", "cursor", cursorCall("shell", "started", "c1",
			obj{"command": "echo ready\ncurl https://example.com"}, nil), why + "shell echo ready\ncurl https://example.com"},
		{"cursor read outside", "cursor", cursorCall("read", "started", "c1",
			obj{"path": "{root}/.atm/runs/old/worker.jsonl"}, nil), why + "read {root}/.atm/runs/old/worker.jsonl"},
		{"cursor write outside", "cursor", cursorCall("write", "started", "c1",
			obj{"path": "{root}/out.txt", "content": "safe"}, nil), why + "write {root}/out.txt"},
		{"cursor write fileText inside started", "cursor", cursorCall("write", "started", "c1",
			obj{"path": "a.txt", "fileText": "see {root}/outside"}, nil), ""},
		{"cursor write fileText inside completed", "cursor", cursorCall("write", "completed", "c1",
			obj{"path": "a.txt", "fileText": "see {root}/outside"}, obj{"success": obj{}}), ""},
		{"cursor write fileText outside started", "cursor", cursorCall("write", "started", "c1",
			obj{"path": "{root}/out.txt", "fileText": "see {root}/outside"}, nil), why + "write {root}/out.txt"},
		{"cursor write fileText outside completed", "cursor", cursorCall("write", "completed", "c1",
			obj{"path": "{root}/out.txt", "fileText": "see {root}/outside"}, obj{"success": obj{}}),
			why + "write {root}/out.txt"},
		{"cursor structured data", "cursor", cursorCall("write", "started", "c1",
			obj{"path": "a.txt", "content": "https://example.com {root}/outside"}, nil), ""},
		{"cursor edit data", "cursor", cursorCall("edit", "started", "c1",
			obj{"path": "a.txt", "oldText": "x", "newText": "https://example.com {root}/outside"}, nil), ""},
		{"cursor shell description", "cursor", cursorCall("shell", "started", "c1",
			obj{"command": "echo ready", "description": "https://example.com"}, nil), ""},
	})
}

func TestCursorEffortRejectedBeforeClone(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	var out bytes.Buffer
	err := Run("t", []string{"--harness", "cursor", "--effort", "high", issueFile(t, issue)}, &out, io.Discard)
	want := "cursor has no effort flag: put it in the model, as '<model>[effort=high]'"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v, want %s", err, want)
	}
	if strings.Contains(out.String(), `"step":"clone"`) {
		t.Fatalf("clone ran: %s", &out)
	}
	if _, err := os.Stat(filepath.Join(root, ".atm", "clones")); !os.IsNotExist(err) {
		t.Fatalf("clone directory created: %v", err)
	}
}
