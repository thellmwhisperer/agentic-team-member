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
	const loss = "the test file b_test.go loses tests: 3 before, 2 after"
	const drop = "sed '/second case/d' b_test.go > b && mv b b_test.go"
	const nested = "package b\nfunc TestCheckout(t *testing.T) { t.Run(\"outer\", func(t *testing.T) { t.Run(\"inner\", func(t *testing.T) {}) }) }\n"
	for _, c := range []struct{ name, work, issue, why, base string }{
		{name: "deleted", work: "sed '/second case/d' b_test.go > b && mv b b_test.go",
			issue: "# Retry\nType: fix\nRetry once.\n", why: "the test file b_test.go loses tests: 3 before, 2 after"},
		{name: "moved without a named subtest", work: "sed '/second case/d' b_test.go > c_test.go && rm b_test.go",
			issue: "# Retry\nType: fix\nRetry once and remove the subtest `second case`.\n"},
		{name: "subtest prefix", work: drop, issue: "Remove the subtest `second case longer`.", why: loss},
		{name: "incidental mention", work: drop, issue: "Investigate `second case`.", why: loss},
		{name: "preserve", work: drop, issue: "Preserve the subtest `second case`.", why: loss},
		{name: "acceptance", work: drop, issue: "Acceptance: `second case` passes.", why: loss},
		{name: "negation", work: drop, issue: "Do not remove the subtest `second case`.", why: loss},
		{name: "commented call", work: "sed 's|t.Run(\"second|//t.Run(\"second|' b_test.go > b && mv b b_test.go", why: loss},
		{name: "adapted", work: "sed 's/second case/adapted case/' b_test.go > b && mv b b_test.go"},
		{name: "literal and comment consolidation", base: tests + "var fixture = `\nfunc TestFixture() {}\nt.Run(\"fixture\", f)`\n// t.Run(\"comment\", f)\n",
			work: "sed '/var fixture/,$d' b_test.go > b && mv b b_test.go"},
		{name: "function prefix", base: nested, work: "rm b_test.go", issue: "Remove TestCheckoutTimeout.",
			why: "the test file b_test.go loses tests: 3 before, 0 after"},
		{name: "commented function", base: nested, work: "sed 's|^func|//func|' b_test.go > b && mv b b_test.go", why: "the test file b_test.go loses tests: 3 before, 0 after"},
		{name: "authorized enclosing function", base: nested, work: "rm b_test.go", issue: "- Remove TestCheckout."},
		{name: "authorized enclosing subtest", base: nested,
			work: "printf 'package b\nfunc TestCheckout(t *testing.T) {}\n' > b_test.go", issue: "Delete subtest \"TestCheckout/outer\"."},
		{name: "function removal sentence", base: nested + "func TestCurrent(t *testing.T) {}\n", work: "sed '/TestCheckout/d' b_test.go > b && mv b b_test.go",
			issue: "Remove TestCheckout. Keep TestCurrent."},
		{name: "qualified removal sentence", base: nested, work: "printf 'package b\nfunc TestCheckout(t *testing.T) {}\n' > b_test.go",
			issue: "Delete subtest \"TestCheckout/outer\". Keep TestCheckout."},
		{name: "subtest removal sentence", work: drop, issue: "Remove subtest `second case`. Keep `first case`."},
		{name: "preserved target after removal sentence", work: drop, issue: "Remove subtest `first case`. Keep `second case`.",
			why: "the test file b_test.go loses tests: 2 before, 1 after"},
		{name: "consecutive removal sentences", work: "sed '/t.Run/d' b_test.go > b && mv b b_test.go",
			issue: "Remove subtest `first case`. Remove subtest `second case`. Keep TestB."},
		{name: "dynamic subtest", base: strings.Replace(tests, `"second case"`, "caseName", 1), work: "sed '/caseName/d' b_test.go > b && mv b b_test.go", why: loss},
		{name: "new file", work: "cp b_test.go new_test.go"},
		{name: "invalid Go", work: "echo 'func' >> b_test.go", why: "count tests in b_test.go:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("FAKE_AGENT_WORK", fixWork+"; "+c.work)
			cfg := strings.Replace(atmYAML, `test_patterns: ["**/*_test.sh"]`,
				`test_patterns: ["**/*_test.sh", "**/*_test.go"]`, 1)
			top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
			base := c.base
			if base == "" {
				base = tests
			}
			writeFixture(t, filepath.Join(top, "b_test.go"), base)
			gitT(t, top, "add", "b_test.go")
			gitT(t, top, "commit", "--amend", "--no-edit", "-q")
			bin, outcome, code := followUpsServer(t, top), "passed", 0
			if c.why != "" {
				outcome, code = "failed", 1
			}
			text := c.issue
			if !strings.HasPrefix(text, "# Retry") {
				text = "# Retry\nType: fix\n" + text
			}
			followedRun(t, bin, top, code, issueFile(t, text))
			rep := readReport(t, filepath.Join(runDir(top, lastRun(t, top, outcome)), "report.json"))
			if c.why != "" && (rep["failed_node"] != "checks" || !strings.Contains(rep["reason"].(string), c.why)) {
				t.Fatalf("want Checks to fail naming %q, got %v", c.why, rep)
			}
		})
	}
}

func TestRunLostTestsAfterDeliveryCommitAndResume(t *testing.T) {
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	cfg := strings.Replace(atmYAML, `test_patterns: ["**/*_test.sh"]`, `test_patterns: ["**/*_test.sh", "**/*_test.go"]`, 1)
	cfg = atmSet(cfg, "delivery", "rm b_test.go; git add -A; git commit -qm removed; echo delivery-finding; exit 1")
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	writeFixture(t, filepath.Join(top, "b_test.go"), "package b\nfunc TestKept(t *testing.T) {}\n")
	gitT(t, top, "add", "b_test.go")
	gitT(t, top, "commit", "--amend", "--no-edit", "-q")
	base := gitT(t, top, "rev-parse", "HEAD")
	bin := followUpsServer(t, top)
	clone, label := question(t, bin, top, "delivery-finding", 4, "run", "--follow", issueFile(t, issue))
	if gitT(t, clone, "rev-parse", "HEAD") == base {
		t.Fatal("delivery did not commit the deletion")
	}
	answerFailedCheck(t, bin, top, label, "fix", 1)
	const why = "the test file b_test.go loses tests: 1 before, 0 after"
	if rep := e2eReport(t, top, label); rep.FailedNode != "checks" || !strings.Contains(rep.Reason, why) {
		t.Fatalf("repair missed committed deletion: %+v", rep)
	}
	answerFailedCheck(t, bin, top, label, "stop", 1)
	followedRun(t, bin, top, 1, "--resume", label)
	rep := e2eReport(t, top, lastRun(t, top, "failed"))
	if rep.FailedNode != "checks" || !strings.Contains(rep.Reason, why) || len(rep.Units) != 1 || rep.Units[0].Base != base {
		t.Fatalf("resume lost original base or missed committed deletion: %+v", rep)
	}
}
