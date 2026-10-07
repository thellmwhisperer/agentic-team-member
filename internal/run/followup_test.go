package run

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// followUpIssue has two acceptance criteria, the sentences a follow-up may quote; the first one wraps.
const followUpIssue = "# Retry on timeout\nType: fix\n\n## Acceptance criteria\n\n1. A retry runs once after a\n" +
	"timeout.\n2. The retry error is reported.\n"

// ends is the end event of every run of step in out, in order.
func ends(t *testing.T, out *bytes.Buffer, step string) []obj {
	t.Helper()
	var got []obj
	for _, ev := range events(t, out) {
		if ev["step"] == step && ev["state"] != "started" {
			got = append(got, ev)
		}
	}
	return got
}

// readFollowUps is root's .atm/follow-ups.json.
func readFollowUps(t *testing.T, root string) []obj {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "follow-ups.json"))
	var got []obj
	if err == nil {
		err = json.Unmarshal(b, &got)
	}
	if err != nil {
		t.Fatalf("follow-ups.json: %v\n%s", err, b)
	}
	return got
}

func TestRunSortsTheFollowUps(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("ATM_TEST_REPORT", `{"test_file": "a_test.sh", "follow_ups": [`+
		`{"title": "no test", "red_test": "b_test.sh", "criterion": "The retry error is reported."}, `+
		`{"title": "green", "red_test": "c_test.sh", "criterion": "The retry error is reported."}, `+
		`{"title": "fragment", "red_test": "d_test.sh", "criterion": "A retry runs once"}, `+
		`{"title": "outside", "red_test": "t/e_test.sh", "criterion": "Errors are logged."}]}`)
	t.Setenv("FAKE_AGENT_WORK", fixWork+"; mkdir -p .atm/follow-ups/2 .atm/follow-ups/3 .atm/follow-ups/4/t; "+
		"echo true > .atm/follow-ups/2/c_test.sh; echo false > .atm/follow-ups/3/d_test.sh; "+
		"echo 'test -f e.txt' > .atm/follow-ups/4/t/e_test.sh; "+
		`printf %s "$ATM_TEST_REPORT" > .atm/fake-report.json`)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if agents := ends(t, bytes.NewBuffer(out.Bytes()), "agent"); len(agents) != 1 {
		t.Fatalf("no follow-up quotes a criterion: want one unit, got %v", agents)
	}
	checks := ends(t, &out, "checks")
	verdicts, _ := json.Marshal(checks[0]["follow_ups"])
	for _, want := range []string{"b_test.sh: rejected", "c_test.sh: rejected: it passes today"} {
		if !strings.Contains(string(verdicts), want) {
			t.Errorf("the checks' follow_ups lack %q: %s", want, verdicts)
		}
	}
	got := readFollowUps(t, root)
	if len(got) != 2 || got[0]["red_test"] != "d_test.sh" || got[0]["test"] != "false\n" ||
		got[0]["criterion"] != "A retry runs once" || got[1]["title"] != "outside" ||
		got[1]["red_test"] != "t/e_test.sh" || got[1]["test"] != "test -f e.txt\n" {
		t.Fatalf("want the two red follow-ups outside the issue, with their tests, got %v", got)
	}
}

func TestFollowUpRedTestMustBeNewOutsideATM(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "existing test", path: "a_test.sh"},
		{name: "inside atm", path: ".atm/hidden_test.sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := repo(t, "https://example.com/owner/repo.git", atmYAML)
			f := followUp{RedTest: tc.path}
			source := filepath.Join(dir, ".atm", "follow-ups", "1", filepath.FromSlash(tc.path))
			if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("false\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			why, err := red(dir, 1, &f, "sh {file}")
			if err != nil || why != "red_test "+filepath.FromSlash(tc.path)+" is not a new test path" {
				t.Fatalf("want new-path rejection, got %q, %v", why, err)
			}
		})
	}
}

func TestQuotedAcceptsCriterionAsMarkedWholeLine(t *testing.T) {
	if !quoted("Acceptance criteria:\n  7) Exit code is 0.\n", "Exit code is 0") {
		t.Fatal("a criterion matching a whole numbered line should chain without punctuation")
	}
}

