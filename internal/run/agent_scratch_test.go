package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentScratchHoldsWhatTheWorkerSetsAside: the implementing agent's $TMPDIR is a scratch directory its
// brief names, and a file it writes there leaves the agent step passed.
func TestAgentScratchHoldsWhatTheWorkerSetsAside(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	call := filepath.Join(t.TempDir(), "call.json")
	t.Setenv("FAKE_AGENT_CALL", call)
	t.Setenv("FAKE_WORKER_TMPDIR", "1")
	var out bytes.Buffer
	err := Run("t", []string{"--harness", "claude", issueFile(t, issue)}, &out, io.Discard)
	if end, _, _ := agentStep(t, &out); end["state"] != "passed" || err != nil {
		t.Fatalf("want the agent step passed, got %v; %v", end, err)
	}
	worker, scratch := agentCall(t, call), ""
	for _, kv := range worker.Env {
		if v, ok := strings.CutPrefix(kv, "TMPDIR="); ok {
			scratch = v
		}
	}
	if scratch == "" || !strings.Contains(worker.Stdin, "`"+scratch+"`") {
		t.Fatalf("want the worker's brief to name its TMPDIR %q as its scratch, got:\n%s", scratch, worker.Stdin)
	}
	if _, err := os.Stat(filepath.Join(scratch, "test.log")); err != nil {
		t.Fatalf("want test.log under the worker's scratch: %v", err)
	}
}
