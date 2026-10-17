package run

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

func TestContractRepairRejectsMovedHEADBeforeProtectedTests(t *testing.T) {
	for _, tc := range []struct {
		name, work string
		moved      bool
	}{
		{"merge", "git merge --no-ff --no-edit other", true},
		{"commit", "git commit --allow-empty -m moved", true},
		{"reset", "git reset --hard other", true},
		{"edit protected test", "echo weakened > a_test.sh", false},
		{"delete protected test", "rm a_test.sh", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := repo(t, "unused", "")
			head := gitT(t, root, "rev-parse", "HEAD")
			gitT(t, root, "checkout", "-b", "other")
			writeFixture(t, filepath.Join(root, "a_test.sh"), "test -s a.txt\n")
			gitT(t, root, "commit", "-am", "change protected test on other branch")
			gitT(t, root, "checkout", "main")
			t.Setenv("FAKE_REPAIR_WORK", tc.work)
			a := agent{Agent: config.Agent{Harness: "claude"}, root: root, dir: t.TempDir()}
			c := config.Config{TestPatterns: []string{"*_test.sh"}}
			_, attempt, err := a.oneRepair(root, root, head, Issue{}, nil, c, 1, nil)
			after := gitT(t, root, "rev-parse", "HEAD")
			if (after != head) != tc.moved {
				t.Fatalf("fake repair HEAD movement: before %s, after %s", head, after)
			}
			want := "contract repair round 1: changed protected test a_test.sh"
			if tc.moved {
				want = fmt.Sprintf("contract repair round 1: the repair agent moved HEAD from %s to %s; "+
					"it may only edit files", head, after)
			}
			if err == nil || err.Error() != want || attempt.Result != "blocked" {
				t.Fatalf("want %q and a blocked round, got %v, %+v", want, err, attempt)
			}
			if tc.name == "merge" && gitT(t, root, "rev-parse", "HEAD^1") != head {
				t.Fatal("fake repair did not create a merge on the pre-repair HEAD")
			}
		})
	}
}

func TestContractRepairBriefForbidsGitHistoryChanges(t *testing.T) {
	brief := repairBrief(nil)
	for _, command := range []string{"merge", "pull", "rebase", "cherry-pick", "reset", "commit"} {
		if !strings.Contains(brief, "git "+command) {
			t.Errorf("repair brief does not forbid git %s", command)
		}
	}
}
