package run

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failedUnit fails the test unless out holds one unit whose checks failed naming why, with no follow-up handling,
// and run label of root wrote no follow-ups.json.
func failedUnit(t *testing.T, root, label string, out *bytes.Buffer, why string) {
	t.Helper()
	agents, checks := stateEnds(t, out, "agent"), stateEnds(t, out, "checks")
	if len(agents) != 1 || len(checks) != 1 || checks[0]["state"] != "failed" || checks[0]["follow_ups"] != nil ||
		!strings.Contains(fmt.Sprint(checks[0]["error"]), why) || strings.Contains(fmt.Sprint(checks[0]["error"]), "follow") {
		t.Fatalf("want the issue's unit failed at checks on %q alone, got %v, %v", why, agents, checks)
	}
	noFollowUpsFile(t, root, label)
}

func TestResumeKeepsAFailedUnitsFollowUpsUntilItsReportReplacesThem(t *testing.T) {
	delivered := filepath.Join(t.TempDir(), "delivered")
	t.Setenv("ATM_TEST_OUT", delivered)
	deliver := atmSet(atmYAML, "delivery", `git show HEAD:a.txt > "$ATM_TEST_OUT"`)
	root := repo(t, "https://example.com/owner/repo.git", atmSet(deliver, "lint", "false"))
	if err := Run("failed", []string{issueFile(t, followUpIssue)}, io.Discard, io.Discard); err == nil {
		t.Fatal("want the unit's checks to fail")
	}
	gitT(t, root, "config", "--global", "core.autocrlf", "true")
	commitConfig(t, root, deliver)
	if err := Run("resumed", []string{"--resume", "failed"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(delivered); err != nil || string(b) != "fixed\n" {
		t.Fatalf("want the kept work delivered without line-ending conversion, got %q: %v", b, err)
	}
}
