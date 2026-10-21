//go:build unix

package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The real terminal must unfold the Slop detector row into its phases and list every finding the ponytail agent
// reported with its verdict: the run deletes old.txt, and the cut that brings it back adds code.
func TestRealScreenShowsSlopDetectorPhases(t *testing.T) {
	builtATM(t)
	top := atmRepo(t)
	if err := os.WriteFile(filepath.Join(top, "old.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, top, "add", "old.txt")
	gitT(t, top, "commit", "-q", "--amend", "--no-edit")
	gitT(t, top, "update-ref", "refs/remotes/origin/main", "main")
	t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest+"; rm old.txt")
	t.Setenv("FAKE_PONYTAIL_WORK", "echo fixed > a.txt; echo old > old.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts("a.txt", "old.txt"))
	f, shown := realScreen(t, top)
	// The required phase values stay readable in the real 100-column terminal.
	for _, want := range []string{"✓ agent reading", "✓ 2 findings", "✓ judged", "1 kept", "✓ checks", "run 1",
		"✓ commit", "1 kept · 1 rejected: adds code", "run 1 ok", "ponytail: 1 cuts", "kept · a.txt", "cut a.txt", "rejected: adds code · old.txt", "cut old.txt"} {
		untilShown(t, f, shown, want)
	}
	f.exit(0)
}

// realScreen is real ATM's run of issue in top on a 100-column terminal, and what the terminal showed so far.
func realScreen(t *testing.T, top string) (*followed, *strings.Builder) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	return startFollow(t, versionServer(t, top, "follow-test"), top, true, "run", issueFile(t, issue)),
		&strings.Builder{}
}

// Issue 586 requires the file-specific verdict even when the remaining cut fails globally.
func TestRealScreenPreservesSpecificSlopRejection(t *testing.T) {
	builtATM(t)
	top := atmRepo(t)
	writeFixture(t, filepath.Join(top, "old.txt"), "old\n")
	gitT(t, top, "add", "old.txt")
	gitT(t, top, "commit", "-q", "--amend", "--no-edit")
	gitT(t, top, "update-ref", "refs/remotes/origin/main", "main")
	t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest+"; rm old.txt")
	t.Setenv("FAKE_PONYTAIL_WORK", "printf 'fixed\\nchanged\\nmore\\n' > a.txt; echo old > old.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts("old.txt", "a.txt"))
	f, shown := realScreen(t, top)
	for _, want := range []string{"✓ judged", "0 kept · 2 rejected: adds code", "rejected: adds code · old.txt",
		"rejected: does not shorten", "cut a.txt", "cut old.txt"} {
		untilShown(t, f, shown, want)
	}
	f.exit(0)
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) != 1 {
		t.Fatalf("want one run, got %+v, %v", runs, err)
	}
	var verdicts []ruling
	_, err = Follow(top, runs[0].Run, nil, nil, func(f Frame) {
		var ev struct {
			Step, State string
			Verdicts    []ruling
		}
		if len(f.Event) != 0 && json.Unmarshal(f.Event, &ev) == nil && ev.Step == "ponytail" && ev.State == "passed" {
			verdicts = ev.Verdicts
		}
	})
	if err != nil || len(verdicts) != 2 || verdicts[0].Verdict != "rejected: adds code" ||
		!strings.HasPrefix(verdicts[1].Verdict, "rejected: does not shorten the diff:") {
		t.Fatalf("specific and global verdicts lost: %+v, %v", verdicts, err)
	}
}

