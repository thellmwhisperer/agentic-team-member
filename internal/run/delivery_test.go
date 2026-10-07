package run

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// deliveryLine is a delivery command that prints a line to stdout and one to stderr, then writes to the
// directory $ATM_TEST_OUT each ATM_ variable, "unset" when it is, and what it sees of the clone.
const deliveryLine = `echo out-line; echo err-line >&2; ` +
	`for v in ATM_TITLE ATM_ISSUE ATM_BRANCH ATM_CLONE ATM_REPORT ATM_PONYTAIL; do ` +
	`printenv $v > "$ATM_TEST_OUT/$v" || echo unset > "$ATM_TEST_OUT/$v"; done; ` +
	`git rev-parse --abbrev-ref HEAD > "$ATM_TEST_OUT/branch"; git rev-parse HEAD > "$ATM_TEST_OUT/head"; ` +
	`git status --porcelain > "$ATM_TEST_OUT/status"; git remote get-url origin > "$ATM_TEST_OUT/origin"; ` +
	`git log -2 --format="%s|%an <%ae>|%cn <%ce>" > "$ATM_TEST_OUT/log"; cp "$ATM_REPORT" "$ATM_TEST_OUT/report.json"`

func TestRunDeliversTheBranch(t *testing.T) {
	bare := t.TempDir()
	gitT(t, bare, "init", "-q", "--bare")
	dev := "Repo Dev <dev@example.com>|Repo Dev <dev@example.com>"
	cases := []struct {
		name, origin, issue, ponytail, log string
		kept                               bool
	}{
		{name: "file issue, cut discarded", origin: bare,
			log: "atm unit 1: Retry on timeout|" + dev + "\ninit|atm <atm@example.com>|atm <atm@example.com>"},
		{name: "GitHub issue, cut kept", origin: "git@github.com:owner/repo.git", issue: "7", kept: true,
			ponytail: "- a.txt: dropped two lines (speculative_feature)",
			log:      "ponytail: 1 cuts|" + dev + "\natm unit 1: Retry on timeout|" + dev},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, c.origin, atmSet(atmYAML, "delivery", deliveryLine))
			noIdentity(t)
			gitT(t, root, "config", "user.name", "Repo Dev")
			gitT(t, root, "config", "user.email", "dev@example.com")
			got := t.TempDir()
			t.Setenv("ATM_TEST_OUT", got)
			arg := c.issue
			if arg == "" {
				arg = issueFile(t, issue)
			} else {
				fakeGH(t, `{"title":"Retry on timeout","body":"Type: fix\nRetry once."}`, false)
			}
			if c.kept {
				t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt; echo 'grep -q fixed a.txt' > a_test.sh")
				t.Setenv("FAKE_PONYTAIL_WORK", "echo fixed > a.txt")
				t.Setenv("FAKE_PONYTAIL_REPORT", cut)
			}
			var out, summary bytes.Buffer
			if err := Run("t", []string{arg}, &out, &summary); err != nil {
				t.Fatalf("%v\n%s", err, summary.String())
			}
			read := reader(t, got)
			top := gitT(t, root, "rev-parse", "--show-toplevel")
			checkBranch(t, read, c.log, c.origin)
			if c.origin == bare {
				checkDeliveredMarker(t, read)
			}
			checkEnv(t, got, top, c.issue, c.ponytail)
			printed, _ := os.ReadFile(filepath.Join(top, ".atm", "runs", "t", "delivery-output.txt"))
			for _, line := range []string{"out-line", "err-line"} {
				if !strings.Contains(summary.String(), line) || !strings.Contains(string(printed), line) {
					t.Fatalf("want %q on the screen and in delivery-output.txt, got %q and %q", line, summary.String(),
						printed)
				}
			}
			if end := events(t, &out); end[len(end)-1]["step"] != "delivery" || end[len(end)-1]["state"] != "passed" {
				t.Fatalf("want delivery passed last, got %v", end[len(end)-1])
			}
			if c.origin == bare {
				if refs := gitT(t, bare, "for-each-ref"); refs != "" {
					t.Fatalf("ATM pushed: %s", refs)
				}
			}
		})
	}
}

