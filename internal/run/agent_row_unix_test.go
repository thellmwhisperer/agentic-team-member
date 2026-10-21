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

func TestRealScreenAgentFailureKeepsUnitInFindings(t *testing.T) {
	for _, tc := range []struct {
		name, check, reference string
	}{
		{"agent", "", "E Agent · unit 1"},
		{"command", "validation", "E Agent · unit 1 · validation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := liveAttach(t, 120, 40)
			a.send(`{"step":"agent","state":"started"}`)
			if tc.check != "" {
				a.send(`{"step":"agent","check":"` + tc.check + `","state":"failed"}`)
			}
			a.send(`{"step":"agent","state":"failed","duration_ms":1000,"error":"agent failed"}`)
			s := a.drawn(func(s string) bool { return strings.Contains(s, "Findings - E 1") })
			for _, want := range []string{"✗ Agent  1.0 s", tc.reference, "agent failed"} {
				if !strings.Contains(s, want) {
					t.Fatalf("want %q on the failed agent screen:\n%s", want, s)
				}
			}
			a.end()
		})
	}
}
