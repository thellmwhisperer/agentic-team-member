package run

import (
	"bytes"
	"cmp"
	"io"
	"os"
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

// lib is lib.sh on the base, value broken, and helper the line that adds a function nothing on the base calls.
const lib, helper = "# lib\nvalue() { echo broken; }\n", "helper() { echo fixed; }"

// testThrough writes a_test.sh, which calls fn from lib.sh and wants fixed.
func testThrough(fn string) string {
	return `; echo '. ./lib.sh; test "$(` + fn + `)" = fixed' > a_test.sh`
}

// commitBase commits name, holding text, on root's base, when text is not "".
func commitBase(t *testing.T, root, name, text string) {
	t.Helper()
	if text == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "add", name)
	gitT(t, root, "commit", "-qm", name)
}

// proofCases are the task types' proofs at work: why, when not "", names the run's failure.
var proofCases = []struct {
	name, typ, work, atm, why, lib, ignore string
	hang                                   bool
}{
	{name: "fix whose test only calls a function it adds to an old file", typ: "fix", lib: lib,
		work: "echo '" + helper + "' >> lib.sh" + testThrough("helper"), why: "depends on nothing the base had"},
	{name: "fix whose test only calls a function it adds next to a changed line", typ: "fix", lib: lib,
		work: "printf '# lib, v2\\n" + helper + "\\nvalue() { echo broken; }\\n' > lib.sh" + testThrough("helper"),
		why:  "depends on nothing the base had"},
	{name: "fix that changes a line", typ: "fix", lib: lib,
		work: "printf '# lib\\nvalue() { echo fixed; }\\n' > lib.sh" + testThrough("value")},
	{name: "fix that adds a line inside a function", typ: "fix", lib: "value() {\n  v=broken\n  echo $v\n}\n",
		work: "printf 'value() {\\n  v=broken\\n  v=fixed\\n  echo $v\\n}\\n' > lib.sh" + testThrough("value")},
	{name: "fix", typ: "fix"},
	{name: "fix that adds a file", typ: "fix",
		work: `echo fixed > conf.txt; echo conf.txt > a.txt; echo 'grep -q fixed "$(cat a.txt)"' > a_test.sh`},
	{name: "fix whose test only exercises a new file", typ: "fix", work: newFile, why: "passes without the fix"},
	{name: "fix whose test fails with it", typ: "fix", work: "echo fixed > a.txt; echo false > a_test.sh",
		why: "fails with the fix"},
	{name: "fix whose test hangs without it", typ: "fix", why: "timed out", hang: true,
		work: "echo fixed > a.txt; echo 'grep -q fixed a.txt || while :; do :; done' > a_test.sh"},
	{name: "fix-less test that passes on its second run", typ: "fix", ignore: ".atm/\n",
		why: "regression_test.sh fails with the fix",
		work: "echo 'if test -f .atm/seen; then exit 0; fi; touch .atm/seen; exit 1' > regression_test.sh; " +
			reportJSON("regression_test.sh")},
	{name: "fix whose test writes ignored output", typ: "fix", ignore: "build/\n",
		work: `echo fixed > a.txt; echo 'mkdir -p build; cp a.txt build; test "$(cat build/a.txt)" = fixed' > a_test.sh; ` +
			reportJSON("a_test.sh")},
	{name: "fix whose test changes an ignored file", typ: "fix", ignore: "cache.txt\n", why: "void",
		work: "echo fixed > a.txt; echo kept > cache.txt; " +
			"echo 'grep -q fixed a.txt && ! grep -q broken a.txt && echo x >> cache.txt' > a_test.sh; " +
			reportJSON("a_test.sh")},
	{name: "fix whose test deletes an ignored file", typ: "fix", ignore: "cache.txt\n", why: "void",
		work: "echo fixed > a.txt; echo kept > cache.txt; " +
			"echo 'grep -q fixed a.txt && ! grep -q broken a.txt && rm cache.txt' > a_test.sh; " +
			reportJSON("a_test.sh")},
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
	{name: "fix that deletes a test", typ: "fix", atm: atmSet(atmYAML, "test", suite),
		work: "echo fixed > a.txt; rm a_test.sh" + reportTest("regression_test.sh"), why: shrunkTest},
	{name: "feature that shortens a test", typ: "feature", why: shrunkTest,
		work: "echo fixed > a.txt; echo true > a_test.sh" + reportTest("b_test.sh")},
	{name: "tests that shorten a test", typ: "tests", why: shrunkTest,
		work: "echo true > a_test.sh; echo 'grep -q broken a.txt' > b_test.sh; " + reportJSON("b_test.sh")},
	{name: "chore that deletes a test", typ: "chore", atm: atmSet(atmYAML, "test", "true"), work: "rm a_test.sh",
		why: shrunkTest},
	{name: "fix that adds a line to a test", typ: "fix",
		work: "echo fixed > a.txt; echo '! grep -q broken a.txt' >> a_test.sh"},
}

// shrunkTest names the failure of work that deletes or shortens a_test.sh, a test on the base.
const shrunkTest = "the base's test a_test.sh is gone or shorter"

// suite runs every *_test.sh.
const suite = "for f in *_test.sh; do sh $f || exit 1; done"

// reportTest writes test, which wants a.txt fixed and broken no longer, and reports it as the test.
func reportTest(test string) string {
	return "; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > " + test + "; " + reportJSON(test)
}

// reportJSON is the shell line that leaves the fake agent's report, naming test.
func reportJSON(test string) string {
	return `mkdir -p .atm; printf '{"test_file":"` + test + `","follow_ups":[]}' > .atm/fake-report.json`
}

func TestRunChecksTheProofOfEachTaskType(t *testing.T) {
	for _, c := range proofCases {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", cmp.Or(c.atm, atmYAML))
			commitBase(t, root, "lib.sh", c.lib)
			commitBase(t, root, ".gitignore", c.ignore)
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
