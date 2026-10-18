package run

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestCursorErrorResultKeepsReachPrecedence(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("FAKE_AGENT", "fail")
	t.Setenv("FAKE_AGENT_STREAM", cursorCall("shell", "started", "c1", obj{"command": "touch /tmp/atm-reach.txt"}, nil)+
		`{"type":"result","is_error":true,"result":"rate limited"}`+"\n")
	var out bytes.Buffer
	err := Run("t", []string{"--harness", "cursor", issueFile(t, issue)}, &out, io.Discard)
	want := "exit code 3; the agent reached outside its clone: shell touch /tmp/atm-reach.txt"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("want reach first, got %v", err)
	}
}
