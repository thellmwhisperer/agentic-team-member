package run

import (
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
			report: strings.Replace(cut, `"issue_sentence": ""`, `"issue_sentence": "Retry once."`, 1),
			reason: "the cut touches the issue: Retry once."},
		{name: "touches test", work: "echo fixed > a.txt; echo bad > a_test.sh", report: cut,
			reason: "the cut touches the test a_test.sh"},
		{name: "fresh", work: "echo fixed > a.txt; mkdir -p .atm; echo injected > .atm/injected.txt", report: cut,
			reason: "the cut adds .atm/injected.txt"},
		{name: "reprove", work: "echo broken > a.txt", report: cut,
			reason: "a_test.sh fails with the fix: exit status 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, clone, sha, cfg := unitDone(t, c.work)
			t.Setenv("FAKE_PONYTAIL_REPORT", c.report)
			dir := t.TempDir()
			r := newVerdict(dir, io.Discard)
			r.step("ponytail", func() (map[string]any, error) {
				return agent{Agent: cfg.Agent, dir: dir}.ponytail(root, clone, sha,
					[]proof{unitProof(t, clone, sha)}, fixIssue, cfg)
			})
			if r.err != nil {
				t.Fatal(r.err)
			}
			var reported obj
			if err := json.Unmarshal([]byte(c.report), &reported); err != nil {
				t.Fatal(err)
			}
			n := reportNode(t, readReport(t, filepath.Join(dir, "report.json")), "ponytail")
			for key, want := range map[string]any{
				"kept": c.reason == "", "reason": c.reason,
				"net_lines": []any{float64(2), float64(c.after)}, "findings": reported["findings"],
			} {
				if got, ok := n[key]; !ok || !reflect.DeepEqual(got, want) {
					t.Errorf("ponytail node %s = %#v (present: %t), want %#v", key, got, ok, want)
				}
			}
		})
	}
}
