package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// checks is the verdict on the agent's work in clone, at base commit sha, for a task of type typ whose report
// names test: HEAD has not moved, the type's proof holds, then install, test, typecheck and lint pass, the
// commands no-mistakes runs, and HEAD has not moved still. Install runs again: the agent may have changed
// dependencies. Each proof and command is a row of the screen.
func checks(clone, sha, typ, test string, c config.Config) (map[string]any, error) {
	live := liveIn(clone)
	err := row(live, proofs[typ], func() error {
		err := moved(clone, sha, "the agent")
		if err == nil {
			err = prove(clone, typ, filepath.ToSlash(test), c)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var ran []cmdResult
	for _, cmd := range [][2]string{{"install", c.Install}, {"test", c.Test}, {"typecheck", c.Typecheck},
		{"lint", c.Lint}} {
		if cmd[1] == "" {
			continue
		}
		var tail string
		err := row(live, cmd[0], func() (err error) {
			tail, err = sh(clone, cmd[1])
			return err
		})
		if ran = append(ran, cmdResult{cmd[0], cmd[1], outcome(err), tail}); err != nil {
			return map[string]any{"commands": ran}, fmt.Errorf("%s: %w", cmd[0], err)
		}
	}
	return map[string]any{"commands": ran}, moved(clone, sha, "the commands")
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

// row runs fn as the screen's row name, under the node that runs, when name is not "".
func row(live func(map[string]any), name string, fn func() error) error {
	if name == "" {
		return fn()
	}
	live(map[string]any{"check": name, "state": "started"})
	start := time.Now()
	err := fn()
	live(map[string]any{"check": name, "state": outcome(err), "duration_ms": time.Since(start).Milliseconds()})
	return err
}

// prove runs the proof of task type typ on the agent's work in clone, all of it staged meanwhile.
func prove(clone, typ, test string, c config.Config) (err error) {
	if _, err := git(clone, "add", "-A"); err != nil {
		return err
	}
	defer func() {
		_, e := git(clone, "reset", "-q") // the agent's work back to unstaged
		err = errors.Join(err, e)
	}()
	tree, err := git(clone, "write-tree")
	if err != nil {
		return err
	}
	changed, aside, err := changes(clone, typ, test)
	if err != nil {
		return err
	}
	switch typ {
	case "fix", "feature", "greenfield":
		var added map[string]string
		if typ == "fix" {
			if added, err = additions(clone, aside); err != nil {
				return err
			}
		}
		return redGreen(clone, tree, aside, added, test, c.TestFile)
	case "refactor":
		if p := first(changed, c.TestPatterns, true); p != "" {
			return fmt.Errorf("a refactor changed the test %s", p)
		}
		base, _, err := trial(clone, tree, changed, nil, c.Test, "")
		return errors.Join(err, wrap(base, "the suite fails on the base"))
	case "tests":
		if p := first(changed, slices.Concat(c.TestPatterns, c.DocsPatterns), false); p != "" {
			return fmt.Errorf("a tests task changed the source %s", p)
		}
		line, err := testLine(c.TestFile, test)
		if err != nil {
			return err
		}
		base, after, err := trial(clone, tree, aside, nil, line, line)
		return errors.Join(err, wrap(base, test+" fails on the base"), wrap(after, test+" fails after the change"))
	case "docs":
		if p := first(changed, c.DocsPatterns, false); p != "" {
			return fmt.Errorf("a docs task changed %s, which is not docs", p)
		}
	}
	return nil
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

// additions is each path's base content with every added line inserted at its position in the staged work.
func additions(clone string, paths []string) (map[string]string, error) {
	added := map[string]string{}
	for _, p := range paths {
		diff, err := git(clone, "--literal-pathspecs", "diff", "--cached", "-U0", "--no-renames", "HEAD", "--", p)
		if err != nil {
			return nil, err
		}
		base, err := git(clone, "show", "HEAD:"+p)
		if err != nil {
			return nil, err
		}
		content, ok := additionsAt(base, diff)
		if !ok {
			return nil, fmt.Errorf("cannot reconstruct additions to %s", p)
		}
		if content != base {
			added[p] = content
		}
	}
	return added, nil
}

// additionsAt inserts diff additions at the start of each changed range in base.
func additionsAt(base, diff string) (string, bool) {
	var lines []string
	if base != "" {
		lines = strings.Split(base, "\n")
	}
	trailingNewline := strings.HasSuffix(base, "\n")
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	insertions := map[int][]string{}
	var boundary int
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			var oldStart, oldCount int
			if _, err := fmt.Sscanf(line, "@@ -%d,%d", &oldStart, &oldCount); err != nil {
				if _, err = fmt.Sscanf(line, "@@ -%d", &oldStart); err != nil {
					return "", false
				}
				oldCount = 1
			}
			boundary = oldStart - 1
			if oldCount == 0 {
				boundary = oldStart
			}
			if boundary < 0 || boundary+oldCount > len(lines) {
				return "", false
			}
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case ' ':
			boundary++
		case '-':
		case '+':
			insertions[boundary] = append(insertions[boundary], line[1:])
		case '\\':
		default:
			inHunk = false
		}
		if boundary > len(lines) {
			return "", false
		}
	}
	if len(insertions) == 0 {
		return base, true
	}
	var reconstructed []string
	for i := 0; i <= len(lines); i++ {
		reconstructed = append(reconstructed, insertions[i]...)
		if i < len(lines) {
			reconstructed = append(reconstructed, lines[i])
		}
	}
	result := strings.Join(reconstructed, "\n")
	if trailingNewline || len(reconstructed) > 0 {
		result += "\n"
	}
	return result, true
}

// redGreen sets aside, in clone, the paths aside, runs test alone with tmpl, which must fail, brings the
// agent's work, tree, back and runs test again, which must pass. If the red reports a missing symbol, it also
// checks whether the added lines alone make the test pass on the base.
func redGreen(clone, tree string, aside []string, added map[string]string, test, tmpl string) error {
	line, err := testLine(tmpl, test)
	if err != nil {
		return err
	}
	red, green, err := trial(clone, tree, aside, nil, line, line)
	switch {
	case err != nil:
		return err
	case red == nil:
		return fmt.Errorf("%s passes without the fix", test)
	case len(added) > 0 && missingSymbol(red):
		alone, _, err := trial(clone, tree, aside, added, line, "")
		if err != nil {
			return err
		}
		if alone == nil {
			return fmt.Errorf("%s passes on the base plus only the lines the fix adds to its files, in their added positions: "+
				"its red is a missing addition, not behaviour the base had", test)
		}
	}
	return wrap(green, test+" fails with the fix")
}

// missingSymbol reports whether the red output says the test cannot resolve a referenced symbol.
func missingSymbol(err error) bool {
	if err == nil {
		return false
	}
	out := err.Error()
	return strings.Contains(out, "not found") || strings.Contains(out, "undefined:")
}

// trial runs base with the paths aside as they are at HEAD, then the files over, paths to their content,
// written, brings the agent's work, tree, back and runs after, "" for nothing, and returns how each ended. A
// hang in either, or a clone that is no longer tree, is err: the verdict is void. over must be among aside.
func trial(clone, tree string, aside []string, over map[string]string, base, after string) (red, green, err error) {
	if len(aside) > 0 {
		args := append([]string{"--literal-pathspecs", "restore", "--source=HEAD", "--staged", "--worktree", "--"},
			aside...)
		if _, err := git(clone, args...); err != nil {
			return nil, nil, err
		}
	}
	for p, s := range over {
		if err = os.WriteFile(filepath.Join(clone, p), []byte(s), 0o644); err != nil {
			break
		}
	}
	if err == nil {
		_, red = sh(clone, base)
	}
	if len(aside) > 0 {
		if _, e := git(clone, "restore", "--source="+tree, "--staged", "--worktree", "--", "."); e != nil || err != nil {
			return red, nil, errors.Join(err, e)
		}
	}
	if errors.Is(red, errTimeout) {
		return red, nil, red
	}
	if after != "" {
		if _, green = sh(clone, after); errors.Is(green, errTimeout) {
			return red, green, green
		}
	}
	if _, err := git(clone, "add", "-A"); err != nil {
		return red, green, err
	}
	if now, err := git(clone, "write-tree"); err != nil || now != tree {
		return red, green, errors.Join(err, errors.New("the clone changed during verification: the verdict is void"))
	}
	return red, green, nil
}

// testLine is test_file, tmpl, for test, a path in the clone.
func testLine(tmpl, test string) (string, error) {
	switch {
	case tmpl == "":
		return "", errors.New("test_file is empty in .atm.yaml: this task type runs its test alone")
	case test == "" || !filepath.IsLocal(test):
		return "", fmt.Errorf("the report's test_file %q is not a path in the clone", test)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	return strings.NewReplacer("{file}", quote(test), "{dir}", quote("./"+path.Dir(test))).Replace(tmpl), nil
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

// terminal is the run's screen when a terminal can be attached to it: run runs cmd on the attached one, if
// one is, and says whether it did.
type terminal interface {
	run(cmd *exec.Cmd) (bool, error)
}

// tee is sh killed when ctx ends rather than past timeout, its output copied to w as it comes and, a line at a
// time, to the screen of the run in dir. It runs on t's terminal when one is attached.
func tee(ctx context.Context, t terminal, w io.Writer, dir, line string, env ...string) (tail string, err error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	var out bytes.Buffer
	live := liveIn(dir)
	log := &lineWriter{fn: func(l string) { live(map[string]any{"output": l}) }}
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, append(os.Environ(), env...), time.Second
	cmd.Stdout = io.MultiWriter(&out, w, log)
	cmd.Stderr = cmd.Stdout
	ownGroup(cmd)
	ran := false
	if t != nil {
		ran, err = t.run(cmd)
	}
	if !ran {
		err = cmd.Run()
	}
	log.flush()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("%w after %s", errTimeout, Human(timeout))
	case ctx.Err() != nil:
		err = errors.New("interrupted")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if tail = strings.Join(lines[max(0, len(lines)-60):], "\n"); err != nil && tail != "" {
		err = fmt.Errorf("%w\n%s", err, tail)
	}
	return tail, err
}
