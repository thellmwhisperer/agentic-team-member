package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

	chk "github.com/thellmwhisperer/agentic-team-member/internal/checks"
	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// checks runs Checks on the completed work in clone with what it shares with the run's other steps, issue as
// chk.Run takes it.
func checks(clone, runDir, sha, base, test string, issue *string, missingReport bool, c config.Config,
	cuts ...string) (map[string]any, error) {
	live := liveIn(clone)
	return chk.Run(clone, runDir, sha, base, test, issue, missingReport, c, chk.Tools[cmdResult]{
		Git: git, Moved: moved, Unmade: unmade, ReadTest: readTest,
		Command: func(name, line string, env ...string) (cmdResult, error) {
			var tail string
			s, err := row(live, name, func() (err error) {
				tail, _, err = teeRun(context.Background(), nil, io.Discard, clone, line, 0, env...)
				return err
			})
			return cmdResult{name, line, outcome(err), tail, s}, wrap(err, name)
		}}, cuts...)
}

// readTest confines every report and saved-history test read to the clone, including symlinks.
func readTest(clone, test string) ([]byte, error) {
	if !filepath.IsLocal(test) {
		return nil, fmt.Errorf("the report's test_file %q is not a path in the clone", test)
	}
	file, err := os.OpenInRoot(clone, test)
	if err != nil {
		return nil, fmt.Errorf("the report's test_file %q cannot be opened inside the clone: %w", test, err)
	}
	defer func() { _ = file.Close() }()
	b, err := io.ReadAll(file)
	return b, wrap(err, fmt.Sprintf("the report's test_file %q cannot be read inside the clone", test))
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

// unmade removes agent-created ignored content using before's paths, then puts back, byte for byte, each
// ignored file before recorded that the agent changed or deleted, those of an ignored nested repository too: a
// package manager's own state, which would tell install the agent's dependencies are there.
func unmade(clone string) error {
	err := prunes(clone)
	if err == nil {
		var changed string
		changed, err = every(clone, "diff", "--name-only", "--no-renames", "-z", "--diff-filter=DMT",
			"refs/atm-before", "--", ".", ":(exclude).atm") // hideATM's
		if err == nil && changed != "" {
			var ignored string
			ignored, err = commandIn(strings.NewReader(changed), clone, "git", "check-ignore", "-z", "--stdin")
			if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 1 {
				err = nil // none is ignored
			} else if err == nil {
				_, err = commandIn(strings.NewReader(ignored), clone, "git", "--literal-pathspecs",
					"--git-dir=.git/atm-files", "--work-tree=.", "checkout", "refs/atm-before",
					"--pathspec-from-file=-", "--pathspec-file-nul")
			}
		}
	}
	if err == nil {
		err = putBackNested(clone)
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
		_, err = commandIn(strings.NewReader(changed), clone, "git", "--literal-pathspecs",
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

// prunes removes agent-created ignored content using before's paths.
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

// every runs git in clone on .git/atm-files, a repository of its own that keeps every file of the working tree,
// the ones git ignores too, which the clone's write-tree never sees. It reads big files, every one above a
// byte, in chunks: macOS kills git for mapping a binary that ran and whose signature, like slopslint's, is bad.
func every(clone string, args ...string) (string, error) {
	return git(clone, append([]string{"-c", "core.bigFileThreshold=1",
		"--git-dir=.git/atm-files", "--work-tree=."}, args...)...)
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
	tail, _, err = shOutput(dir, line, timeout, env...)
	return tail, err
}

func shOutput(dir, line string, limit time.Duration, env ...string) (tail, output string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	return teeRun(ctx, nil, io.Discard, dir, line, limit, env...)
}

// terminal is the run's screen when a terminal can be attached to it: run runs cmd on the attached one, if
// one is, and says whether it did.
type terminal interface {
	run(cmd *exec.Cmd) (bool, error)
}

// tee is sh killed when ctx ends rather than past timeout, its output copied to w as it comes and, a line at a
// time, to the screen of the run in dir. It runs on t's terminal when one is attached.
func tee(ctx context.Context, t terminal, w io.Writer, dir, line string, env ...string) (tail string, err error) {
	tail, _, err = teeRun(ctx, t, w, dir, line, timeout, env...)
	return tail, err
}

func teeRun(ctx context.Context, t terminal, w io.Writer, dir, line string, limit time.Duration,
	env ...string) (tail, output string, err error) {
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
	return tail, out.String(), err
}