// Issue 586 requires truthful put-back rows for each restored file, including full restoration after checks.
func TestRealScreenShowsFullSlopRestoration(t *testing.T) {
	builtATM(t)
	for _, c := range []struct {
		name, seed, work, lint string
		files                  []string
		checks                 string
	}{
		{name: "single unnamed failure", work: "echo fixed > a.txt",
			lint: "grep -q extra a.txt || { echo failed; exit 1; }", files: []string{"a.txt"}, checks: "run 1 failed"},
		{name: "single named failure", seed: "; printf 'keep\\nextra\\n' > b.txt",
			work: "echo keep > b.txt", lint: "grep -q extra b.txt || { echo b.txt; exit 1; }",
			files: []string{"b.txt"}, checks: "run 1 failed: b.txt"},
		{name: "no individual restoration holds", seed: "; printf 'keep\\nextra\\n' > b.txt",
			work:  "echo fixed > a.txt; echo keep > b.txt",
			lint:  "{ grep -q extra a.txt && grep -q extra b.txt; } || { echo failed; exit 1; }",
			files: []string{"a.txt", "b.txt"}, checks: "run 3 failed"},
		{name: "named then unnamed failure", seed: "; printf 'keep\\nextra\\n' > b.txt",
			work:  "echo fixed > a.txt; echo keep > b.txt",
			lint:  "grep -q extra b.txt || { echo b.txt; exit 1; }; grep -q extra a.txt || { echo failed; exit 1; }",
			files: []string{"a.txt", "b.txt"}, checks: "run 2 failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			delivered := filepath.Join(t.TempDir(), "delivered")
			t.Setenv("ATM_TEST_OUT", delivered)
			t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest+c.seed)
			t.Setenv("FAKE_PONYTAIL_WORK", c.work)
			t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts(c.files...))
			cfg := atmSet(atmSet(atmYAML, "lint", c.lint), "delivery", `cat a.txt > "$ATM_TEST_OUT"; if test -f b.txt; then cat b.txt >> "$ATM_TEST_OUT"; fi`)
			top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
			f, shown := realScreen(t, top)
			untilShown(t, f, shown, c.checks)
			if c.name == "single named failure" {
				untilShown(t, f, shown, "run 1 failed: b.txt")
			}
			for _, path := range c.files {
				untilShown(t, f, shown, "✓ put back "+path)
				untilShown(t, f, shown, "discarded: checks failed · "+path)
			}
			f.exit(0)
			want := "fixed\nextra\nmore\n"
			if c.seed != "" {
				want += "keep\nextra\n"
			}
			got, err := os.ReadFile(delivered)
			if err != nil || string(got) != want {
				t.Fatalf("delivery did not receive the restored unit: %q, %v", got, err)
			}
			runs, err := RecordedRuns(top)
			if err != nil || len(runs) != 1 {
				t.Fatalf("want one run, got %+v, %v", runs, err)
			}
			putBack := map[string]int{}
			_, err = Follow(top, runs[0].Run, nil, nil, func(f Frame) {
				var ev struct{ Step, Check, State string }
				if len(f.Event) != 0 && json.Unmarshal(f.Event, &ev) == nil && ev.Step == "ponytail" && ev.State == "passed" && strings.HasPrefix(ev.Check, "put back ") {
					putBack[strings.TrimPrefix(ev.Check, "put back ")]++
				}
			})
			if err != nil || len(putBack) != len(c.files) {
				t.Fatalf("restoration events: %v, %v", putBack, err)
			}
			for _, path := range c.files {
				if putBack[path] != 1 {
					t.Fatalf("want exactly one restoration of %s, got %v", path, putBack)
				}
			}
		})
	}
}

func TestRealScreenShowsCompleteSlopReferences(t *testing.T) {
	builtATM(t)
	const path = "internal/run/ponytail.go"
	for _, c := range []struct {
		name, work, lint, verdict string
		pair                      bool
	}{
		{name: "global rejection", work: "printf 'changed\\nextra\\n' > " + path,
			verdict: "rejected: does not shorten the diff: 4 net added lines, 4 before"},
		{name: "full restoration", work: "echo keep > " + path,
			lint: "grep -q extra " + path + " || { echo failed; exit 1; }", verdict: "discarded: checks failed"},
		{name: "named restoration", work: "echo keep > " + path, pair: true,
			lint: "grep -q extra " + path + " || { echo " + path + "; exit 1; }", verdict: "discarded: checks failed"},
		{name: "individual restoration", work: "echo keep > " + path, pair: true,
			lint: "grep -q extra " + path + " || { echo failed; exit 1; }", verdict: "discarded: checks failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			delivered := filepath.Join(t.TempDir(), "delivered")
			t.Setenv("ATM_TEST_OUT", delivered)
			t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest+
				"; mkdir -p internal/run; printf 'keep\\nextra\\n' > "+path)
			work, files := c.work, []string{path}
			if c.pair {
				work += "; echo fixed > a.txt"
				files = append(files, "a.txt")
			}
			t.Setenv("FAKE_PONYTAIL_WORK", work)
			t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts(files...))
			cfg := atmSet(atmSet(atmYAML, "lint", c.lint), "delivery", "cat "+path+` > "$ATM_TEST_OUT"`)
			top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
			f, shown := realScreen(t, top)
			deadline := time.After(2 * time.Minute)
			open := true
			for open {
				select {
				case line, more := <-f.lines:
					open = more
					shown.WriteString(line + "\n")
				case <-deadline:
					t.Fatal("the real terminal did not finish")
				}
			}
			f.exit(0)
			screen := ansi.Strip(shown.String())
			start := strings.LastIndex(screen, "╭─ ATM")
			if start < 0 {
				t.Fatalf("missing final terminal view:\n%s", screen)
			}
			var left, right []string
			for _, line := range strings.Split(screen[start:], "\n") {
				cells := strings.Split(line, "│")
				if len(cells) >= 3 {
					left = append(left, cells[1])
				}
				if len(cells) == 5 {
					right = append(right, cells[3])
				}
			}
			want := "W " + c.verdict + " · " + path + " cut " + path
			if got := strings.Join(strings.Fields(strings.Join(right, "\n")), " "); !strings.Contains(got, want) {
				t.Fatalf("issue 586 requires the complete verdict, then file and finding, want %q:\n%s", want, screen[start:])
			}
			if !regexp.MustCompile(`✓ put back\s*\n\s*internal/run/ponytail\.go`).MatchString(strings.Join(left, "\n")) {
				t.Fatalf("issue 586 requires the complete restored path on its continuation row:\n%s", screen[start:])
			}
			got, err := os.ReadFile(delivered)
			if err != nil || string(got) != "keep\nextra\n" {
				t.Fatalf("the displayed restoration did not reach delivery: %q, %v", got, err)
			}
		})
	}
}
