package run

import (
	"bytes"
	"cmp"
	"debug/macho"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// atmSet is atm with key set to the shell line value.
func atmSet(atm, key, value string) string {
	return regexp.MustCompile(`(?m)^`+key+`:.*$`).ReplaceAllLiteralString(atm, key+": '"+value+"'")
}

func TestChecksRestoresItsStash(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	runDir := checksStashRunDir(t, root)
	dir, sha := checksStashClone(t, root)
	t.Cleanup(func() {
		if err := release(dir, ""); err != nil {
			t.Error(err)
		}
	})
	artifact := checksStashArtifact(t, dir)
	ready := filepath.Join(runDir, "continue-checks")
	t.Setenv("ATM_TEST_READY", ready)
	if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(atmSet(atmYAML,
		"test", `while [ ! -f "$ATM_TEST_READY" ]; do sleep 0.01; done; test ! -e .atm`)), 0o600); err != nil {
		t.Fatal(err)
	}
	c := checksStashConfig(t, root)
	done := make(chan error, 1)
	go func() {
		_, err := checks(dir, runDir, sha, "", nil, false, c)
		done <- err
	}()
	defer func() {
		if err := os.WriteFile(ready, nil, 0o600); err != nil {
			t.Error(err)
		}
	}()
	hidden := waitForChecksStashHidden(t, dir)
	_, stashErr := os.Stat(filepath.Join(runDir, "checks-atm"))
	if err := os.WriteFile(ready, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	checksErr := <-done
	if !hidden {
		t.Fatal("Checks did not hide .atm")
	}
	if stashErr != nil {
		t.Fatalf("Checks stash missing: %v", stashErr)
	}
	if checksErr != nil {
		t.Fatal(checksErr)
	}
	if b, err := os.ReadFile(artifact); err != nil || string(b) != "follow-up\n" {
		t.Fatalf("follow-up was not restored: %q, %v", b, err)
	}
}

func TestMissingReportKeepsAvailableProofs(t *testing.T) {
	for _, tc := range []struct {
		name, typ, file, content, suite, want string
	}{
		{"refactor test change accepted", "refactor", "a_test.sh", "echo changed\n", "", ""},
		{"refactor base suite", "refactor", "a.txt", "changed\n", "false", "test:"},
		{"tests source restriction", "tests", "a.txt", "changed\n", "", ""},
		{"docs restriction", "docs", "a.txt", "changed\n", "", ""},
		{"fix without a change", "fix", "", "", "", "no change to prove"},
		{"tests without a change", "tests", "", "", "", "no change to prove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			dir, sha := checksStashClone(t, root)
			t.Cleanup(func() { _ = release(dir, "") })
			if err := before(dir); err != nil {
				t.Fatal(err)
			}
			if tc.file != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			c := checksStashConfig(t, root)
			if tc.suite != "" {
				c.Test = tc.suite
			}
			_, err := checks(dir, checksStashRunDir(t, root), sha, "", nil, true, c)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("missing report bypassed %s: %v", tc.name, err)
			}
		})
	}
}

