//go:build unix

package run

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRunFailsChecksWhenATestFileLosesTests: the real atm fails Checks when the worker deletes a subtest of a
// test file on the base, naming the file; moving that file while dropping a subtest the issue names passes
// (decision 8087).
func TestRunFailsChecksWhenATestFileLosesTests(t *testing.T) {
	const tests = "package b\n\nfunc TestB(t *testing.T) {\n\tt.Run(\"first case\", func(t *testing.T) {})\n" +
		"\tt.Run(\"second case\", func(t *testing.T) {})\n}\n"
	for _, c := range []struct{ name, work, issue, why string }{
		{name: "deleted", work: "sed '/second case/d' b_test.go > b && mv b b_test.go",
			issue: "# Retry\nType: fix\nRetry once.\n", why: "the test file b_test.go loses tests: 3 before, 2 after"},
		{name: "moved without a named subtest", work: "sed '/second case/d' b_test.go > c_test.go && rm b_test.go",
			issue: "# Retry\nType: fix\nRetry once and remove the subtest `second case`.\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("FAKE_AGENT_WORK", fixWork+"; "+c.work)
			cfg := strings.Replace(atmYAML, `test_patterns: ["**/*_test.sh"]`,
				`test_patterns: ["**/*_test.sh", "**/*_test.go"]`, 1)
			top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
			writeFixture(t, filepath.Join(top, "b_test.go"), tests)
			gitT(t, top, "add", "b_test.go")
			gitT(t, top, "commit", "--amend", "--no-edit", "-q")
			bin, outcome, code := followUpsServer(t, top), "passed", 0
			if c.why != "" {
				outcome, code = "failed", 1
			}
			followedRun(t, bin, top, code, issueFile(t, c.issue))
			rep := readReport(t, filepath.Join(runDir(top, lastRun(t, top, outcome)), "report.json"))
			if c.why != "" && (rep["failed_node"] != "checks" || !strings.Contains(rep["reason"].(string), c.why)) {
				t.Fatalf("want Checks to fail naming %q, got %v", c.why, rep)
			}
		})
	}
}
