package run

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

func TestHelloFailsOnCursorErrorResultAfterOK(t *testing.T) {
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	okMessage := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"OK"}]}}`
	t.Setenv("FAKE_AGENT_STREAM", okMessage+"\n"+
		`{"type":"result","is_error":true,"result":"rate limited"}`+"\n")
	_, err := Hello(config.Agent{Harness: "cursor", Model: "test-model"}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("want doctor hello to propagate Cursor's error result after OK, got %v", err)
	}
}
