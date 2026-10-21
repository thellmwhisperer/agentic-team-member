package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
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

// noFollowUpsFile fails the test when run label of root wrote a follow-ups.json: follow-ups are retired
// (decision 7934), and the follow-ups an agent reports stay in its report, with no action on them.
func noFollowUpsFile(t *testing.T, root, label string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(root, ".atm", "runs", label, "follow-ups.json")); !os.IsNotExist(err) {
		t.Fatalf("want no follow-ups.json, got %v\n%s", err, b)
	}
}

// stateEnds is ends with a state: a resumed agent's message has none.
func stateEnds(t *testing.T, out *bytes.Buffer, step string) []obj {
	t.Helper()
	var got []obj
	for _, ev := range ends(t, bytes.NewBuffer(out.Bytes()), step) {
		if ev["state"] != nil {
			got = append(got, ev)
		}
	}
	return got
}

// oneUnit fails the test unless out holds one agent and one checks end, unit 1's, neither with follow-ups.
func oneUnit(t *testing.T, out *bytes.Buffer) {
	t.Helper()
	agents, checks := stateEnds(t, out, "agent"), stateEnds(t, out, "checks")
	if len(agents) != 1 || len(checks) != 1 || agents[0]["unit"] != float64(1) || checks[0]["unit"] != float64(1) ||
		checks[0]["follow_ups"] != nil || checks[0]["gross_lines"] != nil {
		t.Fatalf("want one unit with no follow-up handling, got %v, %v", agents, checks)
	}
}

// commitConfig commits atm as root's .atm.yaml: a run reads it from the base ref.
func commitConfig(t *testing.T, root, atm string) {
	t.Helper()
	writeFixture(t, filepath.Join(root, ".atm.yaml"), atm)
	gitT(t, root, "add", ".atm.yaml")
	gitT(t, root, "commit", "-qm", "config")
}

// legacyResume resumes run failed of root as run label on issue, once fields, what a resume point kept before
// follow-ups were retired, are in its resume.json. It returns the resumed run's events and error, and the report
// the failed run kept.
func legacyResume(t *testing.T, root, failed, label, issue string, fields obj) (*bytes.Buffer, obj, error) {
	t.Helper()
	report := addLegacy(t, root, failed, fields)
	var out bytes.Buffer
	err := Run(label, []string{"--resume", failed, issueFile(t, issue)}, &out, io.Discard)
	return &out, report, err
}

// addLegacy adds fields to the resume.json of run failed of root and returns the report it kept.
func addLegacy(t *testing.T, root, failed string, fields obj) obj {
	t.Helper()
	path := filepath.Join(root, ".atm", "runs", failed, "resume", "resume.json")
	var saved obj
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &saved) != nil {
		t.Fatalf("%s's resume.json: %v\n%s", failed, err, b)
	}
	report, _ := saved["report"].(obj)
	maps.Copy(saved, fields)
	b, _ := json.Marshal(saved)
	writeFixture(t, path, string(b))
	return report
}

// resumedAlone fails the test unless run label of root, which ended with err and events out, resumed the issue's
// unit alone and passed: its agent's report the one its failed run kept, saved, untouched, and no follow-ups.json,
// brief of a unit 2 or resume point of its own.
func resumedAlone(t *testing.T, root, label string, out *bytes.Buffer, err error, saved obj) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: want the resumed unit alone, nothing blocked, got %v", label, err)
	}
	oneUnit(t, out)
	if got := stateEnds(t, out, "agent")[0]; got["resumed_from"] == nil || !reflect.DeepEqual(got["report"], saved) {
		t.Fatalf("%s: want the kept report resumed untouched, %v, got %v", label, saved, got)
	}
	noFollowUpsFile(t, root, label)
	for _, name := range []string{"brief-unit-2.md", "resume"} {
		if _, err := os.Stat(filepath.Join(root, ".atm", "runs", label, name)); !os.IsNotExist(err) {
			t.Fatalf("%s: want no %s, got %v", label, name, err)
		}
	}
}

// chainWork is unit k's work: unit 1 fixes a.txt with a line too many, each later unit, were one to run, would
// make the red test of the one before pass by adding u<k> to a.txt. Each declares the follow-up c<k>_test.sh, red
// until a.txt has u<k+1>, quoting $ATM_TEST_CRITERION. Unit 2 would run $ATM_TEST_EDIT first.
const chainWork = `if [ -f c2_test.sh ]; then k=3; echo u3 >> a.txt; elif [ -f c1_test.sh ]; then k=2; ` +
	`eval "$ATM_TEST_EDIT"; echo u2 >> a.txt; else k=1; printf 'fixed\nextra\n' > a.txt; ` +
	`echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh; fi; mkdir -p .atm/follow-ups/1; ` +
	`echo "grep -q u$((k+1)) a.txt" > .atm/follow-ups/1/c${k}_test.sh; ` +
	`printf '{"test_file": "a_test.sh", "follow_ups": [{"title": "gap %s", "red_test": "c%s_test.sh", ` +
	`"criterion": "%s"}]}' $k $k "$ATM_TEST_CRITERION" > .atm/fake-report.json`

func TestRunDiscardsACutThatUndoesAnEarlierUnit(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmSet(atmSet(atmYAML, "test", suite), "delivery",
		"sh regression_test.sh && test ! -e second_test.sh"))
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "add", "source.txt")
	gitT(t, root, "commit", "-q", "-m", "source")
	t.Setenv("FAKE_AGENT_WORK", `if [ -f second_test.sh ]; then echo second >> source.txt; `+
		`printf '{"test_file":"second_test.sh","follow_ups":[]}' > .atm/fake-report.json; `+
		`else printf 'fixed\nextra\nmore\n' > source.txt; `+
		`echo 'grep -q fixed source.txt && ! grep -q broken source.txt' > regression_test.sh; `+
		`mkdir -p .atm/follow-ups/1; echo 'grep -q second source.txt' > .atm/follow-ups/1/second_test.sh; `+
		`printf '{"test_file":"regression_test.sh","follow_ups":[{"title":"gap","red_test":"second_test.sh",`+
		`"criterion":"A retry runs once after a timeout."}]}' > .atm/fake-report.json; fi`)
	t.Setenv("FAKE_PONYTAIL_WORK", `printf 'broken\nsecond\n' > source.txt`)
	t.Setenv("FAKE_PONYTAIL_REPORT", strings.Replace(cut, "a.txt", "source.txt", 1))
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatalf("want the cut discarded, the issue's test passing and no follow-up's test at delivery, got %v", err)
	}
	if p := ends(t, &out, "ponytail"); len(p) != 1 || p[0]["kept"] != false ||
		!strings.Contains(fmt.Sprint(p[0]["reason"]), "test:") {
		t.Fatalf("want the cut discarded by the configured suite, got %v", p)
	}
}
