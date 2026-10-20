package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestResumeKeepsEarlierUnchainedFollowUps(t *testing.T) {
	delivered, order := filepath.Join(t.TempDir(), "delivered"), filepath.Join(t.TempDir(), "order")
	t.Setenv("ATM_TEST_OUT", delivered)
	t.Setenv("FAKE_AGENT_ORDER", order)
	deliver := atmSet(atmYAML, "delivery", `{ git log --format=%s; git ls-files; cat a.txt; } > "$ATM_TEST_OUT"`)
	root := repo(t, "https://example.com/owner/repo.git", atmSet(atmSet(deliver,
		"test", `if [ -f c1_test.sh ]; then false; else sh a_test.sh; fi`), "lint", "false"))
	t.Setenv("ATM_TEST_CRITERION", "A retry runs once after a timeout.")
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then echo u2 >> a.txt; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[]}' > .atm/fake-report.json; `+
		`else printf 'fixed\n' > a.txt; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh; `+
		`mkdir -p .atm/follow-ups/1 .atm/follow-ups/2; echo 'grep -q u2 a.txt' > .atm/follow-ups/1/c1_test.sh; `+
		`echo 'grep -q extra a.txt' > .atm/follow-ups/2/c2_test.sh; echo cache > .atm/follow-ups/cache; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[`+
		`{"title":"chained","red_test":"c1_test.sh","criterion":"A retry runs once after a timeout."},`+
		`{"title":"pending","red_test":"c2_test.sh",`+
		`"criterion":"Some other work is documented."}]}' > .atm/fake-report.json; fi`)
	var first bytes.Buffer
	err := Run("failed", []string{issueFile(t, followUpIssue)}, &first, io.Discard)
	if err == nil {
		t.Fatal("want unit 1's lint to fail")
	}
	failedUnit(t, root, "failed", &first, "lint")
	commitConfig(t, root, atmSet(deliver, "test", "test ! -e .atm/follow-ups/cache && "+
		"test ! -e .atm/follow-ups/1/c1_test.sh && test ! -e c1_test.sh && test ! -e c2_test.sh && sh a_test.sh"))
	// What a run that chained c1_test.sh kept before follow-ups were retired: no queue to take up again.
	resumed, saved, err := legacyResume(t, root, "failed", "resumed", followUpIssue, obj{
		"follow_up": obj{"title": "chained", "red_test": "c1_test.sh", "criterion": "A retry runs once after a timeout.",
			"test": "grep -q u2 a.txt\n", "verified": true},
		"pending": []obj{{"title": "pending", "red_test": "c2_test.sh", "criterion": "Some other work is documented.",
			"test": "grep -q extra a.txt\n", "verified": true}}})
	if err != nil {
		b, _ := os.ReadFile(filepath.Join(root, ".atm", "runs", "failed", "resume", "resume.json"))
		t.Fatalf("resume: %v; saved state: %s", err, b)
	}
	resumedAlone(t, root, "resumed", resumed, err, saved)
	if fs, _ := saved["follow_ups"].([]any); len(fs) != 2 {
		t.Fatalf("want the unchained follow-up left in the kept report, as a note, got %v", saved)
	}
	b, err := os.ReadFile(delivered)
	if err != nil || string(b) != "fix: Retry on timeout\ninit\n.atm.yaml\na.txt\na_test.sh\nfixed\n" {
		t.Fatalf("want unit 1's kept work alone delivered, got %v:\n%s", err, b)
	}
	if b, err := os.ReadFile(order); err != nil || string(b) != "worker\nponytail\n" {
		t.Fatalf("want unit 1's worker alone, then the ponytail pass, got %v:\n%s", err, b)
	}
}
