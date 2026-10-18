package run

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// checks is the verdict on the agent's work in clone, at base commit sha, for a task of type typ whose report
// names test: HEAD has not moved, the ignored files the agent made are gone, as delivery leaves them out, then
// install, the type's proof, test, e2e, typecheck and lint pass, and HEAD has not
// moved still. Install runs again before the proof: the agent may have changed dependencies. Each proof and
// command is a row of the screen. Its result holds the proof's red and green trials.
func checks(clone, runDir, sha, typ, test string, missingReport bool, c config.Config) (result map[string]any, err error) {
	live := liveIn(clone)
	var red, green try
	var proofSpan span
	result = map[string]any{"red": red, "green": green, "proof_span": proofSpan}
	cover, err := coverageEnv(runDir, c.E2E)
	var ran []cmdResult
	run := func(name, line string, env ...string) error {
		if line == "" {
			return nil
		}
		var tail string
		s, err := row(live, name, func() (err error) {
			tail, _, err = shOutput(clone, line, cmp.Or(time.Duration(c.CheckTimeout), timeout), env...)
			return err
		})
		ran = append(ran, cmdResult{name, line, outcome(err), tail, s})
		result["commands"] = ran
		return wrap(err, name)
	}
	if err == nil {
		err = moved(clone, sha, "the agent")
	}
	if err == nil {
		var restore func() error
		restore, err = hideATM(clone, runDir)
		if restore != nil {
			defer func() { err = errors.Join(err, restore()) }()
		}
		if err == nil {
			err = unmade(clone)
		}
	}
	if err == nil {
		err = run("install", c.Install)
	}
	if err != nil {
		return result, err
	}
	if !missingReport || typ == "refactor" || typ == "docs" || typ == "chore" {
		proofSpan, err = row(live, proofs[typ], func() (err error) {
			red, green, err = prove(clone, typ, filepath.ToSlash(test), c)
			return err
		})
		result["red"], result["green"], result["proof_span"] = red, green, proofSpan
		if err != nil {
			return result, err
		}
	} else {
		if _, err = git(clone, "add", "-A"); err != nil {
			return result, err
		}
		changed, _, changeErr := changes(clone, typ, "")
		if changeErr == nil && len(changed) == 0 {
			changeErr = errors.New("no change to prove")
		}
		if changeErr == nil && typ == "tests" {
			if p := first(changed, slices.Concat(c.TestPatterns, c.DocsPatterns), false); p != "" {
				changeErr = fmt.Errorf("a tests task changed the source %s", p)
			}
		}
		if changeErr == nil {
			changeErr = shrunk(clone, changed, c.TestPatterns)
		}
		_, resetErr := git(clone, "reset", "-q")
		if err = errors.Join(changeErr, resetErr); err != nil {
			return result, err
		}
	}
	for _, cmd := range [][2]string{{"test", c.Test}, {"e2e", c.E2E}, {"typecheck", c.Typecheck}, {"lint", c.Lint}} {
		if err := run(cmd[0], cmd[1], coverFor(cover, cmd[0])...); err != nil {
			return result, err
		}
	}
	result["commands"] = ran
	return result, moved(clone, sha, "the commands")
}

// coverageEnv is one fresh GOCOVERDIR for a Checks that declares e2e.
// coverFor gives it to test and e2e only: proof, install, typecheck and lint must not write those counters.
func coverageEnv(runDir, e2e string) ([]string, error) {
	if e2e == "" {
		return nil, nil
	}
	dir, err := os.MkdirTemp(runDir, "coverage-")
	if err != nil {
		return nil, err
	}
	return []string{"GOCOVERDIR=" + dir}, nil
}

func coverFor(cover []string, name string) []string {
	if name == "test" || name == "e2e" {
		return cover
	}
	return nil
}

func hideATM(clone, runDir string) (func() error, error) {
	path := filepath.Join(clone, ".atm")
	stash := filepath.Join(runDir, "checks-atm")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return func() error { return os.RemoveAll(path) }, nil
	} else if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(stash); err == nil {
		return nil, fmt.Errorf("checks artefact path already exists: %s", stash)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.Rename(path, stash); err != nil {
		return nil, err
	}
	return func() error {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		return os.Rename(stash, path)
	}, nil
}

