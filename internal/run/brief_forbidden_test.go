package run

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// runEndingHabits are the worker brief's FORBIDDEN lines on shell writes, URLs and background commands.
var runEndingHabits = []string{
	"- Writing or changing files through shell commands (heredocs, `cat >`, `sed -i`, `python -c` and the like). " +
		"Use your edit and write tools.\n",
	"- A URL inside any shell command. ATM ends the run on it, even with no network access. " +
		"A URL may appear only in files written with your edit tools.\n",
	"- Running a command in the background, or ending your turn while one runs. " +
		"Run every command in the foreground. Your turn ends only with the report.\n",
}

// TestBriefForbidsTheHabitsThatEndARunWithNoReport: shell file writes, URLs in commands and background commands.
func TestBriefForbidsTheHabitsThatEndARunWithNoReport(t *testing.T) {
	issue := issueFile(t, "# Retry\nRetry once.\n\nType: fix\n")
	path := filepath.Join(repo(t, "https://example.com/owner/repo.git", briefYAML), ".atm", "runs", "t", "brief.md")
	if err := Run("t", []string{issue}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	brief := strings.ReplaceAll(string(b), "\r\n", "\n")
	forbidden := brief[strings.Index(brief, "## FORBIDDEN\n"):strings.Index(brief, "## FOLLOW-UPS")]
	for _, line := range runEndingHabits {
		if !strings.Contains(forbidden, line) {
			t.Errorf("FORBIDDEN lacks %q:\n%s", line, forbidden)
		}
	}
}

// TestPonytailBriefForbidsTheHabitsThatEndARunWithNoReport renders the ponytail brief and requires the worker
// brief's three FORBIDDEN lines, verbatim: the same guard watches that agent.
func TestPonytailBriefForbidsTheHabitsThatEndARunWithNoReport(t *testing.T) {
	got, err := ponytailBrief(Issue{Title: "Retry", Body: "Retry once.\n", Type: "fix"}, config.Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	brief := strings.ReplaceAll(got, "\r\n", "\n")
	forbidden := brief[strings.Index(brief, "## FORBIDDEN\n"):strings.Index(brief, "## DIFF")]
	for _, line := range runEndingHabits {
		if !strings.Contains(forbidden, line) {
			t.Errorf("FORBIDDEN lacks %q:\n%s", line, forbidden)
		}
	}
}
