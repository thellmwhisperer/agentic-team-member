package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mergeRepairWork is a repair agent that merges a branch of another commit which modifies a protected test,
// like an agent that brings origin/main in.
const mergeRepairWork = `old=$(git rev-parse HEAD); echo weakened > a_test.sh; git add a_test.sh; ` +
	`git -c user.name=r -c user.email=r@example.com commit -qm 'main moved'; git branch moved; ` +
	`git checkout -q --detach "$old"; git merge --no-edit moved`

func TestContractReviewRepairFailsAMovedHeadNotAProtectedTest(t *testing.T) {
	blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 never retries", nil)
	repairEnv(t, []string{blocked}, []string{mergeRepairWork})
	runDir, _, got, _, err := reviewRun(t, repairATM())
	assertNoDelivery(t, err, runDir, got, "moved HEAD from")
	if !strings.Contains(err.Error(), "it may only edit files") ||
		strings.Contains(err.Error(), "changed protected test") {
		t.Fatalf("a merge is not the agent's edit: %v", err)
	}
}

func TestContractReviewRepairStillFailsAProtectedTestEditedInTheWorktree(t *testing.T) {
	blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 never retries", nil)
	repairEnv(t, []string{blocked}, []string{"echo weakened > a_test.sh"})
	runDir, _, got, _, err := reviewRun(t, repairATM())
	assertNoDelivery(t, err, runDir, got, "changed protected test a_test.sh")
}

func TestContractReviewRepairBriefForbidsMovingGitCommands(t *testing.T) {
	blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 writes fixed and never retries", nil)
	repairEnv(t, []string{blocked, slicingJSON(t, slicingReport())}, []string{repairGrow})
	_, _, got, call, err := reviewRun(t, repairATM())
	if err != nil {
		t.Fatal(err)
	}
	if _, e := os.Stat(filepath.Join(got, "calls")); e != nil {
		t.Fatal("the repair did not deliver")
	}
	brief := agentCall(t, call+".repair1").Stdin
	for _, cmd := range []string{"git merge", "git pull", "git rebase", "git cherry-pick", "git reset", "git commit"} {
		if !strings.Contains(brief, cmd) {
			t.Fatalf("repair brief missing %q:\n%s", cmd, brief)
		}
	}
}
