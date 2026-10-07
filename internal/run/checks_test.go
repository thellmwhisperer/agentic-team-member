package run

import (
	"bytes"
	"cmp"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// atmSet is atm with key set to the shell line value.
func atmSet(atm, key, value string) string {
	return regexp.MustCompile(`(?m)^`+key+`:.*$`).ReplaceAllLiteralString(atm, key+": '"+value+"'")
}

// checksStep is the checks step's end event, after checking the agent step passed and nothing followed a failure.
func checksStep(t *testing.T, out *bytes.Buffer) obj {
	t.Helper()
	evs := events(t, out)
	if len(evs) < 10 || evs[9]["state"] == "failed" && len(evs) != 10 || evs[7]["step"] != "agent" ||
		evs[7]["state"] != "passed" || evs[8]["step"] != "checks" ||
		evs[8]["state"] != "started" || evs[9]["step"] != "checks" {
		t.Fatalf("want the agent step passed, then the checks step, got %v", evs)
	}
	return evs[9]
}

// newFile makes b.txt and tests only it.
const newFile = "echo fixed > b.txt; echo 'grep -q fixed b.txt' > a_test.sh"

// oneLine and multiLine commit a lib.sh whose value is broken, on one line or over a block; testValue tests value.
const (
	oneLine   = "echo 'value() { echo broken; }' > lib.sh; git add lib.sh; git commit -qm lib"
	multiLine = `printf 'value() {\n  v=broken\n  echo $v\n}\n' > lib.sh; git add lib.sh; git commit -qm lib`
	testValue = `; echo '. ./lib.sh; test "$(value)" = fixed' > a_test.sh`
)

// proofCases are, by task type, a line committed on the base, the agent's work, and what the run's failure
// names, "" for a run that passes.
var proofCases = []struct {
	name, typ, base, work, atm, why string
	hang                            bool
}{
	{name: "fix whose test only exercises a function it adds", typ: "fix", base: oneLine,
		work: `printf 'value() { echo changed; }\nhelper() { echo fixed; }\n' > lib.sh; echo '. ./lib.sh; test "$(helper)" = fixed' > a_test.sh`,
		why:  "behaviour the base had"},
	{name: "fix that changes a line", typ: "fix", base: oneLine,
		work: "echo 'value() { echo fixed; }' > lib.sh" + testValue},
	{name: "fix that adds a line inside a function", typ: "fix", base: multiLine,
		work: `printf 'value() {\n  v=broken\n  v=fixed\n  echo $v\n}\n' > lib.sh` + testValue},
	{name: "fix", typ: "fix"},
	{name: "fix that adds a file", typ: "fix",
		work: `echo fixed > conf.txt; echo conf.txt > a.txt; echo 'grep -q fixed "$(cat a.txt)"' > a_test.sh`},
	{name: "fix whose test only exercises a new file", typ: "fix", work: newFile, why: "passes without the fix"},
	{name: "fix whose test fails with it", typ: "fix", work: "echo fixed > a.txt; echo false > a_test.sh",
		why: "fails with the fix"},
	{name: "fix whose test hangs without it", typ: "fix", why: "timed out", hang: true,
		work: "echo fixed > a.txt; echo 'grep -q fixed a.txt || while :; do :; done' > a_test.sh"},
	{name: "fix whose test changes the clone", typ: "fix",
		work: "echo fixed > a.txt; echo 'grep -q fixed a.txt && echo x >> a.txt' > a_test.sh", why: "void"},
	{name: "fix without test_file", typ: "fix", atm: atmSet(atmYAML, "test_file", ""), why: "test_file"},
	{name: "feature red on a missing file", typ: "feature", work: newFile},
	{name: "greenfield red on a missing file", typ: "greenfield", work: newFile},
	{name: "agent commits", typ: "fix", work: fixWork + "; git add -A; git commit -qm fix", why: "commits"},
	{name: "refactor", typ: "refactor"},
	{name: "refactor that touches a test", typ: "refactor", work: "echo true > a_test.sh", why: "a_test.sh"},
	{name: "refactor on a red suite", typ: "refactor", atm: atmSet(atmYAML, "test", "test -f b.txt"),
		why: "fails on the base"},
	{name: "tests", typ: "tests"},
	{name: "tests that touch source", typ: "tests", work: "echo b > b.txt; echo 'test -f b.txt' > b_test.sh",
		why: "b.txt"},
	{name: "tests failing on the base", typ: "tests", work: "echo false > a_test.sh", why: "fails on the base"},
	{name: "docs", typ: "docs"},
	{name: "docs that touch code", typ: "docs", work: "echo doc > README.md; echo fixed > a.txt", why: "a.txt"},
	{name: "chore", typ: "chore"},
}

func TestRunChecksTheProofOfEachTaskType(t *testing.T) {
	for _, c := range proofCases {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", cmp.Or(c.atm, atmYAML))
			if b, err := exec.Command("sh", "-c", c.base).CombinedOutput(); err != nil {
				t.Fatalf("base: %v\n%s", err, b)
			}
			if c.work != "" {
				t.Setenv("FAKE_AGENT_WORK", c.work)
			}
			if c.hang {
				defer func(d time.Duration) { timeout = d }(timeout)
				timeout = 2 * time.Second
			}
			var out bytes.Buffer
			err := Run("t", []string{issueFile(t, "# Retry\nType: "+c.typ+"\nRetry once.\n")}, &out, io.Discard)
			end := checksStep(t, &out)
			switch {
			case c.why == "" && (err != nil || end["state"] != "passed"):
				t.Fatalf("want the checks passed, got %v, %v", err, end)
			case c.why != "" && (err == nil || !strings.Contains(err.Error(), c.why)):
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			case c.why != "" && (end["state"] != "failed" || end["error"] != err.Error()):
				t.Fatalf("end event: %v", end)
			}
			if left := clones(t, root); len(left) != 0 {
				t.Fatalf("the run's clone outlived it: %v", left)
			}
		})
	}
}

func TestRunChecksRunTheCommandsInOrderAndDieOnTheFirstFailure(t *testing.T) {
	got := filepath.Join(t.TempDir(), "ran")
	t.Setenv("ATM_TEST_OUT", got)
	atm := atmYAML
	for _, k := range []string{"install", "test", "typecheck", "lint"} {
		atm = atmSet(atm, k, `echo `+k+` >> "$ATM_TEST_OUT"`)
	}
	atm = atmSet(atm, "typecheck", `echo typecheck >> "$ATM_TEST_OUT"; seq 100; exit 3`)
	repo(t, "https://example.com/owner/repo.git", atm)
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
	var tail []string
	for n := 41; n <= 100; n++ {
		tail = append(tail, strconv.Itoa(n))
	}
	if err == nil || !strings.HasPrefix(err.Error(), "typecheck: ") ||
		!strings.HasSuffix(err.Error(), "exit status 3\n"+strings.Join(tail, "\n")) {
		t.Fatalf("want typecheck's failure with its last 60 lines, got %v", err)
	}
	if end := checksStep(t, &out); end["state"] != "failed" || end["error"] != err.Error() {
		t.Fatalf("end event: %v", end)
	}
	// Install runs in the clone step, then again in the checks: the agent may have changed dependencies.
	if b, _ := os.ReadFile(got); string(b) != "install\ninstall\ntest\ntypecheck\n" {
		t.Fatalf("ran %q", b)
	}
}
