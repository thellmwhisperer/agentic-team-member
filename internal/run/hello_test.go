package run

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

func TestHelloFailsOnCursorErrorResultAfterOK(t *testing.T) {
	const ok = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"OK"}]}}` + "\n"
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_AGENT_WORK", "mkdir -p .atm; printf OK > .atm/fake-report.json")
	for _, tc := range []struct {
		name, result, want string
	}{
		{"error", `{"type":"result","is_error":true,"result":"rate limited"}`, "rate limited"},
		{"success", `{"type":"result","is_error":false,"result":"OK"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_AGENT_STREAM", ok+tc.result+"\n")
			_, err := Hello(config.Agent{Harness: "cursor"}, 5*time.Second)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Hello error = %v, want %q", err, tc.want)
			}
		})
	}
}
