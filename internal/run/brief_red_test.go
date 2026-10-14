package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBriefRedRequiresExecution pins the whole rendered ACCEPTANCE section for feature and greenfield: a missing
// symbol or module is a red only at runtime, and the red must parse/compile and reach an assertion, as Checks
// enforces.
func TestBriefRedRequiresExecution(t *testing.T) {
	for typ, steps := range map[string]string{
		"feature": "1. Write a test that fails today, possibly on a missing symbol at runtime. " +
			"It must parse/compile and reach\n" +
			"   an assertion: a parse, compile or load error before any assertion is not a red.\n" +
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
			acceptance, _, _ = strings.Cut(acceptance, "3. Then every command in VERIFY passes.")
			if acceptance != steps {
				t.Errorf("ACCEPTANCE steps:\n%s\nwant:\n%s", acceptance, steps)
			}
		})
	}
}