// chainWork is unit k's work: unit 1 fixes a.txt with a line too many, each later unit makes the red test of
// the one before pass by adding u<k> to a.txt. Each declares the follow-up c<k>_test.sh, red until a.txt has
// u<k+1>, quoting $ATM_TEST_CRITERION. Unit 2 runs $ATM_TEST_EDIT first.
const chainWork = `if [ -f c2_test.sh ]; then k=3; echo u3 >> a.txt; elif [ -f c1_test.sh ]; then k=2; ` +
	`eval "$ATM_TEST_EDIT"; echo u2 >> a.txt; else k=1; printf 'fixed\nextra\n' > a.txt; ` +
	`echo 'grep -q fixed a.txt' > a_test.sh; fi; mkdir -p .atm/follow-ups/1; ` +
	`echo "grep -q u$((k+1)) a.txt" > .atm/follow-ups/1/c${k}_test.sh; ` +
	`printf '{"test_file": "a_test.sh", "follow_ups": [{"title": "gap %s", "red_test": "c%s_test.sh", ` +
	`"criterion": "%s"}]}' $k $k "$ATM_TEST_CRITERION" > .atm/fake-report.json`

func TestRunChainsAFollowUpOnTheIssueUpToThreeUnits(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git",
		atmSet(atmYAML, "delivery", `{ git log --format=%s; git status --porcelain; } > "$ATM_TEST_OUT"`))
	got := filepath.Join(t.TempDir(), "delivered")
	t.Setenv("ATM_TEST_OUT", got)
	t.Setenv("ATM_TEST_CRITERION", "A retry runs once after a timeout.")
	t.Setenv("FAKE_AGENT_WORK", chainWork)
	t.Setenv("FAKE_PONYTAIL_WORK", "grep -v extra a.txt > cut; mv cut a.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	agents := ends(t, &out, "agent")
	if len(agents) != 3 || agents[2]["unit"] != float64(3) || agents[2]["state"] != "passed" {
		t.Fatalf("want three units, got %v", agents)
	}
	b, _ := os.ReadFile(got)
	want := "ponytail: 1 cuts\natm unit 3: Retry on timeout\natm unit 2: Retry on timeout\n" +
		"atm unit 1: Retry on timeout\ninit\n"
	if string(b) != want {
		t.Fatalf("want each unit committed on the one before, then the cut, got\n%s\nwant\n%s", b, want)
	}
	brief, err := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "brief-unit-2.md"))
	if err != nil || !strings.Contains(string(brief), "`c1_test.sh`") {
		t.Fatalf("want unit 2's brief to name its red test, got %v\n%s", err, brief)
	}
	if left := readFollowUps(t, root); len(left) != 1 || left[0]["red_test"] != "c3_test.sh" ||
		left[0]["test"] != "grep -q u4 a.txt\n" {
		t.Fatalf("want unit 3's follow-up, past the third unit, in follow-ups.json, got %v", left)
	}
}

func TestRunLabelsDeliveredChainedUnitAndChecks(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery", deliveryLine))
	got := t.TempDir()
	t.Setenv("ATM_TEST_OUT", got)
	t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then echo u2 >> a.txt; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[]}' > .atm/fake-report.json; `+
		`else printf 'fixed\nextra\n' > a.txt; echo 'grep -q fixed a.txt' > a_test.sh; `+
		`mkdir -p .atm/follow-ups/1; echo 'grep -q u2 a.txt' > .atm/follow-ups/1/c1_test.sh; `+
		`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"gap","red_test":"c1_test.sh",`+
		`"criterion":"A retry runs once after a timeout."}]}' > .atm/fake-report.json; fi`)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	dev := "atm <atm@example.com>|atm <atm@example.com>"
	want := "atm unit 2: Retry on timeout|" + dev + "\natm unit 1: Retry on timeout|" + dev
	if b, err := os.ReadFile(filepath.Join(got, "log")); err != nil || string(b) != want+"\n" {
		t.Fatalf("want delivered unit numbers in commit history, got %q, %v", b, err)
	}
	checks := ends(t, &out, "checks")
	if len(checks) != 2 || checks[0]["unit"] != float64(1) || checks[1]["unit"] != float64(2) {
		t.Fatalf("want unit numbers on both checks events, got %v", checks)
	}
}

func TestRunDiesWhenAUnitEditsItsRedTest(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("ATM_TEST_CRITERION", "The retry error is reported.")
	t.Setenv("ATM_TEST_EDIT", "echo 'grep -q u2 a.txt # edited' > c1_test.sh;")
	t.Setenv("FAKE_AGENT_WORK", chainWork)
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "c1_test.sh") || !strings.Contains(err.Error(), "edit") {
		t.Fatalf("want unit 2 to fail for editing its red test, got %v", err)
	}
}
