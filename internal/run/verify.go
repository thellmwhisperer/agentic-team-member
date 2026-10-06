package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// verdict is a check's outcome and why.
type verdict struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// testRun is one run of the test file alone.
type testRun struct {
	ran, passed, timedOut bool
	output                string
}

// runTest runs argv in clone, its process group killed at the timeout.
func runTest(clone string, argv []string, timeout time.Duration) testRun {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = clone, &out, &out
	cmd.WaitDelay = 5 * time.Second
	afterStart, closeGroup, err := inOwnGroup(cmd)
	if err == nil {
		defer closeGroup()
		err = cmd.Start()
		if err == nil {
			err = afterStart()
			if err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			} else {
				err = cmd.Wait()
			}
		}
	}
	var exit *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return testRun{timedOut: true, output: fmt.Sprintf("TIMEOUT: the test ran past %s.", timeout)}
	case err != nil && !errors.As(err, &exit):
		return testRun{output: err.Error()}
	}
	return testRun{ran: true, passed: err == nil, output: out.String()}
}

// park moves every change, tracked or not, into a dangling stash commit and leaves the clone at HEAD;
// "" when there was nothing to park. Never refs/stash: that stack is shared, and runs swapped work on it.
func park(clone string) string {
	_, _ = git(clone, "add", "-A")
	// The stash commit is never pushed: an identity of its own spares a machine that has none configured.
	sha, _ := git(clone, "-c", "user.name=atm", "-c", "user.email=atm@localhost", "stash", "create")
	if sha != "" {
		_, _ = git(clone, "reset", "--hard", "-q", "HEAD")
	} else {
		_, _ = git(clone, "reset", "-q")
	}
	return sha
}

// unpark brings back what park parked, unstaged as the agent left it.
func unpark(clone, sha string) error {
	if sha == "" {
		return nil
	}
	_, err := git(clone, "stash", "apply", "--index", "-q", sha)
	_, _ = git(clone, "reset", "-q")
	return err
}

