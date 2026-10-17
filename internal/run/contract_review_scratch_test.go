package run

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestRunContractReviewWritesItsChecksUnderItsScratch runs a reviewer that writes what its checks need into the
// scratch directory its brief names, by a tool call naming the absolute path and through $TMPDIR: delivery
// proceeds and the clone stands.
func TestRunContractReviewWritesItsChecksUnderItsScratch(t *testing.T) {
	runDir, _, got, call, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery),
		"FAKE_REVIEW_SCRATCH", "1")
	if err != nil {
		t.Fatal(err)
	}
	read := reader(t, got)
	review := agentCall(t, call+".review")
	scratch := filepath.Join(runDir, "contract-review-scratch")
	if !strings.Contains(review.Stdin, "`"+scratch+"`") {
		t.Fatalf("want the brief to name the scratch directory %s, got:\n%s", scratch, review.Stdin)
	}
	if !strings.Contains(review.Stdin, "never inline interpreters") {
		t.Fatalf("want the brief to forbid inline interpreters, got:\n%s", review.Stdin)
	}
	if !slices.Contains(review.Env, "TMPDIR="+scratch) {
		t.Fatalf("want the reviewer's TMPDIR at %s, got %v", scratch, review.Env)
	}
	if clone := read("clone"); scratch == clone || strings.HasPrefix(scratch, clone+string(filepath.Separator)) {
		t.Fatalf("the scratch directory %s is inside the clone %s", scratch, clone)
	}
	log, err := os.ReadFile(filepath.Join(runDir, "contract-review.jsonl"))
	if err != nil || !strings.Contains(string(log), scratch+"/plan.txt") {
		t.Fatalf("want the reviewer's tool call writing under %s in its log, got %v", scratch, err)
	}
	for name, want := range map[string]string{"tickets.txt": "tickets", "tmp.txt": "tmp"} {
		b, err := os.ReadFile(filepath.Join(scratch, name))
		if err != nil || strings.TrimSpace(string(b)) != want {
			t.Fatalf("want %s under the scratch directory holding %q, got %q, %v", name, want, b, err)
		}
	}
	if calls := read("calls"); calls != "ran" {
		t.Fatalf("want the delivery command run once, got %q", calls)
	}
}

// TestRunContractReviewScratchDoesNotExcuseTheClone: a reviewer that writes its checks into the clone, an
// ignored path among them, fails exactly as it does without a scratch directory.
func TestRunContractReviewScratchDoesNotExcuseTheClone(t *testing.T) {
	runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery),
		"FAKE_REVIEW_SCRATCH", "1",
		"FAKE_REVIEW_WORK", "mkdir -p .tmp/verify && echo plan > .tmp/verify/x")
	notDelivered(t, err, runDir, got, "the contract reviewer changed the clone")
	if !strings.Contains(err.Error(), "review mutation or inspection failure") {
		t.Fatalf("want the review mutation failure, got %v", err)
	}
}

// TestRunContractReviewScratchStartsEmptyEachRound: the second review of a repair loop starts with an empty
// scratch directory: what the first review wrote there is gone.
func TestRunContractReviewScratchStartsEmptyEachRound(t *testing.T) {
	blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 writes fixed and never retries", nil)
	repairEnv(t, []string{blocked, slicingJSON(t, slicingReport())}, []string{repairGrow})
	runDir, _, _, _, err := reviewRun(t, repairATM(), "FAKE_REVIEW_SCRATCH", "1",
		"FAKE_REVIEW_WORK_1", `echo stale > "$TMPDIR/stale.txt"`)
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(runDir, "contract-review-scratch")
	if _, e := os.Stat(filepath.Join(scratch, "stale.txt")); e == nil {
		t.Fatal("the second review's scratch directory kept the first review's stale.txt")
	}
	for _, name := range []string{"tickets.txt", "tmp.txt"} {
		if _, e := os.Stat(filepath.Join(scratch, name)); e != nil {
			t.Fatalf("want %s under the scratch directory: %v", name, e)
		}
	}
}

// TestFollowUpScopeBriefNamesItsScratch: the scope reviewer, too, gets a scratch directory its brief names and
// its TMPDIR points at, and its checks may write there.
func TestFollowUpScopeBriefNamesItsScratch(t *testing.T) {
	_, runDir, call, _, err := scopeRun(t, androidIssue, "c1_test.sh",
		scopeFollowUp("c1_test.sh", "The android card opens the route.", `["a.txt"]`),
		"FAKE_REVIEW_SCRATCH", "1")
	if err != nil {
		t.Fatal(err)
	}
	scope := agentCall(t, call+".scope")
	dirs, _ := filepath.Glob(filepath.Join(runDir, "scope-review-unit*-scratch"))
	if len(dirs) != 1 {
		t.Fatalf("want one scope scratch directory under %s, got %v", runDir, dirs)
	}
	scratch := dirs[0]
	if !strings.Contains(scope.Stdin, "`"+scratch+"`") ||
		!strings.Contains(scope.Stdin, "never inline interpreters") {
		t.Fatalf("want the scope brief to name the scratch %s and forbid inline interpreters, got:\n%s",
			scratch, scope.Stdin)
	}
	if !slices.Contains(scope.Env, "TMPDIR="+scratch) {
		t.Fatalf("want the scope reviewer's TMPDIR at %s, got %v", scratch, scope.Env)
	}
	for _, name := range []string{"tickets.txt", "tmp.txt"} {
		if _, e := os.Stat(filepath.Join(scratch, name)); e != nil {
			t.Fatalf("want %s under the scope scratch directory: %v", name, e)
		}
	}
}