// before records every file in clone, ignored or not, as the agent finds it, for reset and, as refs/atm-before,
// for unmade, those of an ignored repository nested in it, which whole sees as one, as refs/atm-before of
// .git/atm-nested, and the path of every file and directory in it, those of a nested repository too, for unmade.
func before(clone string) error {
	tree, err := whole(clone)
	if err == nil {
		_, err = every(clone, "update-ref", "refs/atm-before", tree)
	}
	if err == nil {
		tree, err = nestedTree(clone)
	}
	if err == nil && tree != "" {
		_, err = nestedGit(clone, ".", "update-ref", "refs/atm-before", tree)
	}
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(clone)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var was []string
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && p == ".git" {
			return fs.SkipDir
		}
		was = append(was, p)
		return err
	})
	if err == nil {
		err = root.WriteFile(".git/atm-before", []byte(strings.Join(was, "\x00")), 0o600)
	}
	return err
}

// unmade removes agent-created ignored content using before's paths, except follow-up tests the run keeps, then
// puts back, byte for byte, each ignored file before recorded that the agent changed or deleted, those of an
// ignored nested repository too: a package manager's own state, which would tell install the agent's
// dependencies are there.
func unmade(clone string) error {
	err := prunes(clone)
	if err == nil {
		err = putBackIgnored(clone)
	}
	if err == nil {
		err = putBackNested(clone)
	}
	return err
}

// putBackIgnored puts back, byte for byte, each ignored file before recorded that the agent changed or deleted.
func putBackIgnored(clone string) error {
	now, err := whole(clone)
	if err != nil {
		return err
	}
	changed, err := every(clone, "diff", "--name-only", "--no-renames", "-z", "--diff-filter=DMT",
		"refs/atm-before", now, "--", ".", ":(exclude).atm") // hideATM's
	if err != nil || changed == "" {
		return err
	}
	ignored, err := commandIn(strings.NewReader(changed), clone, clone, "git", "check-ignore", "-z", "--stdin")
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil // none is ignored
	}
	if err == nil {
		_, err = commandIn(strings.NewReader(ignored), clone, clone, "git", "--literal-pathspecs",
			"--git-dir=.git/atm-files", "--work-tree=.", "checkout", "refs/atm-before",
			"--pathspec-from-file=-", "--pathspec-file-nul")
	}
	return err
}

// putBackNested puts back, byte for byte, each file of an ignored nested repository before recorded that the
// agent changed or deleted, its nearest existing directory writable meanwhile. It never touches the repository's .git.
func putBackNested(clone string) error {
	now, err := nestedTree(clone)
	if err != nil || now == "" {
		return err
	}
	changed, err := nestedGit(clone, ".", "diff", "--name-only", "--no-renames", "-z", "--diff-filter=DMT",
		"refs/atm-before", now)
	if err != nil || changed == "" {
		return err
	}
	root, err := os.OpenRoot(clone)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	modes := map[string]fs.FileMode{}
	for _, p := range strings.Split(strings.TrimSuffix(changed, "\x00"), "\x00") {
		d := path.Dir(p)
		info, statErr := root.Stat(d)
		for errors.Is(statErr, fs.ErrNotExist) && d != "." {
			d = path.Dir(d)
			info, statErr = root.Stat(d)
		}
		if statErr != nil {
			err = errors.Join(err, statErr)
			continue
		}
		if modes[d] == 0 {
			modes[d] = info.Mode()
			err = errors.Join(err, root.Chmod(d, info.Mode()|0o700))
		}
	}
	if err == nil {
		_, err = commandIn(strings.NewReader(changed), clone, clone, "git", "--literal-pathspecs",
			"--git-dir=.git/atm-nested", "--work-tree=.", "checkout", "refs/atm-before",
			"--pathspec-from-file=-", "--pathspec-file-nul")
	}
	for d, m := range modes {
		err = errors.Join(err, root.Chmod(d, m))
	}
	return err
}

