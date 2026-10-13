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

// failingUnit2Work is unit 1 fixing a.txt with two follow-ups, c1_test.sh on the issue, which chains, and
// c2_test.sh outside it, then unit 2 adding u2 to a.txt and reporting c3_test.sh with criterion
// $ATM_TEST_CRITERION. A test line that fails once c1_test.sh is in place fails unit 2's checks.
// printf gives every red-test fixture the same LF bytes on each OS.
const failingUnit2Work = `if [ -f c1_test.sh ]; then echo u2 >> a.txt; mkdir -p .atm/follow-ups/1; ` +
	`printf '%s\n' 'grep -q u3 a.txt' > .atm/follow-ups/1/c3_test.sh; ` +
	`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"unit 2 gap","red_test":"c3_test.sh",` +
	`"criterion":"%s"}]}' "$ATM_TEST_CRITERION" > .atm/fake-report.json; ` +
	`else printf 'fixed\n' > a.txt; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh; ` +
	`mkdir -p .atm/follow-ups/1 .atm/follow-ups/2; printf '%s\n' 'grep -q u2 a.txt' > .atm/follow-ups/1/c1_test.sh; ` +
	`printf '%s\n' 'grep -q extra a.txt' > .atm/follow-ups/2/c2_test.sh; ` +
	`printf '{"test_file":"a_test.sh","follow_ups":[` +
	`{"title":"chained","red_test":"c1_test.sh","criterion":"A retry runs once after a timeout."},` +
	`{"title":"pending","red_test":"c2_test.sh","criterion":"Some other work is documented."}]}' ` +
	`> .atm/fake-report.json; fi`

const failsOnUnit2 = `if [ -f c1_test.sh ]; then false; else sh a_test.sh; fi`

func normalizedTestContent(followUp obj) string {
	content, _ := followUp["test"].(string)
	return strings.ReplaceAll(content, "\r\n", "\n")
}

// wantFollowUps checks the independent identities and verification states of both pending follow-ups.
func wantFollowUps(t *testing.T, got []obj, unit2 bool) {
	t.Helper()
	if len(got) != 2 {
		t.Fatalf("want both units' follow-ups, got %v", got)
	}
	byTest := map[string]obj{}
	for _, followUp := range got {
		byTest[followUp["red_test"].(string)] = followUp
	}
	if byTest["c2_test.sh"]["verified"] != true || byTest["c2_test.sh"]["title"] != "pending" ||
		byTest["c3_test.sh"]["verified"] != unit2 || byTest["c3_test.sh"]["title"] != "unit 2 gap" ||
		normalizedTestContent(byTest["c3_test.sh"]) != "grep -q u3 a.txt\n" {
		t.Fatalf("want unit 1's follow-up verified and unit 2's verified %v, got %v", unit2, got)
	}
}

func TestAUnitThatFailsChecksKeepsItsFollowUps(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "test", failsOnUnit2))
	t.Setenv("ATM_TEST_CRITERION", "The retry error is reported.") // on the issue: still never chained
	t.Setenv("FAKE_AGENT_WORK", failingUnit2Work)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err == nil {
		t.Fatal("want unit 2's checks to fail")
	}
	if agents := ends(t, bytes.NewBuffer(out.Bytes()), "agent"); len(agents) != 2 {
		t.Fatalf("want the run to end at unit 2, got %v", agents)
	}
	if checks := ends(t, &out, "checks"); len(checks) != 2 || checks[1]["state"] != "failed" {
		t.Fatalf("want unit 2's checks failed, got %v", checks)
	}
	wantFollowUps(t, readFollowUps(t, root), false)
}

func TestUnit1ThatFailsChecksKeepsItsFollowUps(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("FAKE_AGENT_WORK", fixWork+"; mkdir -p .atm/follow-ups/1; "+
		"printf '%s\\n' 'grep -q restored a.txt' > .atm/follow-ups/1/r_test.sh; "+
		`printf '{"test_file":"","follow_ups":[{"title":"atomic restore","red_test":"r_test.sh",`+
		`"criterion":"Restores are atomic."}]}' > .atm/fake-report.json`)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err == nil {
		t.Fatal("want checks to reject the empty test_file")
	}
	if checks := ends(t, &out, "checks"); len(checks) != 1 || !strings.Contains(checks[0]["error"].(string), "test_file") {
		t.Fatalf("want unit 1's checks failed on its test_file, got %v", checks)
	}
	got := readFollowUps(t, root)
	if len(got) != 1 || got[0]["title"] != "atomic restore" || got[0]["verified"] != false ||
		normalizedTestContent(got[0]) != "grep -q restored a.txt\n" {
		t.Fatalf("want unit 1's follow-up not verified, got %v", got)
	}
}

func TestInvalidFollowUpsDoNotHideAChecksFailure(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("FAKE_AGENT_WORK",
		fixWork+`; mkdir -p .atm; printf '{"test_file":"","follow_ups":"later"}' > .atm/fake-report.json`)
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err == nil {
		t.Fatal("want checks to reject the empty test_file")
	}
	checks := ends(t, bytes.NewBuffer(out.Bytes()), "checks")
	if len(checks) != 1 {
		t.Fatalf("want one checks event, got %v\n%s", checks, out.String())
	}
	reason, _ := checks[0]["error"].(string)
	first, rest, _ := strings.Cut(reason, "\n")
	if !strings.Contains(first, "test_file") || !strings.Contains(rest, "invalid follow_ups") {
		t.Fatalf("want the checks reason first, the follow-ups problem next to it, got %q", reason)
	}
	if got := readFollowUps(t, root); len(got) != 0 {
		t.Fatalf("want no follow-ups, got %v", got)
	}
}

