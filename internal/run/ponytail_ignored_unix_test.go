//go:build unix

package run

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A real run keeps a ponytail cut whose commands leave files under an ignored directory, and the clone loses
// them; a file the cut adds that git does not ignore is still thrown away, as "the cut adds".
func TestPonytailCutKeptWhenItsCommandsLeaveIgnoredFiles(t *testing.T) {
	builtATM(t)
	for _, c := range []struct {
		name, work, left string
		rejected         []any
	}{
		{name: "ignored file", work: "echo fixed > a.txt; mkdir -p .tmp/go-build; echo cache > .tmp/go-build/x",
			left: "none\nfixed\n", rejected: []any{}},
		{name: "tracked file", work: "echo fixed > a.txt; echo new > c.txt", left: "none\nfixed\n",
			rejected: []any{map[string]any{"path": "c.txt", "reason": "the cut adds c.txt"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			delivered := filepath.Join(t.TempDir(), "delivered")
			t.Setenv("ATM_TEST_OUT", delivered)
			t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest)
			t.Setenv("FAKE_PONYTAIL_WORK", c.work)
			t.Setenv("FAKE_PONYTAIL_REPORT", cut)
			cfg := atmSet(atmYAML, "delivery", `{ ls .tmp/go-build/x 2>/dev/null || echo none; ls c.txt 2>/dev/null; `+
				`git show HEAD:a.txt; } > "$ATM_TEST_OUT"`)
			top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
			if err := os.WriteFile(filepath.Join(top, ".gitignore"), []byte("/.tmp/\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitT(t, top, "add", ".gitignore")
			gitT(t, top, "commit", "-q", "--amend", "--no-edit")
			gitT(t, top, "update-ref", "refs/remotes/origin/main", "main")
			bin := versionServer(t, top, "follow-test")
			startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue)).exit(0)
			runs, err := RecordedRuns(top)
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 1 || runs[0].Outcome != "passed" {
				t.Fatalf("got %+v, want one passed run", runs)
			}
			n := reportNode(t, readReport(t, filepath.Join(runDir(top, runs[0].Run), "report.json")), "ponytail")
			if n["kept"] != true || !reflect.DeepEqual(n["rejected_files"], c.rejected) {
				t.Fatalf("want the cut kept with rejected_files %v, got %v", c.rejected, n)
			}
			if got, err := os.ReadFile(delivered); err != nil || string(got) != c.left {
				t.Fatalf("the delivery saw %q, %v; want %q", got, err, c.left)
			}
		})
	}
}