// nestedTree records in .git/atm-nested the files, all but .git, of each ignored repository nested in clone,
// which whole sees as one gitlink, and is their tree. ponytail: a repository nested in one of them stays a
// gitlink; recurse if a cache ever nests them.
func nestedTree(clone string) (string, error) {
	out, err := git(clone, "ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	if err != nil {
		return "", err
	}
	if !strings.Contains(out, "/\x00") {
		// No nested files to record. An existing store still needs an empty tree
		// so deletions and a later before can replace its previous snapshot.
		if _, err := os.Stat(filepath.Join(clone, ".git", "atm-nested")); errors.Is(err, fs.ErrNotExist) {
			return "", nil
		} else if err != nil {
			return "", err
		}
	}
	err = store(clone, "atm-nested")
	var trees [][2]string
	for _, p := range strings.Split(out, "\x00") {
		if err != nil || !strings.HasSuffix(p, "/") { // a nested repository is listed as dir/
			continue
		}
		var tree string
		if _, err = nestedGit(clone, p, "read-tree", "--empty"); err == nil {
			_, err = nestedGit(clone, p, "add", "-A", "-f", ".")
		}
		if err == nil {
			tree, err = nestedGit(clone, p, "write-tree")
		}
		trees = append(trees, [2]string{p, tree})
	}
	if err == nil {
		_, err = nestedGit(clone, ".", "read-tree", "--empty")
	}
	for _, t := range trees {
		if err == nil {
			_, err = nestedGit(clone, ".", "read-tree", "--prefix="+t[0], t[1])
		}
	}
	if err != nil {
		return "", err
	}
	return nestedGit(clone, ".", "write-tree")
}

// nestedGit runs git in clone on .git/atm-nested, as every does on .git/atm-files, with dir its working tree.
func nestedGit(clone, dir string, args ...string) (string, error) {
	return git(clone, append([]string{"-c", "core.bigFileThreshold=1", "--git-dir=.git/atm-nested",
		"--work-tree=" + dir}, args...)...)
}

// prunes removes agent-created ignored content using before's paths, except follow-up tests the run keeps.
func prunes(clone string) error {
	root, err := os.OpenRoot(clone)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	was, err := root.ReadFile(".git/atm-before")
	if err != nil {
		return err
	}
	had := map[string]bool{}
	for _, p := range strings.Split(string(was), "\x00") {
		had[p] = true
	}
	now, err := git(clone, "ls-files", "-z", "--others", "--ignored", "--exclude-standard")
	for _, p := range strings.Split(now, "\x00") {
		if err == nil && p != "" {
			err = prune(root, strings.TrimSuffix(p, "/"), had) // a nested repository is listed as dir/
		}
	}
	return err
}

// prune removes p from clone when before did not record it, or else what it holds that before did not record. It
// never follows a link.
func prune(root *os.Root, p string, had map[string]bool) error {
	if !had[p] {
		return drop(root, p, had)
	}
	info, err := root.Lstat(p)
	if err != nil || !info.IsDir() {
		return err
	}
	entries, err := fs.ReadDir(root.FS(), p)
	for _, e := range entries {
		if err == nil {
			err = prune(root, p+"/"+e.Name(), had)
		}
	}
	return err
}

// drop removes p from clone, a link as a link, a directory with all it holds, read-only or not, then each
// directory above it that before did not record and that it leaves empty.
func drop(root *os.Root, p string, had map[string]bool) error {
	info, err := root.Lstat(p)
	if err != nil {
		return err
	}
	if info.IsDir() {
		err = fs.WalkDir(root.FS(), p, func(q string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			return root.Chmod(q, 0o700)
		})
	}
	if err == nil {
		err = unlink(root, p, root.RemoveAll)
	}
	if err != nil {
		return err
	}
	for p = path.Dir(p); p != "." && !had[p]; p = path.Dir(p) {
		left, err := fs.ReadDir(root.FS(), p)
		if err == nil && len(left) == 0 {
			err = unlink(root, p, root.Remove)
		}
		if err != nil || len(left) > 0 {
			return err
		}
	}
	return nil
}

// unlink is rm of p, with p's directory, when rm needs it, writable for it and then as it was.
func unlink(root *os.Root, p string, rm func(string) error) error {
	err := rm(p)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	dir := path.Dir(p)
	info, statErr := root.Stat(dir)
	if statErr == nil {
		statErr = root.Chmod(dir, info.Mode()|0o700)
	}
	if statErr != nil {
		return errors.Join(err, statErr)
	}
	return errors.Join(rm(p), root.Chmod(dir, info.Mode()))
}

// moved is an error when HEAD in clone is not want: who made commits.
func moved(clone, want, who string) error {
	head, err := git(clone, "rev-parse", "HEAD")
	if err == nil && head != want {
		err = fmt.Errorf("%s made commits: HEAD is %s, not the base %s", who, head, want)
	}
	return err
}

// proofs is the screen's row for the proof of each task type; a chore has none.
var proofs = map[string]string{"fix": "red/green", "feature": "red/green", "greenfield": "red/green",
	"refactor": "suite on the base", "tests": "test on the base", "docs": "docs only"}

