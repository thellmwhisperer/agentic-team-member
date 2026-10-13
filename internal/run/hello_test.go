package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

func TestHelloFailsOnCursorErrorResultAfterOK(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"OK"}]}}'
printf '%s\n' '{"type":"result","is_error":true,"result":"rate limited"}'
`
	if err := os.WriteFile(filepath.Join(bin, "cursor-agent"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err := Hello(config.Agent{Harness: "cursor", Model: "test-model"}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("want doctor hello to propagate Cursor's error result after OK, got %v", err)
	}
}