// A commit by anyone but the repository identity, made by a command the run runs, never reaches delivery: the
// run fails at the node where it appeared.
func TestRunFailsOnAForeignCommit(t *testing.T) {
	const commit = "git -c user.name=Agent -c user.email=agent@localhost commit -q"
	suite := func(lines ...string) string {
		return "; printf '%s\\n' '" + strings.Join(lines, "' '") + "' > suite.sh"
	}
	committing := "sh a_test.sh && git add -A && " + commit + " -m indirect-agent-commit"
	cases := []struct{ node, work, ponytail string }{
		{node: "checks", work: fixWork + suite(committing)},
		{node: "ponytail", work: fixWork + suite("sh a_test.sh", "# padding", "# padding"), ponytail: suite(committing)},
		{node: "delivery", work: fixWork + suite("sh a_test.sh") + "; hook=.git/hooks/post-checkout; printf '%s\\n' " +
			"'[ \"$3\" = 1 ] || exit 0' '" + commit + " --allow-empty -m hook-commit' > $hook; chmod +x $hook"},
	}
	for _, c := range cases {
		t.Run(c.node, func(t *testing.T) {
			root := repo(t, t.TempDir(), atmSet(atmSet(atmYAML, "delivery", deliveryLine), "test", "sh suite.sh"))
			noIdentity(t)
			gitT(t, root, "config", "user.name", "Repo Identity")
			gitT(t, root, "config", "user.email", "repo@example.test")
			got := t.TempDir()
			t.Setenv("ATM_TEST_OUT", got)
			t.Setenv("FAKE_AGENT_WORK", c.work)
			if c.ponytail != "" {
				t.Setenv("FAKE_PONYTAIL_WORK", strings.TrimPrefix(c.ponytail, "; "))
				t.Setenv("FAKE_PONYTAIL_REPORT", `{"findings": [{"file": "suite.sh", "family": "speculative_feature", `+
					`"finding": "dropped the padding"}], "summary": "s"}`)
			}
			err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
			rep := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
			if err == nil || rep["failed_node"] != c.node {
				t.Fatalf("want the run failed at %s, got %v at %v", c.node, err, rep["failed_node"])
			}
			if _, err := os.Stat(filepath.Join(got, "branch")); err == nil {
				t.Fatal("the delivery command ran on a foreign commit")
			}
		})
	}
}

func checkDeliveredMarker(t *testing.T, read func(string) string) {
	t.Helper()
	marker, err := os.ReadFile(read("ATM_CLONE") + ".delivered")
	if err != nil || string(marker) != read("ATM_BRANCH")+" main\n" {
		t.Fatalf("want delivered marker for branch and base, got %q, %v", marker, err)
	}
}

// reader reads what the delivery command wrote to got.
func reader(t *testing.T, got string) func(string) string {
	return func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(got, name))
		if err != nil {
			t.Fatalf("the delivery command did not run: %v", err)
		}
		return strings.TrimRight(string(b), "\n")
	}
}

// checkBranch checks the delivery command ran on atm/<slug>-<timestamp>, everything committed, its last two
// commits log, its origin origin.
func checkBranch(t *testing.T, read func(string) string, log, origin string) {
	t.Helper()
	branch := read("branch")
	if !regexp.MustCompile(`^atm/retry-on-timeout-\d{8}-\d{6}$`).MatchString(branch) || read("ATM_BRANCH") != branch {
		t.Fatalf("want the clone on atm/<slug>-<timestamp>, named in ATM_BRANCH, got %q, %q", branch,
			read("ATM_BRANCH"))
	}
	if s := read("status"); s != "" {
		t.Fatalf("want every change committed, got %q", s)
	}
	if l := read("log"); l != log {
		t.Fatalf("want the work committed under the repository's identity:\n%s\ngot:\n%s", log, l)
	}
	if o := read("origin"); o != origin {
		t.Fatalf("want the clone's origin %s, got %s", origin, o)
	}
}

// checkEnv checks the ATM_ variables the delivery command wrote to got, of a run in top, and that report.json,
// as it saw it and at the end, carries the final SHA.
func checkEnv(t *testing.T, got, top, issue, ponytail string) {
	t.Helper()
	read := reader(t, got)
	report := filepath.Join(top, ".atm", "runs", "t", "report.json")
	for k, want := range map[string]string{"ATM_TITLE": "Retry on timeout", "ATM_ISSUE": issue,
		"ATM_REPORT": report, "ATM_PONYTAIL": ponytail} {
		if v := read(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	if clone := read("ATM_CLONE"); filepath.Dir(clone) != filepath.Join(top, ".atm", "clones") ||
		!strings.HasPrefix(filepath.Base(clone), "atm-run-") {
		t.Errorf("ATM_CLONE = %q, want the run's clone", clone)
	}
	head := read("head")
	for _, rep := range []obj{readReport(t, filepath.Join(got, "report.json")), readReport(t, report)} {
		if rep["head_sha"] != head {
			t.Fatalf("want report.json with the final SHA %s, got %v", head, rep["head_sha"])
		}
	}
}

func TestRunDeliveryNeedsAnOrigin(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery", deliveryLine))
	gitT(t, root, "remote", "remove", "origin")
	got := t.TempDir()
	t.Setenv("ATM_TEST_OUT", got)
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, issue)}, &out, &bytes.Buffer{})
	if ExitCode(err) != 4 || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("want exit 4 naming origin, got %d: %v", ExitCode(err), err)
	}
	if rep := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json")); rep["failed_node"] != "delivery" {
		t.Fatalf("want delivery failed, got %v", rep["failed_node"])
	}
	if _, err := os.Stat(filepath.Join(got, "branch")); err == nil {
		t.Fatal("the delivery command ran without an origin")
	}
}