// row runs fn as the screen's row name, under the node that runs, when name is not "", and returns its span, the one
// the screen shows.
func row(live func(map[string]any), name string, fn func() error) (s span, err error) {
	if name == "" {
		return s, fn()
	}
	live(map[string]any{"check": name, "state": "started"})
	s = timed(func() { err = fn() })
	live(map[string]any{"check": name, "state": outcome(err), "duration_ms": s.DurationMS})
	return s, err
}

// prove runs the proof of task type typ on the agent's work in clone, all of it staged meanwhile, and returns its
// trials: red without the work, green with it, each zero when not run.
func prove(clone, typ, test string, c config.Config) (red, green try, err error) {
	limit := cmp.Or(time.Duration(c.CheckTimeout), timeout)
	if _, err := git(clone, "add", "-A"); err != nil {
		return red, green, err
	}
	defer func() {
		_, e := git(clone, "reset", "-q") // the agent's work back to unstaged
		err = errors.Join(err, e)
	}()
	tree, err := keep(clone)
	if err != nil {
		return red, green, err
	}
	changed, aside, err := changes(clone, typ, test)
	if err != nil {
		return red, green, err
	}
	defer func() { // every type, after its own proof, which names a failure better
		if err == nil {
			err = shrunk(clone, changed, c.TestPatterns)
		}
	}()
	switch typ {
	case "fix", "feature", "greenfield":
		red, green, err := redGreen(clone, tree, aside, test, c.TestFile, limit)
		if err == nil && typ == "fix" {
			err = onBase(clone, tree, aside, test, c.TestFile, limit)
		}
		return red, green, err
	case "refactor":
		if p := first(changed, c.TestPatterns, true); p != "" {
			return red, green, fmt.Errorf("a refactor changed the test %s", p)
		}
		base, _, err := trial(clone, tree, changed, c.Test, "", "", false, nil, limit)
		return base, green, errors.Join(err, wrap(base.err, "the suite fails on the base"))
	case "tests":
		if p := first(changed, slices.Concat(c.TestPatterns, c.DocsPatterns), false); p != "" {
			return red, green, fmt.Errorf("a tests task changed the source %s", p)
		}
		line, err := testLine(clone, c.TestFile, test)
		if err != nil {
			return red, green, err
		}
		base, after, err := trial(clone, tree, aside, line, line, test,
			strings.Contains(c.TestFile, "{tests}"), nil, limit)
		return base, after, errors.Join(err, wrap(base.err, test+" fails on the base"),
			wrap(after.err, test+" fails after the change"))
	case "docs":
		if p := first(changed, c.DocsPatterns, false); p != "" {
			return red, green, fmt.Errorf("a docs task changed %s, which is not docs", p)
		}
	}
	return red, green, nil
}

// changes is every path the staged work in clone changes and what the base run sets aside: all but the test;
// for a fix, only what the base has. A test that fails without the fix's new files alone then passes without
// the fix. ponytail: so a fix made only of new files is rejected too.
func changes(clone, typ, test string) (changed, aside []string, err error) {
	out, err := git(clone, "diff", "--cached", "--name-status", "--no-renames", "-z", "HEAD")
	for f := strings.Split(out, "\x00"); len(f) > 1; f = f[2:] {
		if changed = append(changed, f[1]); f[1] != test && (typ != "fix" || f[0] != "A") {
			aside = append(aside, f[1])
		}
	}
	return changed, aside, err
}

// shrunk is an error naming the first of changed that matches one of tests, is in the base and is not staged in
// clone with as many lines, at the path git's rename detection moves it to.
func shrunk(clone string, changed, tests []string) error {
	lines := func(s string) int {
		n := strings.Count(s, "\n")
		if s != "" && !strings.HasSuffix(s, "\n") {
			n++
		}
		return n
	}
	out, err := git(clone, "diff", "--cached", "--name-status", "-M", "--diff-filter=R", "-z", "HEAD")
	if err != nil {
		return err
	}
	to := map[string]string{}
	for f := strings.Split(out, "\x00"); len(f) > 2; f = f[3:] {
		to[f[1]] = f[2]
	}
	for _, p := range changed {
		if first([]string{p}, tests, true) == "" {
			continue
		}
		was, err := gitRaw(clone, "cat-file", "-p", "HEAD:"+p)
		if err != nil {
			continue // new
		}
		is, err := gitRaw(clone, "cat-file", "-p", ":"+cmp.Or(to[p], p)) // staged: an error when deleted
		if err != nil || lines(is) < lines(was) {
			return fmt.Errorf("the base's test %s is gone or shorter", p)
		}
	}
	return nil
}

