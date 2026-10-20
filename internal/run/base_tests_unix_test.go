//go:build unix

package run

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRunAllowsDeletedOrShorterBaseTests: the guard on base tests is retired (decision 7987). The real atm
// delivers a feature whose worker deletes a_test.sh, a test on the base, or shortens it, when the configured
// suite passes; a configured suite that fails still stops it at Checks, before delivery.
func TestRunAllowsDeletedOrShorterBaseTests(t *testing.T) {
	for _, c := range []struct {
		name, work       string
		kept, suiteFails bool
	}{
		{name: "deleted", work: "echo fixed > a.txt; rm a_test.sh" + reportTest("regression_test.sh")},
		{name: "shorter", work: "echo fixed > a.txt; : > a_test.sh" + reportTest("regression_test.sh"),
			kept: true},
		{name: "deleted with a failing suite", suiteFails: true,
			work: "echo fixed > a.txt; rm a_test.sh" + reportTest("regression_test.sh")},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := builtATM(t)
			fails, delivered := filepath.Join(t.TempDir(), "fails"), filepath.Join(t.TempDir(), "delivered")
			t.Setenv("ATM_TEST_SUITE_FAILS", fails)
			t.Setenv("ATM_TEST_OUT", delivered)
			t.Setenv("FAKE_AGENT_WORK", c.work)
			if c.suiteFails {
				writeFixture(t, fails, "")
			}
			cfg := atmSet(atmSet(atmYAML, "test", `test ! -f "$ATM_TEST_SUITE_FAILS" && `+suite),
				"delivery", `ls > "$ATM_TEST_OUT"`)
			top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
			if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
				t.Fatal(err)
			}
			server := startVersionServe(t, bin, top)
			t.Cleanup(func() { endServe(bin, top, server) })
			issue := issueFile(t, "# Retry\nType: feature\nRetry once.\n")
			if c.suiteFails {
				followedRun(t, bin, top, 1, issue)
				rep := readReport(t, filepath.Join(runDir(top, lastRun(t, top, "failed")), "report.json"))
				if rep["failed_node"] != "checks" || !strings.HasPrefix(rep["reason"].(string), "test: ") {
					t.Fatalf("want the configured suite to stop Checks, got %v", rep)
				}
				if _, err := os.Stat(delivered); !os.IsNotExist(err) {
					t.Fatalf("a failing suite delivered: %v", err)
				}
				return
			}
			followedRun(t, bin, top, 0, issue)
			lastRun(t, top, "passed")
			b, err := os.ReadFile(delivered)
			if err != nil {
				t.Fatalf("nothing delivered: %v", err)
			}
			files := strings.Fields(string(b))
			if !slices.Contains(files, "regression_test.sh") || slices.Contains(files, "a_test.sh") != c.kept {
				t.Fatalf("want the %s base test delivered as the worker left it, got %v", c.name, files)
			}
		})
	}
}
