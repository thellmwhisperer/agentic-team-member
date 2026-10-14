package run

import (
	"bytes"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRunDisablesClaudeBackgroundTasks(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			repo(t, "https://example.com/owner/repo.git", atmYAML)
			call := filepath.Join(t.TempDir(), "call.json")
			t.Setenv("FAKE_AGENT_CALL", call)
			t.Setenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "0")
			var out bytes.Buffer
			if err := Run("t", []string{"--harness", harness, issueFile(t, issue)}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			for _, pass := range []string{"", ".ponytail"} {
				env := agentCall(t, call+pass).Env
				var got []string
				for _, kv := range env {
					if strings.HasPrefix(kv, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=") {
						got = append(got, kv)
					}
				}
				want := []string{"CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=0"}
				if harness == "claude" {
					want[0] = "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1"
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%s%s agent env: got %v, want %v", harness, pass, got, want)
				}
				t.Logf("%s%s agent env: %s", harness, pass, got[0])
			}
		})
	}
}