// notRun matches parse or compile diagnostics printed by Go, pytest, node --test, and tsc.
var notRun = regexp.MustCompile(`^(FAIL\s+\S+\s+\[(build|setup) failed\]$|_+ ERROR collecting .+ _+$|` +
	`E\s+(SyntaxError|ImportError)\b|(# )?SyntaxError:|` +
	`\S.*\(\d+,\d+\): error TS\d+:|\S.*:\d+:\d+ - error TS\d+:|error TS\d+:)`)

// jestFile is Jest's FAIL header of a test file, under which it says the file's suite failed to run.
var jestFile = regexp.MustCompile(`^\s*FAIL\s+\S+(\s+\(.*\))?\s*$`)

var testResult = regexp.MustCompile(`^(--- (PASS|FAIL):|ok\s+\S+|` +
	`.+::.+\s+(PASSED|FAILED|SKIPPED|XFAIL|XPASS)\b|(PASSED|FAILED|SKIPPED|XFAIL|XPASS)\s+\S+::|` +
	`[✓✔√]\s|[✕✘×]\s|● )`)
var jestTestsRan = regexp.MustCompile(`^Tests:\s+[1-9]\d*\s+(passed|failed|skipped|todo)\b`)
var tapFailedTest = regexp.MustCompile(`^not ok \d+ - `)
var tapAssertionFailure = regexp.MustCompile(`^(code: 'ERR_ASSERTION'|name: 'AssertionError')$`)
var vitestRun = regexp.MustCompile(`^RUN\s+v\d`)
var vitestLoadError = regexp.MustCompile(`^Error: Failed to load url\s+`)
var vitestNoTests = regexp.MustCompile(`^Tests\s+(no tests|0(?:\s|$))`)
var vitestTestsFailed = regexp.MustCompile(`^Tests\s+[1-9]\d*\s+failed\b`)
var vitestFilesFailed = regexp.MustCompile(`^Test Files\s+[1-9]\d*\s+failed\b`)

// redDidNotRun says whether output, a red's whole, holds a runner's diagnostic that the test file did not parse
// or compile and no test result.
func redDidNotRun(output string) bool {
	lines := strings.Split(output, "\n")
	return hasRunnerDiagnostic(lines) && !hasTestResult(lines) || vitestLoadFailure(lines)
}

func hasRunnerDiagnostic(lines []string) bool {
	jest := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if jest && trimmed == "● Test suite failed to run" || notRun.MatchString(line) {
			return true
		}
		jest = jest || jestFile.MatchString(line)
	}
	return false
}

func hasTestResult(lines []string) bool {
	jest, tap, tapFailed := false, false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed != "● Test suite failed to run" && testResult.MatchString(trimmed) ||
			tap && tapFailed && tapAssertionFailure.MatchString(trimmed) ||
			runnerSummaryHasFailure(trimmed, jest, vitestRun.MatchString(trimmed)) {
			return true
		}
		jest = jest || jestFile.MatchString(line)
		tap = tap || strings.HasPrefix(trimmed, "TAP version ")
		tapFailed = tapFailed || tap && tapFailedTest.MatchString(trimmed)
	}
	return false
}

func runnerSummaryHasFailure(line string, jest, vitest bool) bool {
	return jest && jestTestsRan.MatchString(line) || vitest && vitestTestsFailed.MatchString(line)
}

func vitestLoadFailure(lines []string) bool {
	run, load := false, false
	noTests, failedFiles := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		run = run || vitestRun.MatchString(trimmed)
		if !run {
			continue
		}
		load = load || vitestLoadError.MatchString(trimmed)
		if strings.HasPrefix(trimmed, "Tests ") {
			if vitestTestsFailed.MatchString(trimmed) {
				return false
			}
			noTests = vitestNoTests.MatchString(trimmed)
		}
		if strings.HasPrefix(trimmed, "Test Files ") {
			failedFiles = vitestFilesFailed.MatchString(trimmed)
		}
	}
	return load && noTests && failedFiles
}

