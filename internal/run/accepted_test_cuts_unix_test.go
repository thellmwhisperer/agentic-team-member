//go:build unix

package run

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAcceptedTestCutsSurviveRepairAndResume(t *testing.T) {
	for _, tc := range []struct {
		name, initial, cut, repair, delivery, why string
		resume                                    bool
	}{
		{name: "delivery fix"},
		{name: "repeated cuts do not double credit", initial: "touch repeat-cut",
			repair: "if test -f repeat-cut; then echo 'func TestRestored(t *testing.T) {}' >> b_test.go; " +
				"else sed '/survivor/d' b_test.go > b; mv b b_test.go; fi", why: "3 before, 2 after"},
		{name: "saved repair resumes", resume: true},
		{name: "worker deletes another subtest", repair: "sed '/survivor/d' b_test.go > b; mv b b_test.go",
			why: "3 before, 2 after", resume: true},
		{name: "delivery commits another deletion", delivery: "sed \"/survivor/d\" b_test.go > b; mv b b_test.go; " +
			"git add -A; git commit -qm delivery-deletion; ", why: "3 before, 2 after", resume: true},
		{name: "repair renames the cut file", initial: "echo extra >> a.txt",
			cut:    "echo fixed > a.txt; sed '/cut me/d' b_test.go > b; mv b b_test.go",
			repair: "mv b_test.go moved_test.go", resume: true},
		{name: "worker renames before the cut", initial: "mv b_test.go moved_test.go",
			cut: "sed '/cut me/d' moved_test.go > b; mv b moved_test.go", resume: true},
		{name: "accepted whole file deletion", cut: "rm b_test.go", resume: true},
		{name: "cutting worker additions gives no credit", initial: "echo 'func TestExtra(t *testing.T) {}' >> b_test.go",
			cut:    "sed '/TestExtra/d' b_test.go > b; mv b b_test.go",
			repair: "sed '/survivor/d' b_test.go > b; mv b b_test.go", why: "4 before, 3 after", resume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runAcceptedTestCut(t, tc.initial, tc.cut, tc.repair, tc.delivery, tc.why, tc.resume)
		})
	}
}

func runAcceptedTestCut(t *testing.T, initial, cut, repair, delivery, why string, resume bool) {
	t.Helper()
	const tests = `package b
import "testing"
func TestB(t *testing.T) {
	t.Run("cut me", func(t *testing.T) {})
	t.Run("survivor", func(t *testing.T) {})
	t.Run("another", func(t *testing.T) {})
}
`
	ready, delivered := filepath.Join(t.TempDir(), "ready"), filepath.Join(t.TempDir(), "delivered")
	t.Setenv("ATM_TEST_READY", ready)
	t.Setenv("ATM_TEST_DELIVERED", delivered)
	t.Setenv("FAKE_AGENT_WORK", "if test -f first-turn; then touch repaired; "+repair+"\nelse "+
		fixWork+"; touch first-turn; echo '// worker' >> b_test.go; "+initial+"\nfi")
	if cut == "" {
		cut = "sed '/cut me/d' b_test.go > b; mv b b_test.go"
	}
	t.Setenv("FAKE_PONYTAIL_WORK", "if test ! -f repaired; then "+cut+"; "+
		"elif test -f repeat-cut; then sed '/TestRestored/d' b_test.go > b; mv b b_test.go; fi")
	file := "b_test.go"
	if strings.Contains(initial, "mv b_test.go") {
		file = "moved_test.go"
	}
	if strings.HasPrefix(repair, "mv b_test.go") {
		file = "a.txt"
	}
	t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts(file))
	cfg := strings.Replace(atmYAML, `test_patterns: ["**/*_test.sh"]`,
		`test_patterns: ["**/*_test.sh", "**/*_test.go"]`, 1)
	cfg = atmSet(cfg, "lint", `test ! -f repaired || test -f "$ATM_TEST_READY" || { echo repair-lint; exit 1; }`)
	cfg = atmSet(cfg, "delivery", "if test -f repaired && test -f repeat-cut; then rm repeat-cut; "+
		"echo delivery-finding; exit 1; fi; test -f repaired || { "+delivery+"echo delivery-finding; exit 1; }; "+
		`for f in $(git ls-files "*_test.go"); do cat "$f"; done > "$ATM_TEST_DELIVERED"`)
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	writeFixture(t, filepath.Join(top, "b_test.go"), tests)
	gitT(t, top, "add", "b_test.go")
	gitT(t, top, "commit", "--amend", "--no-edit", "-q")
	base := gitT(t, top, "rev-parse", "HEAD")
	finishAcceptedTestCut(t, top, base, ready, delivered, cut, why, resume, initial == "touch repeat-cut")
}

func finishAcceptedTestCut(t *testing.T, top, base, ready, delivered, cut, why string, resume, repeat bool) {
	t.Helper()
	if !resume {
		writeFixture(t, ready, "ready")
	}
	bin := followUpsServer(t, top)
	_, label := question(t, bin, top, "delivery-finding", 4, "run", "--follow", issueFile(t, issue))
	if kept := e2eReport(t, top, label).node("ponytail").Kept; kept == nil || !*kept {
		t.Fatal("the regression requires an accepted ponytail cut")
	}
	code := 0
	if resume || why != "" {
		code = 1
	}
	if repeat {
		answerFailedCheck(t, bin, top, label, "fix", 4)
	}
	answerFailedCheck(t, bin, top, label, "fix", code)
	if resume {
		want := why
		if want == "" {
			want = "repair-lint"
		}
		if rep := e2eReport(t, top, label); rep.FailedNode != "checks" || !strings.Contains(rep.Reason, want) {
			t.Fatalf("repair failed for the wrong reason: want %q, got %+v", want, rep)
		}
		answerFailedCheck(t, bin, top, label, "stop", 1)
		writeFixture(t, ready, "ready")
		code = 0
		if why != "" {
			code = 1
		}
		followedRun(t, bin, top, code, "--resume", label)
		label = lastRun(t, top, map[bool]string{true: "failed", false: "passed"}[code != 0])
	}
	assertAcceptedTestCut(t, top, label, base, delivered, cut, why)
}

func assertAcceptedTestCut(t *testing.T, top, label, base, delivered, cut, why string) {
	t.Helper()
	rep := e2eReport(t, top, label)
	if len(rep.Units) != 1 || rep.Units[0].Base != base {
		t.Fatalf("lost original base: %+v", rep)
	}
	if why != "" {
		if rep.FailedNode != "checks" || !strings.Contains(rep.Reason, "b_test.go loses tests: "+why) {
			t.Fatalf("unrelated deletion escaped Checks: %+v", rep)
		}
	} else if rep.FailedNode != "" || strings.Contains(reader(t, filepath.Dir(delivered))("delivered"), "cut me") &&
		!strings.Contains(cut, "TestExtra") {
		t.Fatalf("accepted cut did not reach delivery: %+v", rep)
	}
}
