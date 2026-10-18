package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const issue = "# Retry on timeout\nType: fix\nRetry once.\n"

// atmInstall is atmYAML with install set to line.
func atmInstall(line string) string {
	return strings.Replace(atmYAML, `install: ""`, "install: '"+line+"'", 1)
}

// cloneStep is the clone step's end event, after checking issue passed and nothing followed a failure.
func cloneStep(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	evs := events(t, out)
	if len(evs) < 4 || evs[3]["state"] == "failed" && len(evs) != 4 || evs[1]["state"] != "passed" ||
		evs[2]["step"] != "clone" || evs[2]["state"] != "started" || evs[3]["step"] != "clone" {
		t.Fatalf("want the issue and clone steps, got %v", evs)
	}
	return evs[3]
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
				`git symbolic-ref -q HEAD || echo detached; tail -n 1 .git/info/exclude; } >> "$ATM_TEST_OUT"`))
			// The source is on feature, one commit ahead of main: the clone follows the base, not HEAD.
			gitT(t, root, "checkout", "-q", "-b", "feature")
			gitT(t, root, "commit", "-q", "--allow-empty", "-m", "feature")
			sha := gitT(t, root, "rev-parse", c.base)
			var out bytes.Buffer
			if err := Run("t", append(c.args, issueFile(t, issue)), &out, io.Discard); err != nil {
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
			// Install runs again in the checks, after the agent, whose skill is the last line excluded.
			saw := filepath.Base(dir) + "\n" + sha + "\ndetached\n"
			if want := saw + "/.atm/\n" + saw + "/.claude/skills/ponytail/\n"; string(b) != want {
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
			err := Run("t", append(c.args, issueFile(t, issue)), &out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			}
			assertFailedCleanup(t, root, cloneStep(t, &out), err)
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
		t.Cleanup(func() {
			if err := release(dir, ""); err != nil {
				t.Error(err)
			}
		})
	}
}

// oldClone is an earlier run's clone.
func oldClone(t *testing.T, root string, delivered, ahead bool) string {
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
	return old
}