// redGreen sets aside, in clone, the paths aside, runs test alone with tmpl, which must fail, brings the
// agent's work, tree, back and runs test again, which must pass.
func redGreen(clone, tree string, aside []string, test, tmpl string, limit time.Duration) (red, green try, err error) {
	line, err := testLine(clone, tmpl, test)
	if err != nil {
		var parseErr goTestParseError
		if errors.As(err, &parseErr) {
			return red, green, fmt.Errorf("red did not run: parse/compile error: %s fails before any test runs", test)
		}
		return red, green, err
	}
	red, green, err = trial(clone, tree, aside, line, line, test, strings.Contains(tmpl, "{tests}"), func(red try) error {
		if redDidNotRun(red.output) {
			return fmt.Errorf("red did not run: parse/compile error: %s fails before any test runs", test)
		}
		return nil
	}, limit)
	switch {
	case err != nil:
		return red, green, err
	case red.err == nil:
		return red, green, fmt.Errorf("%s passes without the fix", test)
	}
	return red, green, wrap(green.err, test+" fails with the fix")
}

// onBase runs test, after a fix's red and green, twice more with the files the fix adds kept and each old file
// it changes, of the paths aside, rebuilt from its diff: first the base with every added line in place, before
// the base lines it replaces, then the added lines alone. A test that passes on both depends on nothing the
// base had. ponytail: added lines that cannot run alone, as in a compiled language, fail the second and let
// such a test pass.
func onBase(clone, tree string, aside []string, test, tmpl string, limit time.Duration) error {
	line, err := testLine(clone, tmpl, test)
	if err != nil {
		return err
	}
	both, alone := map[string]string{}, map[string]string{}
	for _, p := range aside {
		out, err := git(clone, "--literal-pathspecs", "diff", "--cached", "--no-color", "--no-ext-diff",
			"--no-textconv", "--diff-filter=M", "-U2147483647", "HEAD", "--", p)
		if err != nil {
			return err
		}
		_, hunk, ok := strings.Cut(out, "\n@@")
		if !ok {
			continue // deleted, binary or only its mode changed
		}
		var b, a, cut strings.Builder
		for _, l := range strings.Split(hunk, "\n")[1:] {
			switch {
			case strings.HasPrefix(l, "+"):
				b.WriteString(l[1:] + "\n")
				a.WriteString(l[1:] + "\n")
			case strings.HasPrefix(l, "-"):
				cut.WriteString(l[1:] + "\n")
			case strings.HasPrefix(l, " "):
				b.WriteString(cut.String() + l[1:] + "\n")
				cut.Reset()
			}
		}
		both[p], alone[p] = b.String()+cut.String(), a.String()
	}
	if len(both) == 0 {
		return nil
	}
	for _, files := range []map[string]string{both, alone} {
		for p, s := range files {
			if err := os.WriteFile(filepath.Join(clone, p), []byte(s), 0o600); err != nil {
				return err
			}
		}
		if red, _, err := trial(clone, tree, nil, line, "", "", false, nil, limit); err != nil || red.err != nil {
			return err
		}
	}
	return fmt.Errorf("%s depends on nothing the base had: it passes on the old files with the fix's added "+
		"lines put in the base, and with only those lines", test)
}

// trial runs base with the paths aside as they are at HEAD, brings the agent's work, tree, back and runs
// after, "" for nothing, and returns how each ended, zero when not run. A hang in either, or a clone that is no
// longer tree, is err: the verdict is void. After each run, every file git ignores is back as keep left them,
// changed or deleted ones too, so the green cannot pass on what the red left there.
func trial(clone, tree string, aside []string, base, after, test string, usesTests bool,
	checkRed func(try) error, limit time.Duration) (red, green try, err error) {
	defer func() { err = errors.Join(err, reset(clone)) }()
	if len(aside) > 0 {
		args := append([]string{"--literal-pathspecs", "restore", "--source=HEAD", "--staged", "--worktree", "--"},
			aside...)
		if _, err := git(clone, args...); err != nil {
			return red, green, err
		}
	}
	red = attemptWithTimeout(clone, base, limit)
	classifyTestRun(&red, test, usesTests)
	if checkRed != nil {
		if err := checkRed(red); err != nil {
			return red, green, err
		}
	}
	if err := restoreTrialTree(clone, tree); err != nil {
		return red, green, err
	}
	if err := trialTimeout(red, green, false); err != nil {
		return red, green, red.err
	}
	if after != "" {
		green = attemptWithTimeout(clone, after, limit)
		if err := trialTimeout(red, green, true); err != nil {
			return red, green, err
		}
		classifyTestRun(&green, test, usesTests)
	}
	if _, err := git(clone, "add", "-A"); err != nil {
		return red, green, err
	}
	if now, err := git(clone, "write-tree"); err != nil || now != tree {
		return red, green, errors.Join(err, errVoid)
	}
	return red, green, nil
}

