//go:build unix

package run

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHelperCutAfterRenamePreservesTestRequirement(t *testing.T) {
	for _, erase := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve tests", true: "delete tests"}[erase], func(t *testing.T) {
			repair, code := "touch repaired", 0
			if erase {
				repair, code = "touch repaired; printf 'package b\\n' > moved_test.go", 1
			}
			t.Setenv("FAKE_AGENT_WORK", "if test -f first-turn; then "+repair+"; else "+
				fixWork+"; mv b_test.go moved_test.go; touch first-turn; fi")
			t.Setenv("FAKE_PONYTAIL_WORK", "sed '/^func helper/,$d' moved_test.go > b; mv b moved_test.go")
			t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts("moved_test.go"))
			cfg := strings.Replace(atmYAML, `test_patterns: ["**/*_test.sh"]`,
				`test_patterns: ["**/*_test.sh", "**/*_test.go"]`, 1)
			cfg = atmSet(cfg, "delivery", "test -f repaired || { echo delivery-finding; exit 1; }")
			top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
			writeFixture(t, filepath.Join(top, "b_test.go"), "package b\nimport \"testing\"\n"+
				"func TestB(t *testing.T) {\n"+strings.Repeat("t.Run(\"case\", func(t *testing.T) {})\n", 3)+
				"}\nfunc helper() {\n"+strings.Repeat("println(\"large helper implementation\")\n", 80)+"}\n")
			gitT(t, top, "add", "b_test.go")
			gitT(t, top, "commit", "--amend", "--no-edit", "-q")
			base := gitT(t, top, "rev-parse", "HEAD")
			bin := followUpsServer(t, top)
			clone, label := question(t, bin, top, "delivery-finding", 4, "run", "--follow", issueFile(t, issue))
			diff := gitT(t, clone, "diff", "--name-status", "-M", base, "HEAD", "--", "*_test.go")
			if diff != "D\tb_test.go\nA\tmoved_test.go" {
				t.Fatalf("fixture did not cross Git's rename threshold: %q", diff)
			}
			if kept := e2eReport(t, top, label).node("ponytail").Kept; kept == nil || !*kept {
				t.Fatal("the helper cut must be accepted")
			}
			answerFailedCheck(t, bin, top, label, "fix", code)
			if erase {
				assertAcceptedTestCut(t, top, label, base, "", "", "4 before, 0 after")
				answerFailedCheck(t, bin, top, label, "stop", 1)
				followedRun(t, bin, top, 1, "--resume", label)
				assertAcceptedTestCut(t, top, lastRun(t, top, "failed"), base, "", "", "4 before, 0 after")
			}
		})
	}
}
