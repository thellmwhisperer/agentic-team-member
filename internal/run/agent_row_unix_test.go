//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A run is one unit: the real terminal's Agent row names none, and the unit commit is its title line alone.
func TestRealScreenAgentRowHasNoUnit(t *testing.T) {
	builtATM(t)
	delivered := filepath.Join(t.TempDir(), "delivered")
	t.Setenv("ATM_TEST_OUT", delivered)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	cfg := atmSet(atmYAML, "delivery", `git log -1 --format=%B > "$ATM_TEST_OUT"`)
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
	f := startFollow(t, versionServer(t, top, "follow-test"), top, true, "run", issueFile(t, issue))
	var shown strings.Builder
	untilShown(t, f, &shown, "q close")
	f.exit(0)
	if s := ansi.Strip(shown.String()); !strings.Contains(s, "✓ Agent  ") || strings.Contains(s, "unit") {
		t.Fatalf("want the Agent row without a unit:\n%s", s)
	}
	if b, err := os.ReadFile(delivered); err != nil || strings.TrimRight(string(b), "\n") != "chore: Retry on timeout" {
		t.Fatalf("want the unit commit's message one line, got %q, %v", b, err)
	}
}