func classifyTestRun(result *try, test string, usesTests bool) {
	if usesTests && strings.Contains(result.Tail, "no tests to run") {
		result.err = fmt.Errorf("the test line ran no test of %s: check its build tags against test_file", test)
	}
}

func trialTimeout(red, green try, checkGreen bool) error {
	if checkGreen && errors.Is(green.err, errTimeout) {
		return green.err
	}
	if !checkGreen && errors.Is(red.err, errTimeout) {
		return red.err
	}
	return nil
}

func restoreTrialTree(clone, tree string) error {
	if _, err := git(clone, "restore", "--source="+tree, "--staged", "--worktree", "--", "."); err != nil {
		return err
	}
	return reset(clone)
}

// try is how a trial's command line ended: its exit code, -1 when it did not exit, its last 60 lines of output
// and its span.
type try struct {
	Command string `json:"command"`
	Exit    int    `json:"exit_code"`
	Tail    string `json:"tail"`
	span
	err    error
	output string
}

// attempt runs line in clone, as sh does.
func attempt(clone, line string) try {
	return attemptWithTimeout(clone, line, timeout)
}

func attemptWithTimeout(clone, line string, limit time.Duration) try {
	var tail, output string
	var err error
	s := timed(func() { tail, output, err = shOutput(clone, line, limit) })
	t := try{Command: line, Tail: tail, span: s, err: err, output: output}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		t.Exit = exit.ExitCode()
	} else if err != nil {
		t.Exit = -1
	}
	return t
}

// every runs git in clone on .git/atm-files, a repository of its own that keeps every file of the working tree,
// the ones git ignores too, which the clone's write-tree never sees. It reads big files, every one above a
// byte, in chunks: macOS kills git for mapping a binary that ran and whose signature, like slopslint's, is bad.
func every(clone string, args ...string) (string, error) {
	return git(clone, append([]string{"-c", "core.bigFileThreshold=1",
		"--git-dir=.git/atm-files", "--work-tree=."}, args...)...)
}

// keep is the tree of clone's index, and records every file in clone, ignored or not, for reset. ponytail: a
// copy of each, node_modules too; links or reflinks if a large one makes it slow.
func keep(clone string) (string, error) {
	tree, err := git(clone, "write-tree")
	if err == nil {
		_, err = whole(clone)
	}
	return tree, err
}

// whole records every file in clone, ignored or not, as reset knows them, byte for byte: no line ending is
// converted, whatever core.autocrlf or .gitattributes say. It is their tree.
func whole(clone string) (string, error) {
	err := store(clone, "atm-files")
	if err == nil {
		_, err = every(clone, "add", "-A", "-f", ".")
	}
	if err != nil {
		return "", err
	}
	return every(clone, "write-tree")
}

// store makes .git/name in clone a bare repository that converts no line ending of the files it records.
func store(clone, name string) error {
	info := filepath.Join(clone, ".git", name, "info")
	_, err := git(clone, "init", "-q", "--bare", ".git/"+name)
	if err == nil {
		err = os.MkdirAll(info, 0o700)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(info, "attributes"), []byte("* -text\n"), 0o600)
	}
	return err
}

// reset brings clone's working tree back to what keep recorded, byte for byte, new files gone, the ignored ones
// too. An ignored file a run changed or deleted, a runner's cache say, is put back, not void: the next run cannot
// see it. A file of the index is the clone's own git's to check, which may rewrite its line endings.
func reset(clone string) error {
	_, err := every(clone, "clean", "-fdqx")
	if err == nil {
		_, err = every(clone, "checkout", "--", ".")
	}
	return err
}

var errVoid = errors.New("the clone changed during verification: the verdict is void")

type goTestParseError struct{ error }

func (e goTestParseError) Unwrap() error { return e.error }