// fingerprint hashes the content of every path that differs from HEAD, tracked or not; staging is not a change.
func fingerprint(clone string) string {
	h := sha256.New()
	paths := append(gitZ(clone, "diff", "HEAD", "--name-only", "--no-renames"),
		gitZ(clone, "ls-files", "--others", "--exclude-standard")...)
	slices.Sort(paths)
	for _, rel := range slices.Compact(paths) {
		full := filepath.Join(clone, rel)
		h.Write([]byte(rel + "\x00"))
		info, err := os.Lstat(full)
		switch {
		case err != nil:
			h.Write([]byte("\x00deleted\x00"))
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(full)
			h.Write([]byte("symlink\x00" + target))
		default:
			b, _ := os.ReadFile(full)
			h.Write([]byte(strconv.Itoa(int(info.Mode().Perm()&0o111)) + "\x00"))
			h.Write(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func gitZ(dir string, args ...string) []string {
	out, _ := git(dir, append(args, "-z")...)
	return slices.DeleteFunc(strings.Split(out, "\x00"), func(s string) bool { return s == "" })
}

// redGreen proves the fix: the test file alone fails on the clone without the agent's changes and passes
// with them. The verdict is void if the clone is not what the agent left when it is done.
func redGreen(clone, rel string, argv []string, timeout time.Duration, log *runLog) verdict {
	before := fingerprint(clone)
	full := filepath.Join(clone, rel)
	test, err := os.ReadFile(full)
	if err != nil {
		return verdict{Message: "the test file is gone: " + err.Error()}
	}
	info, _ := os.Stat(full)
	sha := park(clone)
	var red, green testRun
	log.step("Without the fix", func() bool {
		if err = os.MkdirAll(filepath.Dir(full), 0o755); err == nil {
			err = os.WriteFile(full, test, info.Mode().Perm())
		}
		if err == nil {
			red = runTest(clone, argv, timeout)
		}
		// The test goes and HEAD's files come back before the agent's changes do.
		_ = os.Remove(full)
		_, resetErr := git(clone, "reset", "--hard", "-q", "HEAD")
		err = errors.Join(err, resetErr, unpark(clone, sha))
		return red.ran && !red.passed && err == nil
	})
	if err != nil {
		return verdict{Message: "could not restore the agent's changes after the red run: " + err.Error()}
	}
	log.step("With the fix", func() bool {
		green = runTest(clone, argv, timeout)
		return green.passed
	})
	log.log("verify", obj{"test_file": rel, "argv": argv, "red_passed": red.passed, "green_passed": green.passed,
		"red_output": red.output, "green_output": green.output})
	if fingerprint(clone) != before {
		return verdict{Message: "WORKTREE CHANGED DURING VERIFICATION: the red/green verdict is void"}
	}
	return judge(rel, red, green)
}

var (
	pytestMissing = regexp.MustCompile(`no module named\s+['"]?pytest['"]?(?:$|[^a-z0-9_])`)
	binaryMissing = regexp.MustCompile(`no such file or directory:\s*['"]?(python3|python|pytest|bun|node)['"]?` +
		`(?:$|[^a-z0-9_./-])`)
	// A red that fails because a module is absent proves nothing about the bug.
	importError = regexp.MustCompile(`(?im)^(?:[ \t]*(?:E[ \t]+)?` +
		`(?:ModuleNotFoundError|ImportError|Error(?: \[ERR_MODULE_NOT_FOUND\])?):[ \t]*` +
		`(?:(no module named|cannot find (?:module|package)|failed to resolve import) ['"]([^'"]+)['"]|` +
		`cannot import name ['"]([^'"]+)['"] from ['"]([^'"]+)['"])|` +
		`(cannot find module) ['"]([^'"]+)['"] from ['"][^'"]+['"]|` +
		`(failed to resolve import) ['"]([^'"]+)['"] from ['"][^'"]+['"])`)
	capturedHeader = regexp.MustCompile(`^-+ Captured `)
	sectionRule    = regexp.MustCompile(`^[-=_]{3,}`)
)

// infraError is why a test run says nothing about the code: the runner itself is missing.
func infraError(output string) string {
	lowered := strings.ToLower(output)
	if pytestMissing.MatchString(lowered) || strings.Contains(lowered, "pytest: command not found") {
		return "pytest is unavailable in the verification environment"
	}
	if m := binaryMissing.FindStringSubmatch(lowered); m != nil {
		return strings.NewReplacer("python3", "pytest", "python", "pytest").Replace(m[1]) +
			" is unavailable in the verification environment"
	}
	for _, tool := range []string{"bun", "node"} {
		if strings.Contains(lowered, tool+": command not found") {
			return tool + " is unavailable in the verification environment"
		}
	}
	return ""
}

// missingModule is the module a red failed to import, outside pytest's captured output sections.
// ponytail: only pytest's captured sections are dropped; a Jest console.log echoing an import error at line
// start still counts. Strip those blocks too if it ever bites.
func missingModule(output string) string {
	var kept strings.Builder
	captured := false
	for line := range strings.Lines(output) {
		if captured && sectionRule.MatchString(line) {
			captured = false
		}
		captured = captured || capturedHeader.MatchString(line)
		if !captured {
			kept.WriteString(line)
		}
	}
	m := importError.FindStringSubmatch(kept.String())
	switch {
	case m == nil:
		return ""
	case m[1] != "":
		return fmt.Sprintf("%s '%s'", strings.ToLower(m[1]), m[2])
	case m[3] != "":
		return fmt.Sprintf("cannot import name '%s' from '%s'", m[3], m[4])
	case m[5] != "":
		return fmt.Sprintf("cannot find module '%s'", m[6])
	}
	return fmt.Sprintf("failed to resolve import '%s'", m[8])
}

// scaffoldMarkers: a red that fails on a missing test-only seam (__setXForTests) never ran the real code.
var scaffoldMarkers = []string{"not a function", "is undefined", "is not defined", "cannot import",
	"does not provide an export", "has no exported member"}

func judge(rel string, red, green testRun) verdict {
	reject := func(format string, args ...any) verdict {
		return verdict{Message: "REJECTED: " + fmt.Sprintf(format, args...)}
	}
	lowered := strings.ToLower(red.output)
	switch {
	case infraError(red.output) != "":
		return reject("Your red phase for %s failed because the verification environment is broken, not because "+
			"the bug was reproduced. %s. Error: %s", rel, infraError(red.output), head(red.output))
	case !red.ran:
		return reject("Your test (%s) did not run WITHOUT your source fix. Error: %s", rel, head(red.output))
	case red.passed:
		return reject("Your test (%s) passes even WITHOUT your source fix. It does not test the real code: it "+
			"probably uses local stubs instead of importing from the source.", rel)
	case (strings.Contains(lowered, "__set") || strings.Contains(lowered, "fortests")) &&
		slices.ContainsFunc(scaffoldMarkers, func(m string) bool { return strings.Contains(lowered, m) }):
		return reject("Your red phase for %s failed because the test scaffold is incomplete, not because the bug "+
			"was reproduced. Error: %s", rel, head(red.output))
	case !green.passed && infraError(green.output) != "":
		return reject("Your green phase for %s failed because the verification environment is broken. %s. Error: %s",
			rel, infraError(green.output), head(green.output))
	case !green.passed:
		return reject("Your test (%s) fails even WITH your source fix. Error: %s", rel, head(green.output))
	case missingModule(red.output) != "":
		return verdict{Message: fmt.Sprintf("INVALID RED: without the fix the test fails on a missing module (%s), "+
			"not on behavior", missingModule(red.output))}
	}
	return verdict{OK: true, Message: "VERIFIED: Test fails without fix, passes with fix. Real red-green."}
}

func head(output string) string {
	if len(output) > 300 {
		return output[:300]
	}
	return output
}
