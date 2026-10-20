package run

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestResumeKeepsEarlierUnchainedFollowUps(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git",
		atmSet(atmYAML, "test", `if [ -f c1_test.sh ]; then false; else sh a_test.sh; fi`))
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
		t.Fatal("want unit 2 checks to fail")
	}
	fixedConfig := atmSet(atmYAML, "test", "test ! -e .atm/follow-ups/cache && sh a_test.sh")
	if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(fixedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "commit", "-qam", "fix the config") // the resume reads it from the base ref
	var resumed bytes.Buffer
	args := []string{"--resume", "failed", issueFile(t, followUpIssue)}
	if err := Run("resumed", args, &resumed, io.Discard); err != nil {
		b, _ := os.ReadFile(filepath.Join(root, ".atm", "runs", "failed", "resume", "resume.json"))
		t.Fatalf("resume: %v; saved state: %s", err, b)
	}
	b, err := os.ReadFile(filepath.Join(root, ".atm", "runs", "resumed", "follow-ups.json"))
	var followups []map[string]any
	if err == nil {
		err = json.Unmarshal(b, &followups)
	}
	if err != nil {
		t.Fatalf("follow-ups.json: %v\n%s", err, b)
	}
	if len(followups) != 1 || followups[0]["title"] != "pending" || followups[0]["red_test"] != "c2_test.sh" {
		t.Fatalf("want unit 1's unchained follow-up preserved, got %v", followups)
	}
}
