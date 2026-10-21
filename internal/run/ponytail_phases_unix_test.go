//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The real terminal must unfold the Slop detector row into its phases and list every finding the ponytail agent
// reported with its verdict: the run deletes old.txt, and the cut that brings it back adds code.
func TestRealScreenShowsSlopDetectorPhases(t *testing.T) {
	builtATM(t)
	top := atmRepo(t)
	if err := os.WriteFile(filepath.Join(top, "old.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, top, "add", "old.txt")
	gitT(t, top, "commit", "-q", "--amend", "--no-edit")
	gitT(t, top, "update-ref", "refs/remotes/origin/main", "main")
	t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest+"; rm old.txt")
	t.Setenv("FAKE_PONYTAIL_WORK", "echo fixed > a.txt; echo old > old.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts("a.txt", "old.txt"))
	f, shown := realScreen(t, top)
	// A 100-column terminal leaves the ATM box 36: a row's note shows its start.
	for _, want := range []string{"✓ agent reading", "✓ 2 findings", "✓ judged", "1 kept", "✓ checks", "run 1",
		"✓ commit", "kept · a.txt", "cut a.txt", "rejected: adds code · old.txt", "cut old.txt"} {
		untilShown(t, f, shown, want)
	}
	f.exit(0)
}

// realScreen is real ATM's run of issue in top on a 100-column terminal, and what the terminal showed so far.
func realScreen(t *testing.T, top string) (*followed, *strings.Builder) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	return startFollow(t, versionServer(t, top, "follow-test"), top, true, "run", issueFile(t, issue)),
		&strings.Builder{}
}
