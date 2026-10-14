package run

import (
	"bytes"
	"io"
	"path/filepath"
	"slices"
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
				claude := harness == "claude"
				if slices.Contains(env, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1") != claude ||
					slices.Contains(env, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=0") == claude {
					t.Fatalf("%s%s env: want CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1 only for claude", harness, pass)
				}
			}
		})
	}
}
