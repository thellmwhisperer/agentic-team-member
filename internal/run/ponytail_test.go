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
const cut = `{"findings": [{"file": "a.txt", "family": "speculative_feature", "finding": "dropped two lines", ` +
	`"issue_sentence": ""}], "summary": "s"}`

func TestRunPonytailFollowsTheChecks(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode", "pi"} {
		t.Run(harness, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			var out bytes.Buffer
			if err := Run("t", []string{"--harness", harness, issueFile(t, issue)}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			end := ponytailStep(t, &out)
			top := gitT(t, root, "rev-parse", "--show-toplevel")
			logs, _ := filepath.Glob(filepath.Join(top, ".atm", "runs", "t", "ponytail.jsonl"))
			if end["state"] != "passed" || end["kept"] != false || len(logs) != 1 || end["log"] != logs[0] {
				t.Fatalf("want a lean run's pass discarded, logged in .atm/runs/t/ponytail.jsonl, got %v, logs %v", end, logs)
			}
			b, err := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "brief-ponytail.md"))
			if err != nil {
				t.Fatal(err)
			}
			brief := strings.ReplaceAll(string(b), "\r\n", "\n")
			for _, want := range []string{"### Retry on timeout\n\nType: fix\nRetry once.", "-broken\n+fixed",
				"`ponytail-review`", "`speculative_feature`", "no new files", "`git commit`",
				"Never cut code that does what a sentence of the issue asks", `"issue_sentence"`,
				"## SCOPE\n" + reachScope + "\n" + netScope,
				"- Writing or changing files through shell commands (heredocs, `cat >`, `sed -i`, `python -c` and the like). " +
					"Use your edit and write tools.",
				"- A URL inside any shell command. ATM ends the run on it, even with no network access. " +
					"A URL may appear only in files written with your edit tools.",
				"- Running a command in the background, or ending your turn while one runs. " +
					"Run every command in the foreground. Your turn ends only with the report."} {
				if !strings.Contains(brief, want) {
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
	if err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard); err == nil ||
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
		{name: "exit", mode: "fail", why: "exit code 3"},
		{name: "hang", mode: "hang", why: "timed out", hang: true},
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
			err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			}
			assertFailedCleanup(t, root, ponytailStep(t, &out), err)
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
		dir, sha, _, err = clone(root, "main", "", "", nil)
	}
	if err == nil {
		err = before(dir) // as units does, before the unit's agent
	}
	if err == nil {
		_, err = sh(dir, "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := release(dir, ""); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("FAKE_PONYTAIL_WORK", work)
	return root, dir, sha, c
}

func unitProof(t *testing.T, clone, base string) proof {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(clone, "a_test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return proof{base, "a_test.sh", string(content), false}
}

var fixIssue = Issue{Title: "Retry on timeout", Body: "Type: fix\nRetry once."}

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
	ev, err := agent{Agent: c.Agent, dir: t.TempDir()}.ponytail(root, clone, sha,
		[]proof{unitProof(t, clone, sha)}, fixIssue, c)
	if err != nil || ev["kept"] != true {
		t.Fatalf("want the cut kept, got %v, %v", err, ev)
	}
	if got := gitT(t, clone, "rev-parse", "HEAD~2"); got != sha {
		t.Fatalf("want two commits on the base %s, HEAD~2 is %s", sha, got)
	}
	if got := gitT(t, clone, "log", "-1", "--format=%B", "HEAD~1"); got != "chore: Retry on timeout\n\natm unit 1" {
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

func TestTombstonesNameNoProducer(t *testing.T) {
	clone := t.TempDir()
	writeFixture(t, filepath.Join(clone, "a.txt"), "fixed\n")
	paths, err := tombstones(clone, "", []finding{
		{File: "a.txt", Family: "speculative_feature", Finding: "dropped two lines"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("want one tombstone, got %v", paths)
	}
	text, err := os.ReadFile(filepath.Join(clone, paths[0]))
	if err != nil {
		t.Fatal(err)
	}
	want := `  what_went_wrong: "A coding agent wrote it and the ponytail pass cut it before delivery."` + "\n"
	if !strings.Contains(string(text), want) {
		t.Errorf("tombstone lacks %q:\n%s", want, text)
	}
	if strings.Contains(strings.ToLower(string(text)), "atm") {
		t.Errorf("tombstone names ATM:\n%s", text)
	}
}

func TestReproveFailsWhenAPriorUnitTestCommits(t *testing.T) {
	_, clone, sha, cfg := unitDone(t, "echo fixed > a.txt")
	cfg.Test = "sh a_test.sh && git -c user.name=Test -c user.email=test@example.com commit --allow-empty -qm test"
	proofs := []proof{unitProof(t, clone, sha), unitProof(t, clone, sha)}

	why, _, _, err := reprove(t.TempDir(), clone, sha, proofs, cfg)
	if err != nil || !strings.Contains(why, "the commands made commits") {
		t.Fatalf("want the prior unit's test commit to fail re-proving, got %v", err)
	}
	if head := gitT(t, clone, "rev-parse", "HEAD"); head == sha {
		t.Fatal("the test commit was hidden by resetting to the last unit's base")
	}
}

func TestReproveHidesATMFromAnEarlierUnit(t *testing.T) {
	_, clone, sha, cfg := unitDone(t, "")
	if _, err := sh(clone, "mkdir -p .atm/follow-ups/1; echo f > .atm/follow-ups/1/fixture.txt; "+
		"echo 'grep -q fixed a.txt && ! grep -q broken a.txt && cat .atm/follow-ups/1/fixture.txt' > a_test.sh"); err != nil {
		t.Fatal(err)
	}
	proofs := []proof{unitProof(t, clone, sha), unitProof(t, clone, sha)}

	why, _, _, err := reprove(t.TempDir(), clone, sha, proofs, cfg)
	if err != nil || !strings.HasPrefix(why, "test:") {
		t.Fatalf("want unit 1's re-proof to fail without .atm/, got %q, %v", why, err)
	}
	if b, err := os.ReadFile(filepath.Join(clone, ".atm", "follow-ups", "1", "fixture.txt")); string(b) != "f\n" {
		t.Fatalf("the follow-ups artefact did not come back: %q, %v", b, err)
	}
}

func TestPonytailDiscardsCutWhenAChainedUnitChangesEarlierTest(t *testing.T) {
	root, clone, sha, cfg := unitDone(t, "echo fixed > a.txt")
	earlier := unitProof(t, clone, sha)
	content := earlier.testContent + "# unit 2\n"
	if err := os.WriteFile(filepath.Join(clone, "a_test.sh"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	proofs := []proof{earlier, unitProof(t, clone, sha)}
	t.Setenv("FAKE_PONYTAIL_WORK", "echo fixed > a.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	before := state(t, clone)

	ev, err := agent{Agent: cfg.Agent, dir: t.TempDir()}.ponytail(root, clone, sha, proofs, fixIssue, cfg)
	if err != nil || ev["kept"] != false || !strings.Contains(ev["reason"].(string), "unit 1: test a_test.sh changed") {
		t.Fatalf("want the cut discarded because unit 2 changed unit 1's test, got %v, %v", err, ev)
	}
	if after := state(t, clone); after != before {
		t.Fatalf("the clone did not come back:\n%s\nwant\n%s", after, before)
	}
}

func TestPonytailDiscardsACutThatDoesNotHold(t *testing.T) {
	inject := "; echo injected > cache/injected.txt; echo new > cache/old.txt"
	cases := []struct{ name, work, report, why string }{
		{name: "longer", work: "echo extra >> a.txt", report: cut, why: "shorten"},
		{name: "no findings", work: "echo fixed > a.txt", why: "no finding"},
		{name: "breaks a check", work: "echo broken > a.txt", report: cut, why: "test:"},
		{name: "new ignored file", work: "echo fixed > a.txt" + inject, report: cut, why: "adds cache/injected.txt"},
		{name: "longer with ignored files", work: "echo extra >> a.txt" + inject, report: cut, why: "shorten"},
		{name: "deletes file", work: "rm a.txt", report: cut, why: "deletes a.txt"},
		{name: "touches the issue", work: "echo fixed > a.txt", report: strings.Replace(cut, `"issue_sentence": ""`,
			`"issue_sentence": "token() never returns an expired token"`, 1),
			why: ""}, // quoting the issue is no reason to undo: kept
		{name: "no issue_sentence", work: "echo fixed > a.txt", report: strings.Replace(cut, `, "issue_sentence": ""`, "", 1),
			why: "lacks issue_sentence"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, clone, sha, cfg := unitDone(t, c.work)
			t.Setenv("FAKE_PONYTAIL_REPORT", c.report)
			if _, err := sh(clone, "echo cache/ >> .git/info/exclude; mkdir cache; echo old > cache/old.txt"); err != nil {
				t.Fatal(err)
			}
			old, err := os.ReadFile(filepath.Join(clone, "cache", "old.txt"))
			if err != nil {
				t.Fatal(err)
			}
			before := state(t, clone)
			ev, err := agent{Agent: cfg.Agent, dir: t.TempDir()}.ponytail(root, clone, sha,
				[]proof{unitProof(t, clone, sha)}, fixIssue, cfg)
			if c.why == "" {
				if err != nil || ev["kept"] != true {
					t.Fatalf("want the cut kept, got %v, %v", err, ev)
				}
				keptCut(t, clone, sha)
				return
			}
			reason, _ := ev["reason"].(string)
			if err != nil || ev["kept"] != false || !strings.Contains(reason, c.why) {
				t.Fatalf("want the cut discarded for %q, got %v, %v", c.why, err, ev)
			}
			if after := state(t, clone); after != before {
				t.Fatalf("the clone did not come back:\n%s\nwant\n%s", after, before)
			}
			if _, err := os.Stat(filepath.Join(clone, "cache", "injected.txt")); !os.IsNotExist(err) {
				t.Fatalf("the ignored file the cut added is still in the clone: %v", err)
			}
			if b, err := os.ReadFile(filepath.Join(clone, "cache", "old.txt")); err != nil || !bytes.Equal(b, old) {
				t.Fatalf("the ignored file the cut changed did not come back: %q, %v", b, err)
			}
			if _, err := os.Stat(filepath.Join(clone, ".slop")); !os.IsNotExist(err) {
				t.Fatalf("a discarded cut wrote a tombstone: %v", err)
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
			_, err := agent{Agent: cfg.Agent, dir: t.TempDir()}.ponytail(root, clone, sha,
				[]proof{unitProof(t, clone, sha)}, fixIssue, cfg)
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			}
			if head := gitT(t, clone, "rev-parse", "HEAD"); head != sha {
				t.Fatalf("a commit was made: HEAD %s, base %s", head, sha)
			}
		})
	}
}

// twoCuts is a valid cut of a.txt and one of c.txt that quotes a sentence of the issue.
const twoCuts = `{"findings": [` +
	`{"file": "a.txt", "family": "speculative_feature", "finding": "dropped two lines", "issue_sentence": ""},` +
	`{"file": "c.txt", "family": "speculative_feature", "finding": "touches the issue", "issue_sentence": "Retry once."}` +
	`], "summary": "s"}`

// A rejected test must retain the bytes that were proven, even when Git would
// convert them on checkout. Otherwise a valid neighboring cut loses its proof.
func TestPonytailRestoresRejectedTestBytes(t *testing.T) {
	for _, autocrlf := range []string{"false", "true"} {
		for _, eol := range []struct{ name, text string }{{"LF", "\n"}, {"CRLF", "\r\n"}} {
			for _, work := range []string{"echo bad > a_test.sh", "rm a_test.sh"} {
				t.Run(autocrlf+"/"+eol.name+"/"+work, func(t *testing.T) {
					root, clone, sha, cfg := unitDone(t, "echo fixed > a.txt; "+work)
					gitT(t, clone, "config", "core.autocrlf", autocrlf)
					path := filepath.Join(clone, "a_test.sh")
					want := "grep -q fixed a.txt && ! grep -q broken a.txt # proof" + eol.text
					if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("FAKE_PONYTAIL_REPORT", cut)
					ev, err := agent{Agent: cfg.Agent, dir: t.TempDir()}.ponytail(root, clone, sha,
						[]proof{unitProof(t, clone, sha)}, fixIssue, cfg)
					if err != nil || ev["kept"] != true {
						t.Fatalf("want the valid cut kept after restoring the test, got %v, %v", err, ev)
					}
					if got, err := os.ReadFile(path); err != nil || string(got) != want {
						t.Fatalf("restored test = %q, %v; want %q", got, err, want)
					}
					keptCut(t, clone, sha)
				})
			}
		}
	}
}

func TestPonytailJudgesACutPerFile(t *testing.T) {
	touched := strings.Replace(cut, `"issue_sentence": ""`, `"issue_sentence": "Retry once."`, 1)
	longer := "printf 'fixed\\nextra\\nmore\\n" + strings.Repeat("extra\\n", 5) +
		"' > a.txt; echo x > a_test.sh"
	cases := []struct {
		name, work, report, why, seed, body string
	}{
		{name: "valid file beside a test", work: "echo fixed > a.txt; echo bad > a_test.sh", report: cut},
		{name: "valid file beside the issue", work: "echo fixed > a.txt; echo cut > c.txt", report: twoCuts,
			seed: "c.txt", body: "stay\n"},
		{name: "valid file beside a new file", work: "echo fixed > a.txt; echo c > c.txt", report: cut},
		{name: "valid file beside one outside the diff", work: "echo fixed > a.txt; rm b.txt", report: cut},
		{name: "only file touches the issue", work: "echo fixed > a.txt", report: touched}, // kept
		{name: "remainder does not shorten", work: longer, report: cut, why: "shorten",
			seed: "a_test.sh", body: strings.Repeat("grep -q fixed a.txt && ! grep -q broken a.txt\n", 10)},
		{name: "remainder breaks a proof", work: "echo broken > a.txt; echo bad > a_test.sh", report: cut,
			why: "test:"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, clone, sha, cfg := unitDone(t, c.work)
			if c.seed != "" {
				if err := os.WriteFile(filepath.Join(clone, c.seed), []byte(c.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("FAKE_PONYTAIL_REPORT", c.report)
			before := state(t, clone)
			ev, err := agent{Agent: cfg.Agent, dir: t.TempDir()}.ponytail(root, clone, sha,
				[]proof{unitProof(t, clone, sha)}, fixIssue, cfg)
			if c.why != "" {
				reason, _ := ev["reason"].(string)
				if err != nil || ev["kept"] != false || !strings.Contains(reason, c.why) || state(t, clone) != before {
					t.Fatalf("want no cut for %q, got %v, %v", c.why, err, ev)
				}
				return
			}
			if err != nil || ev["kept"] != true {
				t.Fatalf("want the valid file kept, got %v, %v", err, ev)
			}
			if c.report == twoCuts { // the cut that quotes the issue stays beside a.txt's
				if got := gitT(t, clone, "show", "HEAD:c.txt"); got != "cut" ||
					!strings.HasPrefix(gitT(t, clone, "log", "-1", "--format=%B"), "ponytail: 2 cuts") {
					t.Fatalf("want c.txt's cut kept too, c.txt is %q", got)
				}
			}
			keptCut(t, clone, sha, c.report == twoCuts)
		})
	}
}

// keptCut checks the ponytail commit is a.txt's cut and its tombstone, and every other file is as the unit left it.
func keptCut(t *testing.T, clone, sha string, extra ...bool) {
	t.Helper()
	if gitT(t, clone, "rev-parse", "HEAD~2") != sha {
		t.Fatalf("want two commits on %s", sha)
	}
	if got := gitT(t, clone, "show", "HEAD~1:a.txt"); got != "fixed\nextra\nmore" {
		t.Fatalf("unit commit a.txt: %q", got)
	}
	if got := gitT(t, clone, "show", "HEAD:a.txt"); got != "fixed" {
		t.Fatalf("cut a.txt: %q", got)
	}
	msg := "ponytail: 1 cuts\n\n- a.txt: dropped two lines (speculative_feature)"
	if len(extra) != 0 && extra[0] {
		msg = "ponytail: 2 cuts\n\n- a.txt: dropped two lines (speculative_feature)" +
			"\n- c.txt: touches the issue (speculative_feature)"
	}
	if got := gitT(t, clone, "log", "-1", "--format=%B"); got != msg {
		t.Fatalf("commit: %q", got)
	}
	diff := gitT(t, clone, "diff", "--name-only", "HEAD~1", "HEAD")
	if !strings.Contains(diff, "a.txt") {
		t.Fatalf("cut commit lacks a.txt: %q", diff)
	}
	for _, name := range strings.Split(diff, "\n") {
		if name != "a.txt" && (len(extra) == 0 || !extra[0] || name != "c.txt") &&
			!strings.HasPrefix(name, ".slop/tombstones/") {
			t.Fatalf("the cut commit touches %s", name)
		}
	}
	if got := gitT(t, clone, "status", "--porcelain"); got != "" {
		t.Fatalf("dirty clone: %q", got)
	}
}

func TestPonytailKeepsACutThatQuotesTheIssue(t *testing.T) {
	root, clone, sha, cfg := unitDone(t, "echo fixed > a.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", strings.Replace(cut, `"issue_sentence": ""`, `"issue_sentence": "Retry once."`, 1))
	ev, err := agent{Agent: cfg.Agent, dir: t.TempDir()}.ponytail(root, clone, sha,
		[]proof{unitProof(t, clone, sha)}, fixIssue, cfg)
	if err != nil || ev["kept"] != true {
		t.Fatalf("want the cut that quotes the issue kept, got %v, %v", err, ev)
	}
	keptCut(t, clone, sha)
}

// dependentCuts is a ponytail report with one finding on each of files, the first quoting the issue.
func dependentCuts(files ...string) string {
	var found []string
	for n, f := range files {
		sentence := ""
		if n == 0 {
			sentence = "Retry once."
		}
		found = append(found, `{"file": "`+f+`", "family": "speculative_feature", "finding": "cut `+f+
			`", "issue_sentence": "`+sentence+`"}`)
	}
	return `{"findings": [` + strings.Join(found, ", ") + `], "summary": "s"}`
}

const (
	dependentSeed = "printf 'package pair\\nfunc x(unused int) int {\\n return 1\\n}\\n' > x.go; " +
		"printf 'package pair\\nfunc y() int {\\n return x(1)\\n}\\n' > y.go; printf 'v1\\nextra\\n' > z.txt"
	cutX         = "printf 'package pair\\nfunc x() int { return 1 }\\n' > x.go"
	cutY         = "printf 'package pair\\nfunc y() int { return x() }\\n' > y.go"
	dependentCut = cutX + "; " + cutY
)

// The unit's x.go and y.go only build together, as a function and its caller do, and z.txt stands alone.
// The checks of each pass count its re-proofs, which may not exceed twice its cut files, plus one.
func TestPonytailJudgesDependentCutsTogether(t *testing.T) {
	const builds = "go test x.go y.go"
	cases := []struct {
		name, lint string
		files      []string
		kept       bool
		rejected   map[string]string // each undone file and a part of its reason
	}{
		{name: "together", files: []string{"x.go", "y.go"}, kept: true},
		{name: "a check names z.txt", files: []string{"x.go", "y.go", "z.txt"}, kept: true,
			lint:     `! grep -q broken z.txt || { echo "z.txt:1: broken"; exit 1; }`,
			rejected: map[string]string{"z.txt": "lint: exit status 1\nz.txt:1: broken"}},
		{name: "a check names no cut file", files: []string{"x.go", "y.go", "z.txt"}, kept: true,
			lint:     `! grep -q broken z.txt || { echo "a line is broken"; exit 1; }`,
			rejected: map[string]string{"z.txt": "lint: exit status 1\na line is broken"}},
		{name: "no removal holds", files: []string{"x.go", "y.go", "z.txt"},
			lint: `grep -q unused x.go || { echo "a line is broken"; exit 1; }`,
			rejected: map[string]string{"x.go": "a line is broken", "y.go": "a line is broken",
				"z.txt": "a line is broken"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, clone, sha, cfg := unitDone(t, dependentCut+"; echo broken > z.txt")
			if _, err := sh(clone, dependentSeed); err != nil {
				t.Fatal(err)
			}
			checkDependentCompiler(t, clone, builds)
			if len(c.files) == 2 {
				t.Setenv("FAKE_PONYTAIL_WORK", dependentCut)
			}
			counter := filepath.Join(t.TempDir(), "reproofs")
			cfg.Install, cfg.Typecheck, cfg.Lint = "echo >> '"+filepath.ToSlash(counter)+"'", builds, c.lint
			t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts(c.files...))
			before := state(t, clone)
			ev, err := agent{Agent: cfg.Agent, dir: t.TempDir()}.ponytail(root, clone, sha,
				[]proof{unitProof(t, clone, sha)}, fixIssue, cfg)
			if err != nil || ev["kept"] != c.kept {
				t.Fatalf("want kept %t, got %v, %v", c.kept, err, ev)
			}
			rejectedAs(t, ev, c.rejected)
			b, err := os.ReadFile(counter)
			if n := strings.Count(string(b), "\n"); err != nil || n > 2*len(c.files)+1 {
				t.Fatalf("%d re-proofs for %d cut files, %v", n, len(c.files), err)
			}
			if !c.kept {
				if state(t, clone) != before {
					t.Fatal("the clone did not come back")
				}
				return
			}
			keptTogether(t, clone)
		})
	}
}

func checkDependentCompiler(t *testing.T, clone, builds string) {
	t.Helper()
	for _, change := range []string{"true", cutX, cutY} {
		out, err := sh(clone, change+"; "+builds)
		if change == "true" && err != nil || change != "true" && (err == nil || !strings.Contains(out, "y.go:")) {
			t.Fatalf("compiler must accept the original pair and reject either cut alone: %s, %v", out, err)
		}
		if _, err := sh(clone, dependentSeed); err != nil {
			t.Fatal(err)
		}
	}
}

// rejectedAs checks ev's rejected files are want's, each with a reason holding want's part.
func rejectedAs(t *testing.T, ev map[string]any, want map[string]string) {
	t.Helper()
	got, _ := ev["rejected_files"].([]rejectedFile)
	if len(got) != len(want) {
		t.Fatalf("want rejected %v, got %v", want, got)
	}
	for _, f := range got {
		if want[f.Path] == "" || !strings.Contains(f.Reason, want[f.Path]) {
			t.Fatalf("want rejected %v, got %v", want, got)
		}
	}
}

// keptTogether checks the ponytail commit holds the cuts of x.go and y.go, and z.txt as the unit left it.
func keptTogether(t *testing.T, clone string) {
	t.Helper()
	for f, want := range map[string]string{
		"x.go": "package pair\nfunc x() int { return 1 }", "y.go": "package pair\nfunc y() int { return x() }",
	} {
		if got := gitT(t, clone, "show", "HEAD:"+f); got != want {
			t.Fatalf("want %s's cut kept, got %q", f, got)
		}
	}
	msg := "ponytail: 2 cuts\n\n- x.go: cut x.go (speculative_feature)\n- y.go: cut y.go (speculative_feature)"
	if got := gitT(t, clone, "log", "-1", "--format=%B"); got != msg {
		t.Fatalf("ponytail commit: %q", got)
	}
	if got := gitT(t, clone, "show", "HEAD:z.txt"); got != "v1\nextra" {
		t.Fatalf("want z.txt as the unit left it, got %q", got)
	}
	if got := gitT(t, clone, "status", "--porcelain"); got != "" {
		t.Fatalf("dirty clone: %q", got)
	}
}
