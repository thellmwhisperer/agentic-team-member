//go:build unix

package run

import (
	"path/filepath"
	"strings"
	"testing"
)

// Ordinary calls are not tests: removing them must pass, and keeping them cannot hide a lost subtest.
func TestRunTestInventoryIgnoresNonSubtestCalls(t *testing.T) {
	const tests = `package b
import "testing"
type job struct{}
func (job) Run(name string, task func()) { task() }
func ordinary(name string, task func()) { task() }
func TestKept(t *testing.T) {
	t.Run("kept", func(t *testing.T) {})
	ordinary("helper", func() {}) // ordinary call
	t.Log("helper") // ordinary call
	worker := job{} // ordinary call
	worker.Run("helper", func() {}) // ordinary call
	job{}.Run("helper", func() {}) // ordinary call
}
`
	for _, c := range []struct{ name, work, outcome, why string }{
		{name: "remove ordinary calls", work: "sed '/ordinary call/d' b_test.go > b && mv b b_test.go",
			outcome: "passed"},
		{name: "keep ordinary calls but remove subtest", work: "sed '/t.Run/d' b_test.go > b && mv b b_test.go",
			outcome: "failed", why: "the test file b_test.go loses tests: 2 before, 1 after"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("FAKE_AGENT_WORK", fixWork+"; "+c.work)
			cfg := strings.Replace(atmYAML, `test_patterns: ["**/*_test.sh"]`,
				`test_patterns: ["**/*_test.sh", "**/*_test.go"]`, 1)
			top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
			writeFixture(t, filepath.Join(top, "b_test.go"), tests)
			gitT(t, top, "add", "b_test.go")
			gitT(t, top, "commit", "--amend", "--no-edit", "-q")
			code := 0
			if c.why != "" {
				code = 1
			}
			followedRun(t, followUpsServer(t, top), top, code, issueFile(t, issue))
			rep := e2eReport(t, top, lastRun(t, top, c.outcome))
			if c.why != "" && (rep.FailedNode != "checks" || !strings.Contains(rep.Reason, c.why)) {
				t.Fatalf("want Checks to fail naming %q, got %+v", c.why, rep)
			}
			t.Logf("atm report: outcome=%s failed_node=%s reason=%s", c.outcome, rep.FailedNode, rep.Reason)
		})
	}
}
