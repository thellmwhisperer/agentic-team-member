package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// ponytailStep is the ponytail step's end event, after checking the checks passed and nothing followed it.
func ponytailStep(t *testing.T, out *bytes.Buffer) obj {
	t.Helper()
	evs := events(t, out)
	if len(evs) != 12 || evs[9]["step"] != "checks" || evs[9]["state"] != "passed" || evs[10]["step"] != "ponytail" ||
		evs[10]["state"] != "started" || evs[11]["step"] != "ponytail" {
		t.Fatalf("want the checks passed, then the ponytail step, got %v", evs)
	}
	return evs[11]
}

// cut is a ponytail report with one finding on a.txt.
const cut = `{"findings": [{"file": "a.txt", "family": "speculative_feature", "finding": "dropped two lines"}], ` +
	`"summary": "s"}`

func TestRunPonytailFollowsTheChecks(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode", "pi"} {
		t.Run(harness, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			var out bytes.Buffer
			if err := Run([]string{"--harness", harness, issueFile(t, issue)}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			end := ponytailStep(t, &out)
			top := gitT(t, root, "rev-parse", "--show-toplevel")
			logs, _ := filepath.Glob(filepath.Join(top, ".atm", "ponytail-*.jsonl"))
			if end["state"] != "passed" || end["kept"] != false || len(logs) != 1 || end["log"] != logs[0] {
				t.Fatalf("want a lean run's pass discarded, logged in .atm/ponytail-*.jsonl, got %v, logs %v", end, logs)
			}
			b, err := os.ReadFile(filepath.Join(root, ".atm", "brief-ponytail.md"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"### Retry on timeout\n\nType: fix\nRetry once.", "-broken\n+fixed",
				"`ponytail-review`", "`speculative_feature`", "no new files", "`git commit`"} {
				if !strings.Contains(string(b), want) {
					t.Errorf("brief-ponytail.md lacks %q:\n%s", want, b)
				}
			}
		})
	}
}

func TestRunSkipsPonytailWhenAChecksFails(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "lint", "false"))
	call := filepath.Join(t.TempDir(), "call.json")
	t.Setenv("FAKE_AGENT_CALL", call)
	var out bytes.Buffer
	if err := Run([]string{issueFile(t, issue)}, &out, io.Discard); err == nil ||
		!strings.HasPrefix(err.Error(), "lint: ") {
		t.Fatalf("want lint to fail, got %v", err)
	}
	if end := checksStep(t, &out); end["state"] != "failed" {
		t.Fatalf("end event: %v", end)
	}
	if _, err := os.Stat(call + ".ponytail"); err == nil {
		t.Fatal("the ponytail agent ran after a failed check")
	}
}

func TestRunDiesWhenThePonytailAgentFails(t *testing.T) {
	finding := func(file, family string) string {
		return `{"findings": [{"file": "` + file + `", "family": "` + family + `", "finding": "f"}], "summary": "s"}`
	}
	cases := []struct {
		name, mode, work, report, why string
		hang                          bool
	}{
		{name: "exit", mode: "fail", why: "exit status 3"},
		{name: "hang", mode: "hang", why: "timed out", hang: true},
		{name: "no skill", mode: "no-skill", why: "ponytail-review"},
		{name: "no report", mode: "no-report", why: "no report"},
		{name: "no summary", report: `{"findings": []}`, why: "summary"},
		{name: "unknown key", report: `{"findings": [], "summary": "s", "cuts": 1}`, why: "cuts"},
		{name: "unknown family", report: finding("a.txt", "bloat"), why: "bloat"},
		{name: "file outside the diff", report: finding("c.txt", "speculative_feature"), why: "c.txt"},
		{name: "two-line finding", report: strings.Replace(cut, "dropped", `a\nb`, 1), why: "one line"},
		{name: "commits", work: "echo fixed > a.txt; git add -A; git commit -qm cut", report: cut, why: "commits"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			t.Setenv("FAKE_PONYTAIL", c.mode)
			t.Setenv("FAKE_PONYTAIL_WORK", c.work)
			t.Setenv("FAKE_PONYTAIL_REPORT", c.report)
			if c.hang {
				defer func(d time.Duration) { agentTimeout = d }(agentTimeout)
				agentTimeout = 3 * time.Second
			}
			var out bytes.Buffer
			err := Run([]string{issueFile(t, issue)}, &out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			}
			if end := ponytailStep(t, &out); end["state"] != "failed" || end["error"] != err.Error() {
				t.Fatalf("end event: %v", end)
			}
			if left := clones(t, root); len(left) != 0 {
				t.Fatalf("a failed run left its clone: %v", left)
			}
		})
	}
}

// unitDone is a clone of a repository whose main has a broken a.txt, its test and b.txt, with the work of a
// unit that passed every check left in it: a.txt fixed with two lines too many. The ponytail agent does work.
func unitDone(t *testing.T, work string) (root, dir, sha string, c config.Config) {
	t.Helper()
	root = repo(t, "https://example.com/owner/repo.git", atmYAML)
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "add", "b.txt")
	gitT(t, root, "commit", "-q", "-m", "b")
	c, err := config.Load(root, os.Getenv("HOME"), config.Agent{})
	if err == nil {
		dir, sha, err = clone(root, "main", "", "")
	}
	if err == nil {
		_, err = sh(dir, "printf 'fixed\\nextra\\nmore\\n' > a.txt; echo 'grep -q fixed a.txt' > a_test.sh")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PONYTAIL_WORK", work)
	return root, dir, sha, c
}

var fixIssue = Issue{Title: "Retry on timeout", Body: "Type: fix\nRetry once.", Type: "fix"}

