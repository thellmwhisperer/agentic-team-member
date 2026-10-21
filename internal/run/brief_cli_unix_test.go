//go:build unix

package run

import (
	"path/filepath"
	"strings"
	"testing"
)

// Decision 8087 is a contract in the prompt delivered by the real atm binary.
func TestBriefRuleReachesWorkerCLI(t *testing.T) {
	bin := builtATM(t)
	root := repo(t, "local", atmYAML)
	stopServing(t, root)
	call := filepath.Join(t.TempDir(), "call.json")
	t.Setenv("FAKE_AGENT_CALL", call)
	f := startFollow(t, bin, root, false, "run", "--follow", issueFile(t,
		"# Retry\nRetry once. Remove obsolete_test.sh.\n\nType: fix\n"))
	f.exit(0)

	worker := agentCall(t, call).Stdin
	_, forbidden, ok := strings.Cut(worker, "## FORBIDDEN\n")
	forbidden, _, _ = strings.Cut(forbidden, "\n## REPORT")
	for _, want := range []string{
		"Deleting or shortening an existing test, subtest or assertion",
		"A test the change invalidates is updated to\n  expect the new behaviour",
		"The only exception is a test the issue names as one to remove",
	} {
		if !ok || !strings.Contains(forbidden, want) {
			t.Fatalf("worker FORBIDDEN lacks %q:\n%s", want, forbidden)
		}
	}
	if !strings.Contains(worker, "Remove obsolete_test.sh.") {
		t.Fatal("worker did not receive the issue that names a test to remove")
	}
	ponytail := agentCall(t, call+".ponytail").Stdin
	if !strings.Contains(ponytail, "You may only cut: delete, shrink") ||
		strings.Contains(ponytail, "Deleting or shortening an existing test") {
		t.Fatalf("ponytail contract changed:\n%s", ponytail)
	}
	_, cutRule, _ := strings.Cut(ponytail, "- You may only cut: ")
	cutRule, _, _ = strings.Cut(cutRule, "\n")
	t.Logf("worker FORBIDDEN:\n%s\nponytail cut rule: %s", forbidden, cutRule)
}