// testLine is test_file, tmpl, for test, a path in clone. {tests} is ^(TestA|TestB)$, the Go tests test defines.
func testLine(clone, tmpl, test string) (string, error) {
	switch {
	case tmpl == "":
		return "", errors.New("test_file is empty in .atm.yaml: this task type runs its test alone")
	case test == "" || !filepath.IsLocal(test):
		return "", fmt.Errorf("the report's test_file %q is not a path in the clone", test)
	}
	var tests []string
	if strings.Contains(tmpl, "{tests}") {
		f, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(clone, filepath.FromSlash(test)), nil, 0)
		if f == nil {
			var syntaxErrors scanner.ErrorList
			if errors.As(parseErr, &syntaxErrors) {
				return "", goTestParseError{parseErr}
			}
			return "", parseErr
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !isGoTest(fn) {
				continue
			}
			tests = append(tests, fn.Name.Name)
		}
		if len(tests) == 0 {
			if parseErr == nil {
				return "", fmt.Errorf("the report's test file %s defines no Go tests", test)
			}
			tests = []string{"."}
		}
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	return strings.NewReplacer("{file}", quote(test), "{dir}", quote("./"+path.Dir(test)),
		"{tests}", quote("^("+strings.Join(tests, "|")+")$")).Replace(tmpl), nil
}

func isGoTest(fn *ast.FuncDecl) bool {
	name := fn.Name.Name
	if name == "TestMain" || !strings.HasPrefix(name, "Test") {
		return false
	}
	if suffix := strings.TrimPrefix(name, "Test"); suffix != "" {
		r, _ := utf8.DecodeRuneInString(suffix)
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

// first is the first path that matches one of globs, when want, or matches none, when not; "" when none is.
func first(paths, globs []string, want bool) string {
	for _, p := range paths {
		if slices.ContainsFunc(globs, func(g string) bool { return match(g, p) }) == want {
			return p
		}
	}
	return ""
}

// glob turns a glob into a regular expression: ** spans directories, * and ? do not. ponytail: no character
// classes; path.Match per segment is the upgrade.
var glob = strings.NewReplacer(`\*\*/`, `(.*/)?`, `\*\*`, `.*`, `\*`, `[^/]*`, `\?`, `[^/]`)

// match says whether the slash-separated path p matches the glob g.
func match(g, p string) bool {
	ok, _ := regexp.MatchString("^"+glob.Replace(regexp.QuoteMeta(g))+"$", p)
	return ok
}

func wrap(err error, msg string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", msg, err)
}

var errTimeout = errors.New("timed out")

// sh runs line with sh -c in dir, env added to its environment, in its own process group, killed past timeout.
// tail is the last 60 lines of its output, which its error carries too.
func sh(dir, line string, env ...string) (tail string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return tee(ctx, nil, io.Discard, dir, line, env...)
}

func shOutput(dir, line string, limit time.Duration, env ...string) (tail, output string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	return teeRun(ctx, nil, io.Discard, dir, line, true, limit, env...)
}

// terminal is the run's screen when a terminal can be attached to it: run runs cmd on the attached one, if
// one is, and says whether it did.
type terminal interface {
	run(cmd *exec.Cmd) (bool, error)
}

// tee is sh killed when ctx ends rather than past timeout, its output copied to w as it comes and, a line at a
// time, to the screen of the run in dir. It runs on t's terminal when one is attached.
func tee(ctx context.Context, t terminal, w io.Writer, dir, line string, env ...string) (tail string, err error) {
	tail, _, err = teeRun(ctx, t, w, dir, line, false, timeout, env...)
	return tail, err
}

func teeRun(ctx context.Context, t terminal, w io.Writer, dir, line string, capture bool,
	limit time.Duration, env ...string) (tail, output string, err error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	var out bytes.Buffer
	live := liveIn(dir)
	log := &lineWriter{fn: func(l string) { live(map[string]any{"output": l}) }}
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, append(os.Environ(), env...), time.Second
	cmd.Stdout = io.MultiWriter(&out, w, log)
	cmd.Stderr = cmd.Stdout
	attach, closeGroup, err := ownGroup(cmd)
	if err != nil {
		return "", "", err
	}
	defer closeGroup()
	ran := false
	if t != nil {
		ran, err = t.run(cmd)
	}
	if !ran {
		if err = cmd.Start(); err == nil {
			if err = attach(); err != nil {
				_ = cmd.Cancel()
				err = errors.Join(err, cmd.Wait())
			} else {
				err = cmd.Wait()
			}
		}
	}
	log.flush()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("%w after %s", errTimeout, Human(limit))
	case ctx.Err() != nil:
		err = errors.New("interrupted")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if tail = strings.Join(lines[max(0, len(lines)-60):], "\n"); err != nil && tail != "" {
		err = fmt.Errorf("%w\n%s", err, tail)
	}
	if capture {
		output = out.String()
	}
	return tail, output, err
}
