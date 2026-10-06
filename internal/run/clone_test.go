package run

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const issue = "# Retry on timeout\nType: fix\nRetry once.\n"

// atmInstall is atmYAML with install set to line.
func atmInstall(line string) string {
	return strings.Replace(atmYAML, `install: ""`, "install: '"+line+"'", 1)
}

// cloneStep is the clone step's end event, after checking issue and contract passed.
func cloneStep(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	evs := events(t, out)
	if len(evs) != 6 || evs[1]["state"] != "passed" ||
		evs[2]["step"] != "contract" || evs[3]["state"] != "passed" ||
		evs[4]["step"] != "clone" || evs[4]["state"] != "started" ||
		evs[5]["step"] != "clone" {
		t.Fatalf("want the issue, contract, and clone steps, got %v", evs)
	}
	return evs[5]
}

// clones is what is left under root's .atm/clones.
func clones(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".atm", "clones"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestRunClonesBaseDetachedAndInstalls(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		base string
	}{
		{name: "main by default", base: "main"},
		{name: "--base-ref", args: []string{"--base-ref", "feature"}, base: "feature"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := filepath.Join(t.TempDir(), "install")
			t.Setenv("ATM_TEST_OUT", got)
			root := repo(t, "https://example.com/owner/repo.git", atmInstall(`{ basename "$PWD"; git rev-parse HEAD; `+
				`git symbolic-ref -q HEAD || echo detached; tail -n 1 .git/info/exclude; } > "$ATM_TEST_OUT"`))
			// The source is on feature, one commit ahead of main: the clone follows the base, not HEAD.
			gitT(t, root, "checkout", "-q", "-b", "feature")
			gitT(t, root, "commit", "-q", "--allow-empty", "-m", "feature")
			sha := gitT(t, root, "rev-parse", c.base)
			var out bytes.Buffer
			if err := Run(append(c.args, issueFile(t, issue)), &out); err != nil {
				t.Fatal(err)
			}
			end := cloneStep(t, &out)
			dir, _ := end["clone"].(string)
			top := gitT(t, root, "rev-parse", "--show-toplevel")
			if end["state"] != "passed" || end["sha"] != sha ||
				!strings.HasPrefix(dir, filepath.Join(top, ".atm", "clones", "atm-run-")) {
				t.Fatalf("end event: %v", end)
			}
			b, err := os.ReadFile(got)
			if err != nil {
				t.Fatal("install did not run: ", err)
			}
			if want := filepath.Base(dir) + "\n" + sha + "\ndetached\n/.atm/\n"; string(b) != want {
				t.Fatalf("install saw %q, want %q", b, want)
			}
			if left := clones(t, root); len(left) != 0 {
				t.Fatalf("the run's clone outlived it: %v", left)
			}
		})
	}
}

func TestRunDiesWhenCloneFails(t *testing.T) {
	cases := []struct {
		name, atm, why string
		args           []string
	}{
		{name: "missing base ref", atm: atmYAML, args: []string{"--base-ref", "nope"}, why: `base ref "nope"`},
		{name: "install fails", atm: atmInstall("echo broken >&2; exit 3"), why: "broken"},
		{name: "install times out", atm: atmInstall(`while :; do :; done`), why: "timed out"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", c.atm)
			if c.name == "install times out" {
				defer func(d time.Duration) { timeout = d }(timeout)
				timeout = 2 * time.Second
			}
			var out bytes.Buffer
			err := Run(append(c.args, issueFile(t, issue)), &out)
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			}
			if end := cloneStep(t, &out); end["state"] != "failed" || end["error"] != err.Error() {
				t.Fatalf("end event: %v", end)
			}
			if left := clones(t, root); len(left) != 0 {
				t.Fatalf("a failed run left its clone: %v", left)
			}
		})
	}
}

func TestClaimSuffixesRunsInTheSameSecond(t *testing.T) {
	root := filepath.Join(t.TempDir(), "clones")
	now := time.Date(2026, 10, 6, 23, 39, 6, 0, time.Local)
	for _, want := range []string{"atm-run-20261006-233906", "atm-run-20261006-233906-2", "atm-run-20261006-233906-3"} {
		dir, err := claim(root, now)
		if err != nil || dir != filepath.Join(root, want) {
			t.Fatalf("claim = %q, %v; want %s", dir, err, want)
		}
	}
}

// deadPID is the pid of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("git", "--version")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// oldClone is a clone an earlier run left under root, with its run's pid and, when delivered, branch atm/fix,
// one commit ahead of main when ahead.
func oldClone(t *testing.T, root string, pid int, delivered, ahead bool) string {
	t.Helper()
	old := filepath.Join(root, ".atm", "clones", "atm-run-20261001-120000")
	if delivered {
		gitT(t, root, "clone", "-q", root, old)
		gitT(t, old, "checkout", "-q", "-b", "atm/fix")
		if ahead {
			gitT(t, old, "commit", "-q", "--allow-empty", "-m", "fix")
		}
		if err := os.WriteFile(old+".delivered", []byte("atm/fix main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old+".pid", []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	return old
}

func TestRunSweepsClonesNoRunNeeds(t *testing.T) {
	cases := []struct {
		name      string
		live      bool   // the clone's run still runs
		delivered bool   // the clone delivered branch atm/fix
		ahead     bool   // atm/fix has a commit main lacks
		pr        string // the PR's state on GitHub; empty is no GitHub
		kept      bool
	}{
		{name: "run died", kept: false},
		{name: "run alive", live: true, kept: true},
		{name: "delivered and merged", delivered: true, kept: false},
		{name: "delivered and not merged, no GitHub", delivered: true, ahead: true, kept: true},
		{name: "delivered, PR open", delivered: true, ahead: true, pr: "OPEN", kept: true},
		{name: "delivered, PR closed", delivered: true, ahead: true, pr: "CLOSED", kept: false},
		{name: "delivered, PR merged", delivered: true, ahead: true, pr: "MERGED", kept: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			origin := "https://example.com/owner/repo.git"
			if c.pr != "" {
				origin = "https://github.com/owner/repo"
			}
			root := repo(t, origin, atmYAML)
			var args string
			if c.pr != "" {
				args = fakeGH(t, c.pr, false)
			}
			pid := deadPID(t)
			if c.live {
				pid = os.Getpid()
			}
			old := oldClone(t, root, pid, c.delivered, c.ahead)
			var out bytes.Buffer
			if err := Run([]string{issueFile(t, issue)}, &out); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(old)
			if kept := err == nil; kept != c.kept {
				t.Fatalf("kept = %v, want %v; left %v", kept, c.kept, clones(t, root))
			}
			if left := clones(t, root); !c.kept && len(left) != 0 {
				t.Fatalf("a removed clone left files: %v", left)
			}
			if c.pr != "" {
				if got, _ := os.ReadFile(args); string(got) != "pr view atm/fix --repo owner/repo --json state --jq .state" {
					t.Fatalf("gh args: %q", got)
				}
			}
		})
	}
}
