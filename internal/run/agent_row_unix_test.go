//go:build unix

package run

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The real atm run on a terminal: a run is one unit, so its Agent row names none and its unit commit is the
// chore line alone.
func TestRealScreenAgentRowHasNoUnit(t *testing.T) {
	got := t.TempDir()
	t.Setenv("ATM_TEST_OUT", got)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	top := gitT(t, repo(t, "https://example.com/o/r.git", atmSet(atmYAML, "delivery", deliveryLine)),
		"rev-parse", "--show-toplevel")
	stopServing(t, top)
	f := startFollow(t, builtATM(t), top, true, "run", issueFile(t, issue))
	var shown strings.Builder
	for l := range f.lines {
		shown.WriteString(l + "\n")
	}
	if err := f.cmd.Wait(); err != nil {
		t.Fatalf("atm run: %v\n%s", err, f.errors())
	}
	if s := ansi.Strip(shown.String()); !strings.Contains(s, "✓ Agent") ||
		regexp.MustCompile(`Agent[^│\n]*unit`).MatchString(s) {
		t.Fatalf("the Agent row names a unit:\n%s", s)
	}
	if msg := gitT(t, reader(t, got)("ATM_CLONE"), "log", "-1", "--format=%B"); msg != "chore: Retry on timeout" {
		t.Fatalf("want the unit commit's message one line, got %q", msg)
	}
}
