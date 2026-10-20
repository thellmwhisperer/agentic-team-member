package run

import (
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

func TestPonytailDecisionPersistsInReport(t *testing.T) {
	for _, c := range []struct {
		name, work, report, reason string
		after                      int
	}{
		{name: "kept", work: "echo fixed > a.txt", report: cut},
		{name: "does not shorten", report: cut, after: 2,
			reason: "the cut does not shorten the diff: 2 net added lines, 2 before"},
		{name: "touches issue", work: "echo fixed > a.txt",
			report: strings.Replace(cut, `"issue_sentence": ""`, `"issue_sentence": "Retry once."`, 1)}, // kept
		{name: "touches test", work: "echo bad > a_test.sh", report: cut,
			reason: "the cut touches the test a_test.sh"},
		{name: "fresh", work: "echo fixed > a.txt; mkdir -p .atm; echo injected > .atm/injected.txt", report: cut,
			reason: "the cut adds .atm/injected.txt"},
		{name: "reprove", work: "echo broken > a.txt", report: cut,
			reason: "a_test.sh fails with the fix: exit status 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, clone, sha, cfg := unitDone(t, c.work)
			if c.name == "touches test" {
				if _, err := sh(clone, "echo fixed > a.txt; printf 'true\ntrue\n' >> a_test.sh"); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("FAKE_PONYTAIL_REPORT", c.report)
			n := ponytailReport(t, root, clone, sha, cfg)
			var reported obj
			if err := json.Unmarshal([]byte(c.report), &reported); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]any{
				"kept": c.reason == "", "reason": c.reason,
				"net_lines": []any{float64(2), float64(c.after)}, "findings": reported["findings"],
			} {
				if got, ok := n[key]; !ok || !reflect.DeepEqual(got, want) {
					t.Errorf("ponytail node %s = %#v (present: %t), want %#v", key, got, ok, want)
				}
			}
			if got := n["rejected_files"]; c.reason == "" && !reflect.DeepEqual(got, []any{}) {
				t.Errorf("a kept cut persisted rejected_files %#v, want none", got)
			}
		})
	}
}

func TestPonytailRejectedFilesInReport(t *testing.T) {
	for _, c := range []struct {
		name, work, report string
		kept               bool
		rejected           []obj
	}{
		{name: "kept in full", work: "echo fixed > a.txt", kept: true},
		{name: "no cut"},
		{name: "partly kept beside a test", work: "echo fixed > a.txt; echo bad > a_test.sh", kept: true,
			rejected: []obj{{"path": "a_test.sh", "reason": "the cut touches the test a_test.sh"}}},
		{name: "partly kept beside a deletion", work: "echo fixed > a.txt; rm b.txt", kept: true,
			rejected: []obj{{"path": "b.txt", "reason": "the cut deletes b.txt"}}},
		{name: "partly kept beside an addition", work: "echo fixed > a.txt; echo new > c.txt", kept: true,
			rejected: []obj{{"path": "c.txt", "reason": "the cut adds c.txt"}}},
		{name: "partly kept beside an outside file", work: "echo fixed > a.txt; echo changed > b.txt", kept: true,
			rejected: []obj{{"path": "b.txt", "reason": "the cut touches b.txt, outside the run's diff"}}},
		{name: "rejected in full", work: "echo fixed > a.txt", kept: true, // quoting the issue undoes nothing
			report: strings.Replace(cut, `"issue_sentence": ""`, `"issue_sentence": "Retry once."`, 1)},
		{name: "remainder fails proof", work: "echo broken > a.txt; echo bad > a_test.sh",
			rejected: []obj{
				{"path": "a.txt", "reason": "a_test.sh fails with the fix: exit status 1"},
				{"path": "a_test.sh", "reason": "the cut touches the test a_test.sh"},
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, clone, sha, cfg := unitDone(t, c.work)
			if c.report == "" {
				c.report = cut
			}
			t.Setenv("FAKE_PONYTAIL_REPORT", c.report)
			n := ponytailReport(t, root, clone, sha, cfg)
			if n["kept"] != c.kept {
				t.Fatalf("kept = %v, want %t", n["kept"], c.kept)
			}
			want := make([]any, 0, len(c.rejected))
			for _, f := range c.rejected {
				want = append(want, f)
			}
			if got := n["rejected_files"]; !reflect.DeepEqual(got, want) {
				t.Errorf("rejected_files = %#v, want %#v", got, want)
			}
			if reason, ok := n["reason"].(string); !ok || c.kept != (reason == "") {
				t.Errorf("kept %t persisted reason %#v, want one only when undone", c.kept, n["reason"])
			}
		})
	}
}

func ponytailReport(t *testing.T, root, clone, sha string, cfg config.Config) obj {
	t.Helper()
	dir := t.TempDir()
	r := newVerdict(dir, io.Discard)
	r.step("ponytail", func() (map[string]any, error) {
		return agent{Agent: cfg.Agent, dir: dir}.ponytail(root, clone, sha,
			[]proof{unitProof(t, clone, sha)}, fixIssue, cfg)
	})
	if r.err != nil {
		t.Fatal(r.err)
	}
	return reportNode(t, readReport(t, filepath.Join(dir, "report.json")), "ponytail")
}