func TestMalformedReportedFollowUpsDoNotHideAChecksFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		list string
	}{
		{name: "null entry", list: `[null]`},
		{name: "missing title", list: `[{"red_test":"r_test.sh","criterion":"A gap is documented."}]`},
		{name: "blank title", list: `[{"title":" ","red_test":"r_test.sh","criterion":"A gap is documented."}]`},
		{name: "missing red test", list: `[{"title":"gap","criterion":"A gap is documented."}]`},
		{name: "blank red test", list: `[{"title":"gap","red_test":"","criterion":"A gap is documented."}]`},
		{name: "missing criterion", list: `[{"title":"gap","red_test":"r_test.sh"}]`},
		{name: "blank criterion", list: `[{"title":"gap","red_test":"r_test.sh","criterion":"\t"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			t.Setenv("FAKE_AGENT_WORK", fixWork+`; mkdir -p .atm; printf '%s' '{"test_file":"","follow_ups":`+
				tc.list+`}' > .atm/fake-report.json`)
			var out bytes.Buffer
			if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err == nil {
				t.Fatal("want checks to reject the empty test_file")
			}
			checks := ends(t, &out, "checks")
			if len(checks) != 1 {
				t.Fatalf("want one checks event, got %v", checks)
			}
			reason, _ := checks[0]["error"].(string)
			first, rest, _ := strings.Cut(reason, "\n")
			if !strings.Contains(first, "test_file") || !strings.Contains(rest, "invalid follow_ups") {
				t.Fatalf("want the checks reason first, the malformed follow-ups problem next to it, got %q", reason)
			}
			if got := readFollowUps(t, root); len(got) != 0 {
				t.Fatalf("want no malformed follow-ups, got %v", got)
			}
		})
	}
}

func TestFollowUpsFieldShapesKeepEarlierFollowUpsOnChecksFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		field     string
		wantError bool
	}{
		{name: "absent"},
		{name: "null", field: `,"follow_ups":null`, wantError: true},
		{name: "empty array", field: `,"follow_ups":[]`},
		{name: "scalar", field: `,"follow_ups":"later"`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "test", failsOnUnit2))
			t.Setenv("ATM_TEST_REPORT", `{"test_file":"a_test.sh"`+tc.field+`}`)
			t.Setenv("FAKE_AGENT_WORK", `if [ -f c1_test.sh ]; then echo u2 >> a.txt; `+
				`mkdir -p .atm/follow-ups/1; printf '%s\n' 'grep -q u3 a.txt' > .atm/follow-ups/1/c3_test.sh; `+
				`printf '%s' "$ATM_TEST_REPORT" > .atm/fake-report.json; `+
				`else printf 'fixed\n' > a.txt; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh; `+
				`mkdir -p .atm/follow-ups/1 .atm/follow-ups/2; `+
				`printf '%s\n' 'grep -q u2 a.txt' > .atm/follow-ups/1/c1_test.sh; `+
				`printf '%s\n' 'grep -q extra a.txt' > .atm/follow-ups/2/c2_test.sh; `+
				`printf '{"test_file":"a_test.sh","follow_ups":[`+
				`{"title":"chained","red_test":"c1_test.sh","criterion":"A retry runs once after a timeout."},`+
				`{"title":"pending","red_test":"c2_test.sh","criterion":"Some other work is documented."}]}' `+
				`> .atm/fake-report.json; fi`)
			var out bytes.Buffer
			if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err == nil {
				t.Fatal("want unit 2's checks to fail")
			}
			checks := ends(t, &out, "checks")
			if len(checks) != 2 || checks[1]["state"] != "failed" {
				t.Fatalf("want unit 2's checks failure, got %v", checks)
			}
			reason, _ := checks[1]["error"].(string)
			first, note, hasNote := strings.Cut(reason, "\n")
			if !strings.HasPrefix(first, "test: ") || hasNote != tc.wantError ||
				(tc.wantError && !strings.Contains(note, "invalid follow_ups")) {
				t.Fatalf("want invalid-field diagnostic %v after the checks reason, got %q", tc.wantError, reason)
			}
			got := readFollowUps(t, root)
			if len(got) != 1 || got[0]["title"] != "pending" || got[0]["verified"] != true ||
				normalizedTestContent(got[0]) != "grep -q extra a.txt\n" {
				t.Fatalf("want unit 1's valid follow-up retained without a malformed entry, got %v", got)
			}
		})
	}
}

func TestResumeKeepsAFailedUnitsFollowUpsUntilItsReportReplacesThem(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "test", failsOnUnit2))
	t.Setenv("ATM_TEST_CRITERION", "Unit two's gap is documented.")
	t.Setenv("FAKE_AGENT_WORK", failingUnit2Work)
	saved := func(label string) []obj {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, ".atm", "runs", label, "follow-ups.json"))
		var got []obj
		if err == nil {
			err = json.Unmarshal(b, &got)
		}
		if err != nil {
			t.Fatalf("%s's follow-ups.json: %v\n%s", label, err, b)
		}
		return got
	}
	issue := issueFile(t, followUpIssue)
	if err := Run("failed", []string{issue}, io.Discard, io.Discard); err == nil {
		t.Fatal("want unit 2's checks to fail")
	}
	wantFollowUps(t, saved("failed"), false)
	if err := Run("again", []string{"--resume", "failed", issue}, io.Discard, io.Discard); err == nil {
		t.Fatal("want unit 2's checks to fail again")
	}
	wantFollowUps(t, saved("again"), false) // kept, once
	if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(atmYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run("resumed", []string{"--resume", "again", issue}, io.Discard, io.Discard); err != nil {
		t.Fatalf("resume: %v", err)
	}
	wantFollowUps(t, saved("resumed"), true) // its report's own, now verified
}
