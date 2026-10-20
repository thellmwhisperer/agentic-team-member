package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// redOnBase says the red runs with the source reverted: in Go a test naming a function the worker is about to add
// does not compile there.
const redOnBase = "   ATM runs the red test with your source changes reverted to the base commit, " +
	"so the test may only name\n" +
	"   identifiers that already exist there; reach new behaviour at runtime through an existing entry " +
	"point, and\n" +
	"   check it by building or vetting with only the test file changed.\n"

// TestBriefRedRequiresExecution pins the whole rendered ACCEPTANCE section for fix, feature and greenfield: a
// missing symbol or module is a red only at runtime, the red must parse/compile and reach an assertion, as Checks
// enforces, and for fix and feature it runs on the base commit.
func TestBriefRedRequiresExecution(t *testing.T) {
	for typ, steps := range map[string]string{
		"fix": "1. Write a test on the existing code that fails today on behaviour, not on a missing symbol or " +
			"module.\n" +
			"   The fix may add files: the test must still fail with those files present and the fix reverted.\n" +
			redOnBase +
			"2. Fix the source. The test passes.\n",
		"feature": "1. Write a test that fails today, possibly on a missing symbol at runtime. " +
			"It must parse/compile and reach\n" +
			"   an assertion: a parse, compile or load error before any assertion is not a red.\n" +
			redOnBase +
			"2. Implement the feature. The test passes.\n",
		"greenfield": "1. Write a test that fails today, possibly on a missing module at runtime. " +
			"It must parse/compile and reach\n" +
			"   an assertion: a parse, compile or load error before any assertion is not a red.\n" +
			"2. Build it. The test passes.\n",
	} {
		t.Run(typ, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", briefYAML)
			if err := Run("t", []string{issueFile(t, "# Retry\nRetry once.\n\nType: "+typ+"\n")}, &bytes.Buffer{},
				io.Discard); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "brief.md"))
			if err != nil {
				t.Fatal(err)
			}
			_, acceptance, _ := strings.Cut(strings.ReplaceAll(string(b), "\r\n", "\n"), "## ACCEPTANCE\n")
			acceptance, _, _ = strings.Cut(acceptance, "\n## VERIFY\n")
			if want := "1. Implement the issue and cover the changed behaviour with tests.\n" +
				"2. Then every command in VERIFY passes.\n"; acceptance != want {
				t.Errorf("want uniform acceptance %q, got %q", want, acceptance)
			}
			if acceptance == steps || strings.Contains(acceptance, redOnBase) {
				t.Errorf("ACCEPTANCE retains removed steps:\n%s\nold:\n%s", acceptance, steps)
			}
		})
	}
}