func checksStashRunDir(t *testing.T, root string) string {
	t.Helper()
	runDir := filepath.Join(root, ".atm", "runs", "t")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func checksStashClone(t *testing.T, root string) (string, string) {
	t.Helper()
	dir, sha, _, err := clone(root, "main", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return dir, sha
}

func checksStashArtifact(t *testing.T, dir string) string {
	t.Helper()
	artifact := filepath.Join(dir, ".atm", "follow-ups", "1", "artifact.txt")
	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("follow-up\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := before(dir); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func checksStashConfig(t *testing.T, root string) config.Config {
	t.Helper()
	c, err := config.Load(root, os.Getenv("HOME"), config.Agent{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func waitForChecksStashHidden(t *testing.T, dir string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".atm")); errors.Is(err, os.ErrNotExist) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
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

const jestZeroTestOutput = " FAIL  ./index.test.js\n      ● Test suite failed to run\n\n" +
	"        SyntaxError: Unexpected token\n      Tests:       0 total"
const jestAssertionOutput = " FAIL  ./index.test.js\n  ● Test suite failed to run\n\n" +
	"    expect(received).toBe(expected)\n    Expected: 1\n    Received: 0\n\n" +
	"Test Suites: 1 failed, 1 total\nTests: 1 failed, 1 total"
const nodeSyntaxOutput = "/tmp/a.test.js:3\n\nSyntaxError: Unexpected end of input\n" +
	"    at wrapSafe (node:internal/modules/cjs/loader:1637:18)\nNode.js v22.22.1"
const nodeAssertionOutput = "TAP version 13\n# SyntaxError: ordinary assertion output\n" +
	"# Subtest: fails after logging\nnot ok 1 - fails after logging\n  ---\n" +
	"  failureType: 'testCodeFailure'\n  error: |\n    Expected values to be strictly equal\n" +
	"  code: 'ERR_ASSERTION'\n  name: 'AssertionError'\n  ...\n1..1\n# tests 1\n# pass 0\n# fail 1"
const vitestLoadOutput = " RUN  v3.2.4 /workspace\n\n" +
	"Error: Failed to load url ../steps (resolved id: ../steps) in /workspace/a.test.js. Does the file exist?\n\n" +
	" Test Files  1 failed (1)\n      Tests  no tests\n   Start at 12:03:18\n   Duration 4ms"
const vitestAssertionOutput = " RUN  v3.2.4 /workspace\n\n FAIL a.test.js > loads\n" +
	"Error: Failed to load url ../steps was expected text\n\n Test Files 1 failed (1)\n Tests 1 failed (1)"
const vitestLoggedNoTestsOutput = " RUN  v3.2.4 /workspace\n\n FAIL a.test.js > loads\n" +
	"Error: Failed to load url ../steps\nTests no tests\n\n Test Files 1 failed (1)\n Tests 1 failed (1)"

// fixtureWork fixes a.txt and tests it with fixture.txt, which it makes.
const fixtureWork = "echo fixed > a.txt; echo ok > fixture.txt" +
	"; echo 'grep -q fixed a.txt && ! grep -q broken a.txt && grep -q ok fixture.txt' > a_test.sh"

// proofCases are the task types' proofs at work: why, when not "", names the run's failure; files are on the base.
var proofCases = []struct {
	name, typ, work, atm, why, lib, ignore, hooks string // hooks: hooks_test.sh on the base
	hang, binary                                  bool   // binary: $ATM_TEST_BINARY is executedBinary
	files                                         map[string]string
}{
	{name: "Go fix whose test does not compile without it", typ: "fix", atm: goAtm, files: goBase,
		work: `printf 'package p\n\nfunc Value() string { return "broken" }\n\nfunc Fixed() string { return "fixed" }\n'` +
			" > p/p.go; " + goTest("Fixed"),
		why: ""},
	{name: "Go fix whose test fails on an assertion without it", typ: "fix", atm: goAtm, files: goBase,
		work: `printf 'package p\n\nfunc Value() string { return "fixed" }\n' > p/p.go; ` + goTest("Value")},
	{name: "Go fix whose assertion mentions a build diagnostic", typ: "fix", atm: goAtm, files: goBase,
		work: `printf 'package p\n\nfunc Value() string { return "fixed" }\n' > p/p.go; ` +
			"printf 'package p\\n\\nimport \"testing\"\\n\\nfunc TestFix(t *testing.T) {\\n" +
			"\\tif Value() != \"fixed\" { t.Fatal(\"expected [build failed]\") }\\n}\\n' > p/p_test.go; " +
			reportJSON("p/p_test.go")},
	{name: "Go feature whose red test does not compile", typ: "feature", atm: goAtm, files: goBase,
		work: `printf 'package p\n\nfunc TestFix(t *testing.T) {\n' > p/p_test.go; ` + reportJSON("p/p_test.go"),
		why:  ""},
	{name: "Go feature whose red test has no package clause", typ: "feature", atm: goAtm, files: goBase,
		work: noPackage, why: ""},
	{name: "Go greenfield whose red test has no package clause", typ: "greenfield", atm: goAtm, files: goBase,
		work: noPackage, why: ""},
	{name: "Go fix whose red test has no package clause", typ: "fix", atm: goAtm, files: goBase,
		work: noPackage, why: ""},
	{name: "fix whose malformed Python test fails in red and green", typ: "fix",
		atm: atmSet(atmYAML, "test_file", "sh pytest.sh {file}"), files: map[string]string{"pytest.sh": fakePytest},
		work: "echo fixed > a.txt; printf 'def test_broken(:\\n    assert True\\n' > test_bad.py; " +
			reportJSON("test_bad.py"),
		why: ""},
	{name: "Jest suite parse diagnostic rejected at red green boundary", typ: "fix",
		atm:   atmSet(atmYAML, "test_file", "sh runner.sh {file}"),
		files: map[string]string{"runner.sh": recordedRunner(jestZeroTestOutput)},
		work:  "echo fixed > a.txt; echo test > index.test.js; " + reportJSON("index.test.js"),
		why:   ""},
	{name: "Jest assertion with suite diagnostic title remains a valid red", typ: "feature",
		atm:   atmSet(atmYAML, "test_file", "sh runner.sh {file}"),
		files: map[string]string{"runner.sh": recordedRunnerWhenFixed(jestAssertionOutput)},
		work:  "echo fixed > a.txt; echo test > index.test.js; " + reportJSON("index.test.js")},
	{name: "node syntax diagnostic rejected at red green boundary", typ: "fix",
		atm:   atmSet(atmYAML, "test_file", "sh runner.sh {file}"),
		files: map[string]string{"runner.sh": recordedRunner(nodeSyntaxOutput)},
		work:  "echo fixed > a.txt; echo test > a.test.js; " + reportJSON("a.test.js"),
		why:   ""},
	{name: "node TAP assertion output with SyntaxError remains a valid red", typ: "feature",
		atm:   atmSet(atmYAML, "test_file", "sh runner.sh {file}"),
		files: map[string]string{"runner.sh": recordedRunnerWhenFixed(nodeAssertionOutput)},
		work:  "echo fixed > a.txt; echo test > a.test.js; " + reportJSON("a.test.js")},
	{name: "tsc diagnostic rejected at red green boundary", typ: "fix",
		atm: atmSet(atmYAML, "test_file", "sh runner.sh {file}"), files: map[string]string{"runner.sh": recordedRunner(
			"src/a.test.ts(3,7): error TS2322: Type 'number' is not assignable to type 'string'.")},
		work: "echo fixed > a.txt; echo test > a.test.ts; " + reportJSON("a.test.ts"),
		why:  ""},
	{name: "Vitest load failure with no tests rejected at red green boundary", typ: "feature",
		atm: atmSet(atmYAML, "test_file", "sh runner.sh {file}"), files: map[string]string{"runner.sh": recordedRunner(
			vitestLoadOutput)},
		work: "echo fixed > a.txt; echo test > a.test.js; " + reportJSON("a.test.js"),
		why:  ""},
	{name: "Vitest assertion output with load wording remains a valid red", typ: "feature",
		atm:   atmSet(atmYAML, "test_file", "sh runner.sh {file}"),
		files: map[string]string{"runner.sh": recordedRunnerWhenFixed(vitestAssertionOutput)},
		work:  "echo fixed > a.txt; echo test > a.test.js; " + reportJSON("a.test.js")},
	{name: "Vitest logged zero-test wording before a failed assertion", typ: "feature",
		atm:   atmSet(atmYAML, "test_file", "sh runner.sh {file}"),
		files: map[string]string{"runner.sh": recordedRunnerWhenFixed(vitestLoggedNoTestsOutput)},
		work:  "echo fixed > a.txt; echo test > a.test.js; " + reportJSON("a.test.js")},
	{name: "fix whose test only calls a function it adds to an old file", typ: "fix", lib: lib,
		work: "echo '" + helper + "' >> lib.sh" + testThrough("helper"), why: ""},
	{name: "fix whose test only calls a function it adds next to a changed line", typ: "fix", lib: lib,
		work: "printf '# lib, v2\\n" + helper + "\\nvalue() { echo broken; }\\n' > lib.sh" + testThrough("helper"),
		why:  ""},
	{name: "fix whose test depends on the base", typ: "fix", lib: lib,
		work: "printf '# lib\\nvalue() { echo fixed; }\\n' > lib.sh" + testThrough("value")},
	{name: "fix that adds a line inside a function", typ: "fix", lib: "value() {\n  v=broken\n  echo $v\n}\n",
		work: "printf 'value() {\\n  v=broken\\n  v=fixed\\n  echo $v\\n}\\n' > lib.sh" + testThrough("value")},
	{name: "fix", typ: "fix"},
	{name: "fix that adds a file", typ: "fix",
		work: `echo fixed > conf.txt; echo conf.txt > a.txt; echo 'grep -q fixed "$(cat a.txt)"' > a_test.sh`},
	{name: "fix whose test only exercises a new file", typ: "fix", work: newFile, why: ""},
	{name: "fix whose test fails with it", typ: "fix", work: "echo fixed > a.txt; echo false > a_test.sh",
		why: "test:"},
	{name: "fix whose test hangs without it", typ: "fix", why: "", hang: true,
		work: "echo fixed > a.txt; echo 'grep -q fixed a.txt || while :; do :; done' > a_test.sh"},
	{name: "fix-less test that passes on its second run", typ: "fix", ignore: ".atm/\n",
		why: "",
		work: "echo 'if test -f .atm/seen; then exit 0; fi; touch .atm/seen; exit 1' > regression_test.sh; " +
			reportJSON("regression_test.sh")},
	{name: "fix whose test writes ignored output", typ: "fix", ignore: "build/\n",
		work: `echo fixed > a.txt; echo 'mkdir -p build; cp a.txt build; test "$(cat build/a.txt)" = fixed' > a_test.sh; ` +
			reportJSON("a_test.sh")},
	{name: "fix whose test reads an ignored fixture its agent made", typ: "fix", ignore: "fixture.txt\n",
		work: fixtureWork, why: "test:"},
	{name: "fix whose test reads a fixture its agent made", typ: "fix", work: fixtureWork},
	{name: "fix whose test reads an ignored fixture in .atm", typ: "fix", ignore: ".atm/\n",
		work: `mkdir -p .atm; echo ok > .atm/fixture.txt; echo fixed > a.txt; ` +
			`echo 'grep -q fixed a.txt && grep -q ok .atm/fixture.txt' > a_test.sh; ` + reportJSON("a_test.sh"),
		why: "test:"},
	{name: "fix whose test reads an ignored follow-up fixture in .atm", typ: "fix", ignore: ".atm/\n",
		work: `mkdir -p .atm/follow-ups/1; echo ok > .atm/follow-ups/1/fixture.txt; echo fixed > a.txt; ` +
			`echo 'grep -q fixed a.txt && grep -q ok .atm/follow-ups/1/fixture.txt' > a_test.sh; ` +
			`printf '{"test_file":"a_test.sh","follow_ups":[{"title":"gap","red_test":"fixture.txt",` +
			`"criterion":"A retry runs once after a timeout."}]}' > .atm/fake-report.json`,
		why: "test:"},
	{name: "fix whose test changes an ignored file", typ: "fix", ignore: "cache.txt\n",
		work: "echo fixed > a.txt; echo kept > cache.txt; " +
			"echo 'grep -q fixed a.txt && ! grep -q broken a.txt && echo x >> cache.txt' > a_test.sh; " +
			reportJSON("a_test.sh")},
	{name: "fix whose test deletes an ignored file", typ: "fix", ignore: "cache.txt\n",
		atm: atmInstall("echo kept > cache.txt"), // there before the agent
		work: "echo fixed > a.txt; " +
			"echo 'grep -q fixed a.txt && ! grep -q broken a.txt && rm cache.txt' > a_test.sh; " +
			reportJSON("a_test.sh")},
	{name: "fix whose test passes on what its red left in an ignored file", typ: "fix", ignore: "cache.txt\n",
		why: "test:",
		work: "echo fixed > a.txt; echo 0 > cache.txt; " +
			"echo 'if grep -q 1 cache.txt; then exit 0; fi; echo 1 > cache.txt; exit 1' > a_test.sh; " +
			reportJSON("a_test.sh")},
	{name: "fix whose agent ran an ignored binary", typ: "fix", ignore: ".tmp/\n", binary: true,
		work: `mkdir .tmp; cp "$ATM_TEST_BINARY" .tmp/bin; .tmp/bin; echo fixed > a.txt; ` +
			"echo 'touch .tmp/bin; grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh"},
	{name: "fix whose test changes the clone", typ: "fix",
		work: "echo fixed > a.txt; echo 'grep -q fixed a.txt && echo x >> a.txt' > a_test.sh", why: ""},
	{name: "fix without test_file", typ: "fix", atm: atmSet(atmYAML, "test_file", ""), why: ""},
	{name: "feature red on a missing file", typ: "feature", work: newFile},
	{name: "greenfield red on a missing file", typ: "greenfield", work: newFile},
	{name: "agent commits", typ: "fix", work: fixWork + "; git add -A; git commit -qm fix", why: "commits"},
	{name: "refactor", typ: "refactor"},
	{name: "refactor that touches a test", typ: "refactor", work: "echo true > a_test.sh", why: ""},
	{name: "refactor on a red suite", typ: "refactor", atm: atmSet(atmYAML, "test", "test -f b.txt"),
		work: "echo b > b.txt",
		why:  ""},
	{name: "tests", typ: "tests"},
	{name: "tests that touch source", typ: "tests", work: "echo b > b.txt; echo 'test -f b.txt' > b_test.sh",
		why: ""},
	{name: "tests failing on the base", typ: "tests", work: "echo false > a_test.sh", why: "test:"},
	{name: "docs", typ: "docs"},
	{name: "docs that touch code", typ: "docs", work: "echo doc > README.md; echo fixed > a.txt", why: ""},
	{name: "chore", typ: "chore"},
	{name: "fix that deletes a test", typ: "fix", atm: atmSet(atmYAML, "test", suite),
		work: "echo fixed > a.txt; rm a_test.sh" + reportTest("regression_test.sh")},
	{name: "feature that shortens a test", typ: "feature",
		work: "echo fixed > a.txt; : > a_test.sh" + reportTest("b_test.sh")},
	{name: "tests that shorten a test", typ: "tests",
		work: ": > a_test.sh; echo 'grep -q broken a.txt' > b_test.sh; " + reportJSON("b_test.sh")},
	{name: "chore that deletes a test", typ: "chore", atm: atmSet(atmYAML, "test", "true"),
		work: "rm a_test.sh; " + reportJSON("")},
	{name: "fix that adds a line to a test", typ: "fix",
		work: "echo fixed > a.txt; echo '! grep -q broken a.txt' >> a_test.sh"},
	{name: "fix that shortens a string in a test", typ: "fix", hooks: hooks,
		work: fixWork + "; sed 's/\"atm unit 1: three\"/\"fix: three\"/' hooks_test.sh > h; mv h hooks_test.sh"},
	{name: "fix that removes a trailing blank test line", typ: "fix", hooks: hooks + "\n",
		work: fixWork + "; sed '$d' hooks_test.sh > h; mv h hooks_test.sh"},
	{name: "chore that moves a test", typ: "chore", hooks: hooks, work: moveHooks("")},
	{name: "chore that moves a test and drops a function", typ: "chore", hooks: hooks, work: moveHooks("/two/d")},
}

// executedBinary is a program that runs but whose code signature, like slopslint's, does not match a page it
// never runs: once it has run, macOS kills any process that maps that page. Only macOS does.
func executedBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	dir := t.TempDir()
	src, bin := filepath.Join(dir, "main.go"), filepath.Join(dir, "bin")
	if err := os.WriteFile(src, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	f, err := macho.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	dwarf := f.Segment("__DWARF") // signed, never loaded
	if err = f.Close(); err != nil || dwarf == nil {
		t.Fatalf("no __DWARF in %s: %v", bin, err)
	}
	b, err := os.OpenFile(bin, os.O_RDWR, 0)
	if err == nil {
		at, one := int64(dwarf.Offset+dwarf.Filesz/2), []byte{0}
		if _, err = b.ReadAt(one, at); err == nil {
			_, err = b.WriteAt([]byte{^one[0]}, at)
		}
		err = errors.Join(err, b.Close())
	}
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// goBase is a Go module whose p.Value is broken, goAtm runs a Go test file alone.
var goBase, goAtm = map[string]string{"go.mod": "module example.com/m\n\ngo 1.21\n",
	"p/p.go": "package p\n\nfunc Value() string { return \"broken\" }\n"},
	atmSet(atmYAML, "test_file", "go test -vet=off -count=1 -run {tests} {dir}")

// goTest writes p/p_test.go, which wants fn to return fixed, and reports it as the test.
func goTest(fn string) string {
	return `printf 'package p\n\nimport "testing"\n\nfunc TestFix(t *testing.T) {\n\tif ` + fn +
		`() != "fixed" {\n\t\tt.Fatal()\n\t}\n}\n' > p/p_test.go; ` + reportJSON("p/p_test.go")
}

// noPackage writes p/p_test.go without its package clause and reports it as the test.
const noPackage = `printf 'import "testing"\n\nfunc TestFix(t *testing.T) {}\n' > p/p_test.go; ` + "mkdir -p .atm; " +
	`printf '{"test_file":"p/p_test.go","follow_ups":[]}' > .atm/fake-report.json`

// pytestSyntaxError is pytest 9.0.2's output, recorded, on a test file that does not parse.
var pytestSyntaxError = strings.Join([]string{
	"============================= test session starts ==============================",
	"platform darwin -- Python 3.14.7, pytest-9.0.2, pluggy-1.6.0",
	"collected 0 items / 1 error", "",
	"==================================== ERRORS ====================================",
	"_________________________ ERROR collecting test_bad.py _________________________",
	"/opt/homebrew/Cellar/python@3.14/3.14.7/lib/python3.14/ast.py:46: in parse",
	"    return compile(source, filename, mode, flags,",
	`E     File "/tmp/test_bad.py", line 1`, "E       def test_broken(:", "E                       ^",
	"E   SyntaxError: invalid syntax",
	"=========================== short test summary info ============================",
	"ERROR test_bad.py",
	"!!!!!!!!!!!!!!!!!!!! Interrupted: 1 error during collection !!!!!!!!!!!!!!!!!!!!",
	"=============================== 1 error in 0.17s ===============================",
}, "\n")

// fakePytest plays pytest on a test file that does not parse, red or green.
var fakePytest = "cat <<'EOF'\n" + pytestSyntaxError + "\nEOF\nexit 2\n"

func recordedRunner(output string) string {
	return "cat <<'EOF'\n" + output + "\nEOF\nexit 1\n"
}

func recordedRunnerWhenFixed(output string) string {
	return "if grep -q fixed a.txt; then exit 0; fi\n" + recordedRunner(output)
}

// hooks is a test on the base, as a Go test file would be: a package line, then its functions.
const hooks = "# package cli\none() { true; }\ntwo() { true; }\n" +
	"three() { test \"atm unit 1: three\" = \"atm unit 1: three\"; }\n"

// moveHooks moves hooks_test.sh to sub/, changing its package line and, with edit, more.
func moveHooks(edit string) string {
	return "mkdir sub; sed -e 's/package cli/package sub/' -e '" + edit + "' hooks_test.sh > sub/hooks_test.sh; " +
		"rm hooks_test.sh"
}

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

// keepGoCache keeps Go's build cache where it is once repo moves HOME.
func keepGoCache(t *testing.T) {
	t.Helper()
	gocache, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCACHE", strings.TrimSpace(string(gocache)))
}

func TestRedDidNotRunMatchesRunnerOutput(t *testing.T) {
	for _, c := range redDidNotRunCases() {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "local", atmSet(atmYAML, "test", "sh runner.sh"))
			commitBase(t, root, "runner.sh", recordedRunner(c.output))
			var out bytes.Buffer
			err := Run("t", []string{issueFile(t, issue)}, &out, io.Discard)
			if err == nil || !strings.HasPrefix(err.Error(), "test:") ||
				!strings.Contains(err.Error(), strings.TrimSpace(c.output)) {
				t.Fatalf("suite diagnostic must remain an ordinary command failure: %v", err)
			}
		})
	}
}

func redDidNotRunCases() []struct {
	name, output string
	want         bool
} {
	return append(redDidNotRunBaseCases(), redDidNotRunNodeCases()...)
}

func redDidNotRunBaseCases() []struct {
	name, output string
	want         bool
} {
	return []struct {
		name, output string
		want         bool
	}{
		{"pytest collection", strings.Join([]string{
			"_________________________ ERROR collecting test_bad.py _________________________",
			"E     File \"test_bad.py\", line 1", "E       def test_broken(:", "E                       ^",
			"E   SyntaxError: invalid syntax",
		}, "\n"), true},
		{"pytest import", "E   ImportError: cannot import name 'missing' from 'sample'", true},
		{"pytest runtime syntax error", "E   SyntaxError: raised by test body\nFAILED tests/test_bad.py::test_parse", false},
		{"Jest setup", strings.Join([]string{
			" FAIL  ./index.test.js", "      ● Test suite failed to run", "",
			"        SecurityError: Cannot initialize local storage without a `--localstorage-file` path", "",
			"      Test Suites: 1 failed, 1 total", "      Tests:       0 total", "      Snapshots:   0 total",
			"      Time:        0.029 s", "      Ran all test suites.",
		}, "\n"), true},
		{"tsc diagnostic", "index.ts:4:14 - error TS2322: Type '{ name: string; type: string; }' " +
			"is not assignable to type 'Schema'.\n  Types of property 'type' are incompatible.", true},
		{"Go build", "FAIL example.com/m/p [build failed]", true},
		{"Go setup", "FAIL example.com/m/p [setup failed]", true},
		{"Go assertion", "--- FAIL: TestFix (0.00s)\nchecks_test.go:1: expected [build failed]\n" +
			"FAIL example.com/m/p 0.003s", false},
		{"Go no package clause", "# example.com/m/p\np/p_test.go:1:1: expected 'package', found 'import'\n" +
			"FAIL\texample.com/m/p [setup failed]\nFAIL", true},
		{"pytest recorded", pytestSyntaxError, true},
		{"tsc", "src/a.test.ts(3,7): error TS2322: Type 'number' is not assignable to type 'string'.", true},
		{"Jest assertion text alone", "    Test suite failed to run", false},
		{"Vitest load diagnostic with zero-test summary", strings.Join([]string{
			" RUN  v3.2.4 /workspace", "", "Error: Failed to load url ../steps (resolved id: ../steps) in /workspace/a.test.js.",
			" Test Files  1 failed (1)", "      Tests  no tests",
		}, "\n"), true},
		{"Vitest assertion contains load wording", strings.Join([]string{
			" RUN  v3.2.4 /workspace", "", " FAIL  a.test.js > loads",
			"Error: Failed to load url ../steps was expected text", " Test Files  1 failed (1)", "      Tests  1 failed (1)",
		}, "\n"), false},
		{"Vitest assertion logs zero-test wording before its final summary", strings.Join([]string{
			" RUN  v3.2.4 /workspace", "", " FAIL  a.test.js > loads", "Error: Failed to load url ../steps",
			"Tests no tests", " Test Files  1 failed (1)", "      Tests  1 failed (1)",
		}, "\n"), false},
		{"Vitest assertion logs zero-test wording before genuine failed-test summary", vitestLoggedNoTestsOutput, false},
	}
}

func redDidNotRunNodeCases() []struct {
	name, output string
	want         bool
} {
	return []struct {
		name, output string
		want         bool
	}{
		{"node --test tap", strings.Join([]string{
			"TAP version 13", "# /tmp/a.test.js:3", "# SyntaxError: Unexpected end of input",
			"#     at wrapSafe (node:internal/modules/cjs/loader:1637:18)", "# Node.js v22.22.1",
			"# Subtest: a.test.js", "not ok 1 - a.test.js", "  ---", "  failureType: 'testCodeFailure'",
			"  exitCode: 1", "  error: 'test failed'", "  code: 'ERR_TEST_FAILURE'", "  ...", "1..1",
			"# tests 1", "# pass 0", "# fail 1",
		}, "\n"), true},
		{"node --test spec", strings.Join([]string{
			"/tmp/a.test.js:3", "", "", "", "SyntaxError: Unexpected end of input",
			"    at wrapSafe (node:internal/modules/cjs/loader:1637:18)", "", "Node.js v22.22.1",
			"✖ a.test.js (20.180916ms)", "ℹ tests 1", "ℹ pass 0", "ℹ fail 1", "", "✖ failing tests:", "",
			"test at a.test.js:1:1", "✖ a.test.js (20.180916ms)", "  'test failed'",
		}, "\n"), true},
		{"node --test runtime SyntaxError", strings.Join([]string{
			"✖ x (0.370709ms)", "ℹ tests 1", "ℹ pass 0", "ℹ fail 1", "", "✖ failing tests:", "",
			"test at b.test.js:2:1", "✖ x (0.370709ms)",
			"  SyntaxError: Expected property name or '}' in JSON at position 1 (line 1 column 2)",
			"      at JSON.parse (<anonymous>)",
		}, "\n"), false},
		{"node --test assertion text", strings.Join([]string{
			"TAP version 13", "# Subtest: x", "not ok 1 - x", "  ---", "  failureType: 'testCodeFailure'",
			"  error: |-", "    want", "    error TS2322: nope", "    SyntaxError: nope", "  code: 'ERR_ASSERTION'",
			"  name: 'AssertionError'", "  ...", "1..1", "# tests 1", "# pass 0", "# fail 1",
		}, "\n"), false},
	}
}

func TestRedClassificationUsesOutputBeyondTheDisplayedTail(t *testing.T) {
	var command strings.Builder
	command.WriteString("printf '%s\\n' ")
	command.WriteString("'_________________________ ERROR collecting test_bad.py _________________________'; ")
	command.WriteString("i=0; while [ $i -lt 65 ]; do printf 'line %s\\n' \"$i\"; i=$((i+1)); done")
	tail, output, err := shOutput(t.TempDir(), command.String(), timeout)
	if err != nil || strings.Contains(tail, "ERROR collecting") || !strings.Contains(output, "ERROR collecting") {
		t.Fatalf("want only the full output to retain the collection error; tail: %q", tail)
	}
}

func TestRunChecksTheProofOfEachTaskType(t *testing.T) {
	keepGoCache(t)
	for _, c := range proofCases {
		t.Run(c.name, func(t *testing.T) {
			if c.binary {
				t.Setenv("ATM_TEST_BINARY", executedBinary(t))
			}
			root := repo(t, "https://example.com/owner/repo.git", cmp.Or(c.atm, atmYAML))
			commitBase(t, root, "lib.sh", c.lib)
			commitBase(t, root, ".gitignore", c.ignore)
			commitBase(t, root, "hooks_test.sh", c.hooks)
			commitFiles(t, root, "", c.files)
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
			assertNoTrials(t, end)
			if left := clones(t, root); len(left) != 0 {
				t.Fatalf("the run's clone outlived it: %v", left)
			}
		})
	}
}

func TestRunProvesATestsIssueOnlyThroughTheReportedFilesGoTests(t *testing.T) {
	gocache, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	got := filepath.Join(t.TempDir(), "ran")
	t.Setenv("ATM_TEST_OUT", got)
	atm := strings.Replace(
		atmSet(atmYAML, "test_file", `go test -vet=off -count=1 -v -run {tests} {dir} >> "$ATM_TEST_OUT"`),
		`"**/*_test.sh"`, `"**/*_test.go"`, 1)
	root := repo(t, "https://example.com/owner/repo.git", atm)
	t.Setenv("GOCACHE", strings.TrimSpace(string(gocache))) // repo moves HOME, and the cache with it
	commitFiles(t, root, "", map[string]string{"go.mod": "module example.com/m\n\ngo 1.21\n",
		"p/old_test.go": "package p\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) { t.Fatal(\"old\") }\n"})
	t.Setenv("ATM_TEST_NEW", "package p\n\nimport \"testing\"\n\ntype T = testing.T\n\n"+
		"func TestNew(t *testing.T) {}\n\nfunc TestNewToo(t *testing.T) {}\n\nfunc TestAlias(t *T) {}\n\n"+
		"func Testhelper(t *testing.T) { t.Fatal(\"invalid test name\") }\n\n"+
		"/*\nfunc TestOld(t *testing.T) {}\n*/\n\nfunc helper() {}\n")
	t.Setenv("FAKE_AGENT_WORK", `printf %s "$ATM_TEST_NEW" > p/new_test.go; `+reportJSON("p/new_test.go"))
	var out bytes.Buffer
	if err := Run("t", []string{issueFile(t, "# Cover p\nType: tests\nCover p.\n")}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(got)
	ran := regexp.MustCompile(`(?m)^=== RUN   (\w+)$`).FindAllStringSubmatch(string(b), -1)
	var names []string
	for _, m := range ran {
		names = append(names, m[1])
	}
	if strings.Join(names, " ") != "" {
		t.Fatalf("want no isolated test_file invocation, got %q", names)
	}
}

func TestRunRejectsATestsIssueWhenBuildTagsSelectNoTests(t *testing.T) {
	gocache, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	atm := strings.Replace(atmSet(atmYAML, "test_file", "go test -count=1 -run {tests} {dir}"),
		`"**/*_test.sh"`, `"**/*_test.go"`, 1)
	root := repo(t, "https://example.com/owner/repo.git", atm)
	t.Setenv("GOCACHE", strings.TrimSpace(string(gocache)))
	commitFiles(t, root, "", map[string]string{"go.mod": "module example.com/m\n\ngo 1.21\n",
		"p/old_test.go": "package p\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) {}\n"})
	t.Setenv("ATM_TEST_TAGGED", "//go:build migrate_100k\n\npackage p\n\nimport \"testing\"\n\n"+
		"func TestTagged(t *testing.T) {}\n")
	t.Setenv("FAKE_AGENT_WORK", `printf %s "$ATM_TEST_TAGGED" > p/new_test.go; `+reportJSON("p/new_test.go"))
	var out bytes.Buffer
	err = Run("t", []string{issueFile(t, "# Cover p\nType: tests\nCover p.\n")}, &out, io.Discard)
	if err != nil || checksStep(t, &out)["state"] != "passed" {
		t.Fatalf("want the configured suite, with no type-specific proof, got %v", err)
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
	atm = atmSet(atm, "test_file", `echo proof >> "$ATM_TEST_OUT"; sh {file}`)
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
	// Install runs in the clone step, then again in the checks before the proof: the agent may have changed
	// dependencies.
	if b, _ := os.ReadFile(got); !regexp.MustCompile(`^install\ninstall\ntest\ntypecheck\n$`).Match(b) {
		t.Fatalf("ran %q", b)
	}
}

const sourceWork = `printf 'fixed\nextra\nmore\n' > source.txt; ` +
	`echo 'grep -q fixed source.txt && ! grep -q broken source.txt' > regression_test.sh; ` +
	`mkdir -p .atm; printf '{"test_file":"regression_test.sh"}' > .atm/fake-report.json`

// realTools is a worker that, before its fix, does what a real one does inside the clone: runs the
// repository's own lint, which downloads slopslint under the ignored .tmp/ and runs it, then builds and runs
// the repository's Go program there.
func realToolsWork() string {
	binary := ".tmp/m"
	tools := "go build -o " + binary + " . && " + binary
	switch runtime.GOOS {
	case "darwin", "linux":
		tools = "sh scripts/lint.sh && " + tools
	default:
		tools = "mkdir -p .tmp && " + tools
	}
	return tools + " && echo fixed > a.txt && echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh"
}

// auditCases are the adversarial agents of the audit of 7 Oct 2026, and one that behaves. base is the files
// committed on main, the ATM repository's file when the value is "atm"; why, when not "", names the failure
// of node, the failed one, or the cut's rejection when node is "". delivered is what delivery gets: each
// commit as subject|author|committer, then every file in the clone, ignored ones too but .tmp/'s, then
// every .txt file's content; a failed run delivers nothing.
var auditCases = []struct {
	name, mode, work, ponytail, report, atm, why, node, delivered string
	base                                                          map[string]string
}{
	{name: "behaves", delivered: unit1 + initial + placed + ".atm.yaml\na.txt\na_test.sh\nfixed\n"},
	{name: "commits directly", work: fixWork + "; git add -A; git commit -qm fix", why: "the agent made commits",
		node: "checks"},
	{name: "a declared command commits during checks",
		atm: atmSet(atmYAML, "lint", "git commit --allow-empty -qm lint"), why: "the commands made commits", node: "checks"},
	{name: "deletes an existing test", atm: atmSet(atmYAML, "test", suite),
		work:      "echo fixed > a.txt; rm a_test.sh" + reportTest("regression_test.sh"),
		delivered: unit1 + initial + ".atm/fake-report.json\n" + placed + ".atm.yaml\na.txt\nregression_test.sh\nfixed\n"},
	{name: "red on a new symbol in an old file", base: map[string]string{"lib.sh": lib},
		work:      "echo '" + helper + "' >> lib.sh" + testThrough("helper"),
		delivered: unit1 + based + placed + ".atm.yaml\na.txt\na_test.sh\nlib.sh\nbroken\n"},
	{name: "red faked with ignored state", base: map[string]string{".gitignore": "state/\n"},
		work: "echo 'if test -f state/seen; then exit 0; fi; mkdir -p state; touch state/seen; exit 1' > " +
			"regression_test.sh; " + reportJSON("regression_test.sh"),
		why: "test:", node: "checks", atm: atmSet(atmYAML, "test", suite)},
	{name: "kept cut that creates an ignored file", delivered: uncut, base: map[string]string{".gitignore": "cache/\n"},
		work: "printf 'fixed\\nextra\\nmore\\n' > a.txt" + fixTest, report: cut,
		ponytail: "echo fixed > a.txt; mkdir cache; echo injected > cache/injected.txt", why: "adds cache/injected.txt"},
	{name: "rejected cut that creates an ignored file", delivered: uncut,
		base: map[string]string{".gitignore": "cache/\n"},
		work: "printf 'fixed\\nextra\\nmore\\n' > a.txt" + fixTest, report: cut,
		ponytail: "echo extra >> a.txt; mkdir cache; echo injected > cache/injected.txt", why: "shorten"},
	{name: "cut that breaks the configured suite", delivered: unit1 + based + ".atm/fake-report.json\n" +
		placed + ".atm.yaml\na.txt\na_test.sh\nregression_test.sh\nsource.txt\nbroken\nfixed\nextra\nmore\n",
		base: map[string]string{"source.txt": "broken\n"},
		work: sourceWork, report: strings.Replace(cut, "a.txt", "source.txt", 1),
		ponytail: "echo broken > source.txt", why: "test:",
		atm: atmSet(atmYAML, "test", suite)},
	{name: "runs real tools in the clone", work: realToolsWork(), delivered: unit1 + based + placed +
		".atm.yaml\n.gitignore\n.slop/ceilings.yml\n.slop/config.yml\n.slop/tombstones/README.md\n" +
		"a.txt\na_test.sh\ngo.mod\ninternal/probe1/probe.go\ninternal/probe2/probe.go\nmain.go\nscripts/lint.sh\n" +
		"scripts/slopslint.sh\nfixed\n",
		base: map[string]string{".gitignore": ".tmp/\n", ".slop/config.yml": "schema: 1\ndetector:\n  name: jscpd\n" +
			"  version: \"4.2.5\"\ndefaults:\n  format: go\n  mode: mild\n  min_lines: 1\n  min_tokens: 1\n" +
			"global_ignore:\n  - \"**/.git/**\"\n  - \"**/.tmp/**\"\nscopes:\n  go_production:\n" +
			"    scan_path: .\n    pattern: \"**/*.go\"\n    ignore:\n      - \"**/*_test.go\"\n",
			".slop/ceilings.yml":         "schema: 1\nscopes:\n  go_production:\n    active_clones_ceiling: 10\n",
			".slop/tombstones/README.md": "atm", "go.mod": "module example.com/m\n\ngo 1.21\n",
			"internal/probe1/probe.go": "package probe\n\nfunc Probe(a, b, c, d, e int) int {\n" +
				"return a + b + c + d + e + a*b + b*c + c*d + d*e + e*a +\n" +
				"a - b + c - d + e - a + b - c + d - e + a + b + c + d + e +\n" +
				"a*b + b*c + c*d + d*e + e*a + a-b + b-c + c-d + d-e + e-a +\n" +
				"a + b*c - d + e*a - b + c*d - e + a*b - c + d*e - a\n}\n",
			"internal/probe2/probe.go": "package probe\n\nfunc Probe(a, b, c, d, e int) int {\n" +
				"return a + b + c + d + e + a*b + b*c + c*d + d*e + e*a +\n" +
				"a - b + c - d + e - a + b - c + d - e + a + b + c + d + e +\n" +
				"a*b + b*c + c*d + d*e + e*a + a-b + b-c + c-d + d-e + e-a +\n" +
				"a + b*c - d + e*a - b + c*d - e + a*b - c + d*e - a\n}\n",
			"main.go":         "package main\n\nfunc main() {}\n",
			"scripts/lint.sh": "atm", "scripts/slopslint.sh": "atm"}},
}

// atmID is repo's git identity as author and committer; unit1, initial and based are commits delivery gets,
// placed the skills ATM leaves in the clone, and uncut what delivery gets when a cut on a.txt is rejected.
const (
	atmID   = "atm <atm@example.com>|atm <atm@example.com>"
	unit1   = "chore: Retry on timeout|" + atmID + "\n"
	initial = "init|" + atmID + "\n"
	based   = "base|" + atmID + "\n" + initial
	placed  = ".claude/skills/ponytail-review/SKILL.md\n.claude/skills/ponytail/SKILL.md\n"
	uncut   = unit1 + based + placed + ".atm.yaml\n.gitignore\na.txt\na_test.sh\nfixed\nextra\nmore\n"
)

// commitFiles commits files, each path's content or, when it is "atm", the ATM repository's file at atm, on
// root's base, when there are any.
func commitFiles(t *testing.T, root, atm string, files map[string]string) {
	t.Helper()
	if len(files) == 0 {
		return
	}
	for name, text := range files {
		var err error
		b := []byte(text)
		if text == "atm" {
			b, err = os.ReadFile(filepath.Join(atm, name))
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		if err == nil {
			err = os.MkdirAll(filepath.Dir(path), 0o755)
		}
		if err == nil {
			err = os.WriteFile(path, b, 0o755)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-qm", "base")
}

// runResult is the failed node and delivered HEAD of the run at root, after checking every node passed when
// none failed.
func runResult(t *testing.T, root string) (string, string) {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "report.json"))
	var r verdict
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	for _, n := range r.Nodes {
		if r.FailedNode == "" && n.Result != "passed" { // every node reached its real verdict
			t.Errorf("want every node passed, %s %s", n.Name, n.Result)
		}
	}
	return r.FailedNode, r.HeadSHA
}

func assertAuditCase(t *testing.T, c struct {
	name, mode, work, ponytail, report, atm, why, node, delivered string
	base                                                          map[string]string
}, out *bytes.Buffer, err error, failedNode, head, got string) {
	t.Helper()
	switch {
	case failedNode != c.node:
		t.Fatalf("want the failed node %q, got %q: %v", c.node, failedNode, err)
	case c.node != "" && (err == nil || !strings.Contains(err.Error(), c.why)):
		t.Fatalf("want an error naming %q, got %v", c.why, err)
	case c.node == "" && err != nil:
		t.Fatalf("want a pass, got %v", err)
	}
	if p := ends(t, out, "ponytail"); c.node == "" && c.why != "" &&
		(p[0]["kept"] != false || !strings.Contains(fmt.Sprint(p[0]["reason"]), c.why)) {
		t.Fatalf("want the cut rejected for %q, got %v", c.why, p)
	}
	want := c.delivered
	if c.node == "" {
		want = head + "\n" + want
	}
	if d, _ := os.ReadFile(got); string(d) != want {
		t.Fatalf("delivery got\n%s\nwant\n%s", d, want)
	}
}

func runAuditCase(t *testing.T, atm, gocache string, c struct {
	name, mode, work, ponytail, report, atm, why, node, delivered string
	base                                                          map[string]string
}) {
	t.Helper()
	t.Setenv("GOCACHE", gocache) // repo moves HOME, and the cache with it
	got := filepath.Join(t.TempDir(), "delivered")
	t.Setenv("ATM_TEST_OUT", got)
	root := repo(t, "https://example.com/owner/repo.git", atmSet(cmp.Or(c.atm, atmYAML), "delivery",
		`{ git rev-parse HEAD; git log --format="%s|%an <%ae>|%cn <%ce>"; `+
			`git ls-files -co -x .tmp; cat *.txt; } > "$ATM_TEST_OUT"`))
	commitFiles(t, root, atm, c.base)
	t.Setenv("FAKE_AGENT", c.mode)
	t.Setenv("FAKE_AGENT_WORK", c.work)
	t.Setenv("FAKE_PONYTAIL_WORK", c.ponytail)
	t.Setenv("FAKE_PONYTAIL_REPORT", c.report)
	var out bytes.Buffer
	err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard)
	node, head := runResult(t, root)
	assertAuditCase(t, c, &out, err, node, head, got)
}

func TestRunGivesTheAuditsAdversarialAgentsNoPass(t *testing.T) {
	atm, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	gocache, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range auditCases {
		t.Run(c.name, func(t *testing.T) { runAuditCase(t, atm, strings.TrimSpace(string(gocache)), c) })
	}
}

func assertNoTrials(t *testing.T, result obj) {
	t.Helper()
	for _, removed := range []string{"red", "green", "proof_span"} {
		if _, exists := result[removed]; exists {
			t.Fatalf("removed proof still recorded: %v", result)
		}
	}
}
