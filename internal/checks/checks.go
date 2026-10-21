// Package checks is the Checks step: the configured commands on the completed work, with the clone and test
// safeguards around them.
package checks

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// Run runs the configured commands on the completed work, preserving clone and test safeguards. A nil issue
// disables the test-loss check during ponytail reproof; later Checks uses cuts to retain only accepted reductions.
func Run[C any](clone, runDir, sha, base, test string, issue *string, missingReport bool, c config.Config,
	t Tools[C], cuts ...string) (result map[string]any, err error) {
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
	if err = checkChanges(t.Git, clone, base, issue, c.TestPatterns, missingReport, cuts); err == nil && test != "" {
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
func checkChanges(git func(dir string, args ...string) (string, error), clone, base string, issue *string,
	tests []string, missingReport bool, cuts []string) error {
	_, err := git(clone, "add", "-A")
	if err == nil {
		var out string
		out, err = git(clone, "diff", "--cached", "--name-only", "--no-renames", "-z", "HEAD")
		if err == nil && missingReport && out == "" {
			err = errors.New("no change to prove")
		}
		if err == nil && issue != nil && len(tests) > 0 {
			err = lost(git, clone, base, *issue, tests, cuts)
		}
		_, resetErr := git(clone, "reset", "-q")
		err = errors.Join(err, resetErr)
	}
	return err
}

func lost(git func(dir string, args ...string) (string, error), clone, base, issue string, tests, cuts []string) error {
	removed := map[string]bool{}
	for text := issue; ; {
		m := removal.FindStringSubmatchIndex(text)
		if m == nil {
			break
		}
		for i := 2; i < 8; i += 2 {
			if m[i] >= 0 {
				removed[text[m[i]:m[i+1]]] = true
			}
		}
		text = text[m[8]:]
	}
	return lostAcrossCuts(git, clone, base, tests, removed, cuts)
}

type testInventory struct {
	from, to      string
	before, after int
}

func lostAcrossCuts(git func(dir string, args ...string) (string, error), clone, base string,
	tests []string, removed map[string]bool, cuts []string) error {
	var versions []string
	for _, cut := range cuts {
		versions = append(versions, cut+"^", cut)
	}
	tracked := map[string]*testInventory{}
	var required []*testInventory
	previous := base
	for n, rev := range append(versions, "") {
		changes, err := inventories(git, clone, previous, rev, tests, removed)
		if err != nil {
			return err
		}
		next := maps.Clone(tracked)
		for _, change := range changes {
			delete(next, change.from)
		}
		for _, change := range changes {
			prior := tracked[change.from]
			if prior == nil {
				prior = &testInventory{from: change.from, before: change.before}
				required = append(required, prior)
			}
			if n%2 == 1 {
				prior.before -= max(0, min(prior.before, change.before)-change.after)
			}
			prior.after = change.after
			next[change.to] = prior
		}
		tracked, previous = next, rev
	}
	for _, count := range required {
		if count.after < count.before {
			return fmt.Errorf("the test file %s loses tests: %d before, %d after", count.from, count.before, count.after)
		}
	}
	return nil
}

func inventories(git func(dir string, args ...string) (string, error), clone, base, rev string,
	tests []string, removed map[string]bool) ([]testInventory, error) {
	arg := rev
	if arg == "" {
		arg = "--cached"
	}
	spec := []string{"diff", "--name-status", "-M", "-z", base, arg, "--"}
	for _, g := range tests {
		spec = append(spec, ":(glob)"+g)
	}
	out, err := git(clone, spec...)
	var counts []testInventory
	count := func(rev, path string, absent bool) (int, error) {
		if absent || filepath.Ext(path) != ".go" {
			return 0, nil
		}
		text, err := git(clone, "show", rev+":"+path)
		n, parseErr := countTests(text, removed)
		return n, errors.Join(err, parseErr)
	}
	for f := strings.Split(out, "\x00"); err == nil && len(f) > 1; f = f[2:] {
		was, is := f[1], f[1]
		if f[0][0] == 'R' {
			is, f = f[2], f[1:]
		}
		before, beforeErr := count(base, was, f[0] == "A")
		after, afterErr := count(rev, is, f[0] == "D")
		if err = errors.Join(beforeErr, afterErr); err != nil {
			return counts, fmt.Errorf("count tests in %s: %w", was, err)
		}
		counts = append(counts, testInventory{was, is, before, after})
	}
	return counts, err
}

var removal = regexp.MustCompile("(?im)(?:^[ \t]*(?:[-*][ \t]+)?|[.!?][ \t]+|[ \t]+and[ \t]+)" +
	"(?:remove|delete)[ \t]+(?:the[ \t]+)?(?:(?:test|subtest)[ \t]+)?" +
	"(?:`([^`]+)`|\"([^\"]+)\"|([[:alnum:]_/-]+))([.!](?:[ \t]|$)|[ \t]*$)")

func testIdentity(node ast.Node) (string, ast.Node) {
	switch v := node.(type) {
	case *ast.FuncDecl:
		if v.Recv == nil && v.Body != nil && strings.HasPrefix(v.Name.Name, "Test") {
			return v.Name.Name, v.Body
		}
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" || len(v.Args) != 2 {
			break
		}
		receiver, ok := sel.X.(*ast.Ident)
		if !ok || receiver.Name != "t" {
			break
		}
		name := ""
		if literal, ok := v.Args[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
			name, _ = strconv.Unquote(literal.Value)
		}
		return name, v.Args[1]
	}
	return "", nil
}

func countTests(text string, removed map[string]bool) (int, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "", text, 0)
	if err != nil {
		return 0, err
	}
	n := 0
	var walk func(ast.Node, string)
	walk = func(root ast.Node, parent string) {
		ast.Inspect(root, func(node ast.Node) bool {
			name, body := testIdentity(node)
			if body == nil {
				return true
			}
			identity := strings.TrimPrefix(parent+"/"+name, "/")
			if name != "" && (removed[name] || removed[identity]) {
				return false
			}
			n++
			walk(body, identity)
			return false
		})
	}
	walk(f, "")
	return n, nil
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