// state is everything a pass could change in dir: HEAD, the index and the working tree.
func state(t *testing.T, dir string) string {
	t.Helper()
	head, staged := gitT(t, dir, "rev-parse", "HEAD"), gitT(t, dir, "diff", "--cached", "--name-status")
	gitT(t, dir, "add", "-A")
	defer gitT(t, dir, "reset", "-q")
	return head + "\n" + staged + "\n" + gitT(t, dir, "write-tree")
}

// noIdentity leaves git no identity but the one a test configures.
func noIdentity(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
		"EMAIL"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func TestPonytailKeepsAShorterGreenCut(t *testing.T) {
	root, clone, sha, c := unitDone(t, "echo fixed > a.txt")
	noIdentity(t)
	// The repository's own identity, which a clone does not inherit.
	gitT(t, root, "config", "user.name", "Repo Dev")
	gitT(t, root, "config", "user.email", "dev@example.com")
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	ev, err := agent{Agent: c.Agent}.ponytail(root, clone, sha, "a_test.sh", fixIssue, c)
	if err != nil || ev["kept"] != true {
		t.Fatalf("want the cut kept, got %v, %v", err, ev)
	}
	if got := gitT(t, clone, "rev-parse", "HEAD~2"); got != sha {
		t.Fatalf("want two commits on the base %s, HEAD~2 is %s", sha, got)
	}
	if got := gitT(t, clone, "log", "-1", "--format=%B", "HEAD~1"); got != "atm unit 1: Retry on timeout" {
		t.Fatalf("unit commit: %q", got)
	}
	if got := gitT(t, clone, "show", "HEAD~1:a.txt"); got != "fixed\nextra\nmore" {
		t.Fatalf("the unit commit holds the work before the cut, a.txt is %q", got)
	}
	if got := gitT(t, clone, "log", "-1", "--format=%B"); got != "ponytail: 1 cuts\n\n- a.txt: dropped two lines "+
		"(speculative_feature)" {
		t.Fatalf("ponytail commit: %q", got)
	}
	files := strings.Fields(gitT(t, clone, "show", "--name-only", "--format=", "HEAD"))
	if len(files) != 2 || !strings.HasPrefix(files[0], ".slop/tombstones/T-PONYTAIL-") || files[1] != "a.txt" {
		t.Fatalf("want the cut and its tombstone in the ponytail commit, got %v", files)
	}
	tomb := gitT(t, clone, "show", "HEAD:"+files[0])
	for _, want := range []string{"family: speculative_feature", `artifact: "a.txt"`, `"dropped two lines"`} {
		if !strings.Contains(tomb, want) {
			t.Errorf("tombstone lacks %q:\n%s", want, tomb)
		}
	}
	id := "Repo Dev <dev@example.com> Repo Dev <dev@example.com>"
	if got := gitT(t, clone, "log", "-2", "--format=%an <%ae> %cn <%ce>"); got != id+"\n"+id {
		t.Fatalf("want the repository's identity on both commits, got %q", got)
	}
	if got := gitT(t, clone, "status", "--porcelain"); got != "" {
		t.Fatalf("want a clean clone, got %q", got)
	}
}

func TestPonytailDiscardsACutThatDoesNotHold(t *testing.T) {
	cases := []struct{ name, work, report, why string }{
		{name: "longer", work: "echo extra >> a.txt", report: cut, why: "shorten"},
		{name: "no findings", work: "echo fixed > a.txt", why: "no finding"},
		{name: "breaks a check", work: "echo broken > a.txt", report: cut, why: "fails with the fix"},
		{name: "new file", work: "echo fixed > a.txt; echo c > c.txt", report: cut, why: "c.txt"},
		{name: "outside the diff", work: "echo fixed > a.txt; rm b.txt", report: cut, why: "b.txt"},
		{name: "touches test", work: "echo fixed > a.txt; echo bad > a_test.sh",
			report: cut, why: "touches the test a_test.sh"},
		{name: "deletes file", work: "rm a.txt", report: cut, why: "deletes a.txt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, clone, sha, cfg := unitDone(t, c.work)
			t.Setenv("FAKE_PONYTAIL_REPORT", c.report)
			before := state(t, clone)
			ev, err := agent{Agent: cfg.Agent}.ponytail(root, clone, sha, "a_test.sh", fixIssue, cfg)
			reason, _ := ev["reason"].(string)
			if err != nil || ev["kept"] != false || !strings.Contains(reason, c.why) {
				t.Fatalf("want the cut discarded for %q, got %v, %v", c.why, err, ev)
			}
			if after := state(t, clone); after != before {
				t.Fatalf("the clone did not come back:\n%s\nwant\n%s", after, before)
			}
		})
	}
}

func TestPonytailDiesWithoutTheRepositoryIdentityBeforeCommitting(t *testing.T) {
	for _, c := range []struct{ name, email, why string }{
		{name: "missing", why: "identity"},
		{name: "localhost", email: "atm@localhost", why: "@localhost"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, clone, sha, cfg := unitDone(t, "echo fixed > a.txt")
			noIdentity(t)
			if c.email != "" {
				gitT(t, root, "config", "user.name", "atm")
				gitT(t, root, "config", "user.email", c.email)
			}
			t.Setenv("FAKE_PONYTAIL_REPORT", cut)
			_, err := agent{Agent: cfg.Agent}.ponytail(root, clone, sha, "a_test.sh", fixIssue, cfg)
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			}
			if head := gitT(t, clone, "rev-parse", "HEAD"); head != sha {
				t.Fatalf("a commit was made: HEAD %s, base %s", head, sha)
			}
		})
	}
}
