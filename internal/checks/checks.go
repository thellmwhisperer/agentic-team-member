// Package checks is the Checks step: the configured commands on the completed work, with the clone and test
// safeguards around them.
package checks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// Tools is what Checks uses of the run that calls it, which the run's other steps share. C is the record of one
// command, the one the run's report keeps.
type Tools[C any] struct {
	Git      func(dir string, args ...string) (string, error)
	Moved    func(clone, want, who string) error // an error when HEAD in clone is not want: who made commits
	Unmade   func(clone string) error            // removes the agent's ignored content and puts back what was
	ReadTest func(clone, test string) ([]byte, error)
	// Command runs line in clone as the screen's row name, env added to its environment, its error named name.
	Command func(name, line string, env ...string) (C, error)
}

// Run runs the configured commands on the completed work, preserving clone and test safeguards. issue, when not
// nil, is the issue's text: a changed test file must keep its tests but those it names (decision 8087).
func Run[C any](clone, runDir, sha, test string, issue *string, missingReport bool, c config.Config,
	t Tools[C]) (result map[string]any, err error) {
	result = map[string]any{}
	cover, err := coverageEnv(runDir, c.E2E)
	var ran []C
	run := func(name, line string, env ...string) error {
		if line == "" {
			return nil
		}
		cmd, err := t.Command(name, line, env...)
		ran = append(ran, cmd)
		result["commands"] = ran
		return err
	}
	if err == nil {
		err = t.Moved(clone, sha, "the agent")
	}
	if err == nil {
		var restore func() error
		restore, err = hideATM(clone, runDir)
		if restore != nil {
			defer func() { err = errors.Join(err, restore()) }()
		}
		if err == nil {
			err = t.Unmade(clone)
		}
	}
	if err == nil {
		err = run("install", c.Install)
	}
	if err != nil {
		return result, err
	}
	if err = checkChanges(t.Git, clone, issue, c.TestPatterns, missingReport); err == nil && test != "" {
		_, err = t.ReadTest(clone, test)
	}
	if err != nil {
		return result, err
	}
	for _, cmd := range [][2]string{{"test", c.Test}, {"e2e", c.E2E}, {"typecheck", c.Typecheck}, {"lint", c.Lint}} {
		if err := run(cmd[0], cmd[1], coverFor(cover, cmd[0])...); err != nil {
			return result, err
		}
	}
	result["commands"] = ran
	return result, t.Moved(clone, sha, "the commands")
}

// checkChanges rejects empty work when there is no report and, when issue is not nil, a changed test file that
// loses tests.
func checkChanges(git func(dir string, args ...string) (string, error), clone string, issue *string,
	tests []string, missingReport bool) error {
	_, err := git(clone, "add", "-A")
	if err == nil {
		var out string
		out, err = git(clone, "diff", "--cached", "--name-only", "--no-renames", "-z", "HEAD")
		if err == nil && missingReport && out == "" {
			err = errors.New("no change to prove")
		}
		if err == nil && issue != nil && len(tests) > 0 {
			err = lost(git, clone, *issue, tests)
		}
		_, resetErr := git(clone, "reset", "-q")
		err = errors.Join(err, resetErr)
	}
	return err
}

// testOrSubtest is a test function or a t.Run subtest, its name the first or second group when written out.
var testOrSubtest = regexp.MustCompile(`(?m)^func (Test\w*)|t\.Run\((?:"([^"]*)")?`)

// lost is an error naming the first staged test file, matched by tests, that has fewer tests than in HEAD, at the
// path git's rename detection moves it to, those issue names left out.
func lost(git func(dir string, args ...string) (string, error), clone, issue string, tests []string) error {
	spec := []string{"diff", "--cached", "--name-status", "-M", "-z", "HEAD", "--"}
	for _, g := range tests {
		spec = append(spec, ":(glob)"+g)
	}
	out, err := git(clone, spec...)
	count := func(rev string) int {
		text, _ := git(clone, "show", rev) // a file that is not there has none
		n := 0
		for _, m := range testOrSubtest.FindAllStringSubmatch(text, -1) {
			// ponytail: a name inside a longer one of the issue counts as named; match words if that bites.
			if name := m[1] + m[2]; name == "" || !strings.Contains(issue, name) {
				n++
			}
		}
		return n
	}
	for f := strings.Split(out, "\x00"); err == nil && len(f) > 1; f = f[2:] {
		was, is := f[1], f[1]
		if f[0][0] == 'R' {
			is, f = f[2], f[1:]
		}
		if before, after := count("HEAD:"+was), count(":"+is); after < before {
			err = fmt.Errorf("the test file %s loses tests: %d before, %d after", was, before, after)
		}
	}
	return err
}

// coverageEnv is one fresh GOCOVERDIR for a Checks that declares e2e, of no use when it cannot be made.
// coverFor gives it to test and e2e only: install, typecheck and lint must not write those counters.
func coverageEnv(runDir, e2e string) ([]string, error) {
	if e2e == "" {
		return nil, nil
	}
	dir, err := os.MkdirTemp(runDir, "coverage-")
	return []string{"GOCOVERDIR=" + dir}, err
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
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return func() error { return os.RemoveAll(path) }, nil
	}
	if err == nil {
		if _, err = os.Lstat(stash); err == nil {
			err = fmt.Errorf("checks artefact path already exists: %s", stash)
		} else if errors.Is(err, os.ErrNotExist) {
			err = os.Rename(path, stash)
		}
	}
	if err != nil {
		return nil, err
	}
	return func() error {
		err := os.RemoveAll(path)
		if err == nil {
			err = os.Rename(stash, path)
		}
		return err
	}, nil
}
