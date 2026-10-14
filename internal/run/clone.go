package run

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// clone sweeps root/.atm/clones, then claims a clone there of root at base's commit, detached, with install
// run in it, whose result it returns when it ran. It returns the clone even when a later step fails, so the
// caller can release it.
//
// Next to each clone, <clone>.lock guards cleanup while the run or an inheriting process holds it.
// <clone>.delivered holds "<branch> <base>": what tells a later run whether the clone is still needed.

var cloneLeases sync.Map

func clone(root, base, install, repo string, live func(map[string]any)) (dir, sha string, ran []cmdResult, err error) {
	if sha, err = git(root, "rev-parse", "--verify", base+"^{commit}"); err != nil {
		return "", "", nil, fmt.Errorf("base ref %q: %w", base, err)
	}
	clones := filepath.Join(root, ".atm", "clones")
	sweep(clones, repo)
	if dir, err = claim(clones, time.Now()); err != nil {
		return "", sha, nil, err
	}
	for _, args := range [][]string{
		{"clone", "-q", "--local", "--no-checkout", root, dir},
		{"-C", dir, "fetch", "-q", root, sha},
		{"-C", dir, "checkout", "-q", "--detach", sha},
	} {
		if _, err = commandFor(dir, "", "git", args...); err != nil {
			return dir, sha, nil, err
		}
	}
	if err = exclude(dir, "/.atm/"); err != nil || install == "" { // ATM's scratch
		return dir, sha, nil, err
	}
	var tail string
	s, err := row(live, "install", func() error {
		tail, err = sh(dir, install)
		return err
	})
	return dir, sha, []cmdResult{{"install", install, outcome(err), tail, s}}, wrap(err, "install")
}

// claim makes a fresh atm-run-<now> directory under root: mkdir either succeeds or the name is taken, so two
// runs started in the same second never share one.
func claim(root string, now time.Time) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(root, "atm-run-"+now.Format("20060102-150405"))
	for n := 1; ; n++ {
		dir := base
		if n > 1 {
			dir = fmt.Sprintf("%s-%d", base, n)
		}
		f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err = lockCloneShared(f); err != nil {
			_ = f.Close()
			_ = os.Remove(dir + ".lock")
			return "", err
		}
		if err = os.Mkdir(dir, 0o755); err != nil {
			_ = f.Close()
			_ = os.Remove(dir + ".lock")
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return "", err
		}
		cloneLeases.Store(dir, f)
		return dir, nil
	}
}

// sweep tries to remove clones whose runs are gone, leaving any it cannot select or safely remove.
func sweep(clones, repo string) {
	entries, _ := os.ReadDir(clones) // none yet: nothing to sweep
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(clones, e.Name())
		lock, locked, err := tryCloneExclusive(dir)
		if err != nil || !locked {
			continue
		}
		if kept(dir, repo, lock) {
			_ = closeClone(lock)
			continue
		}
		_ = remove(dir, lock)
	}
}

// release ends the run's hold on its clone dir, removing it when eligible and safely selected.
func release(dir, repo string) error {
	if dir == "" {
		return nil
	}
	closeCloneLease(dir)
	lock, locked, err := tryCloneExclusive(dir)
	if err != nil || !locked {
		return err
	}
	if kept(dir, repo, lock) {
		return closeClone(lock)
	}
	return remove(dir, lock)
}

// kept says whether the clone at dir delivered and its PR is still open: its branch is not merged into its
// base, as fetched again from the source, and GitHub, when configured, does not say its PR is closed. The
// clone lock selected, on Unix, is the one asked: its .delivered read beside it, git run in its open directory.
func kept(dir, repo string, lock *os.File) bool {
	delivered := func() ([]byte, error) { return os.ReadFile(dir + ".delivered") }
	gitIn, ghDir := func(args ...string) (string, error) { return git(dir, args...) }, dir
	s, selectedClone := selected(lock)
	if selectedClone {
		delivered = func() ([]byte, error) { return s.parent.ReadFile(s.name + ".delivered") }
		gitIn, ghDir = func(args ...string) (string, error) { return gitAt(s.dir, args...) }, ""
	}
	b, err := delivered()
	if err != nil {
		return false
	}
	branch, base, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	if _, err := gitIn("fetch", "-q", "origin", base); err == nil {
		merged, err := gitIn("branch", "--merged", "FETCH_HEAD", "--list", branch)
		if selectedClone && err != nil {
			return true
		}
		if merged != "" {
			return false
		}
	} else if selectedClone {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return true
		}
	}
	if repo == "" {
		return true
	}
	// A gh that cannot tell keeps the clone: the next run asks again.
	state, err := command(ghDir, "gh", "pr", "view", branch, "--repo", repo, "--json", "state", "--jq", ".state")
	return err != nil || state == "OPEN"
}

// cloneGitHelper is atm's one hidden entry, gitAt's: atm cloneGitHelper <git args> changes into the directory
// open on fd 3, closes it and becomes git there. init reaches it before any command line parsing.
const cloneGitHelper = "__atm-clone-git"

func init() {
	if len(os.Args) < 2 || os.Args[1] != cloneGitHelper {
		return
	}
	err := syscall.Fchdir(3)
	if err == nil {
		_ = syscall.Close(3)
		var git string
		if git, err = exec.LookPath("git"); err == nil {
			err = syscall.Exec(git, append([]string{"git"}, os.Args[2:]...), os.Environ())
		}
	}
	fmt.Fprintln(os.Stderr, "atm:", err)
	os.Exit(1)
}

// gitAt runs git in the directory dir is open on, through cloneGitHelper, so that no path names it, on that
// directory's own .git: without one git fails rather than answer for a repository around it.
func gitAt(dir *os.File, args ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, append([]string{cloneGitHelper}, args...)...)
	cmd.ExtraFiles = []*os.File{dir}
	cmd.Env = append(os.Environ(), "GIT_DIR=.git")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func remove(dir string, lock *os.File) error {
	if s, ok := selected(lock); ok {
		return errors.Join(s.remove(lock), closeClone(lock))
	}
	opened, err := lock.Stat()
	if err != nil {
		return errors.Join(err, lock.Close())
	}
	current, err := os.Stat(dir + ".lock")
	if err != nil || !os.SameFile(opened, current) {
		return errors.Join(err, lock.Close())
	}
	removed, err := removeCloneDir(dir)
	if err != nil || !removed {
		return errors.Join(err, lock.Close())
	}
	err = errors.Join(os.RemoveAll(dir+".pid"), os.RemoveAll(dir+".delivered"), os.RemoveAll(dir+".lock"))
	return errors.Join(err, lock.Close())
}

// remove deletes the selected clone and its sidecars while its lock and directory still hold their names: its
// contents through the directory it selected, read-only ones too, then its emptied name.
func (s cloneSelection) remove(lock *os.File) error {
	if !s.holds(s.name+".lock", lock) {
		return nil // another clone's lock now
	}
	replaced := fmt.Errorf("%s was replaced since it was selected: kept", s.name)
	if !s.holds(s.name, s.dir) {
		return replaced
	}
	clone, err := s.parent.OpenRoot(s.name)
	if err != nil {
		return err
	}
	defer func() { _ = clone.Close() }()
	if at, err := clone.Stat("."); !sameAs(s.dir, at, err) {
		return replaced
	}
	entries, err := fs.ReadDir(clone.FS(), ".")
	for _, e := range entries {
		err = errors.Join(err, drop(clone, e.Name(), nil))
	}
	if err != nil {
		return err
	}
	if !s.holds(s.name, s.dir) {
		return replaced
	}
	if err := s.parent.Remove(s.name); err != nil {
		return err
	}
	return errors.Join(s.parent.RemoveAll(s.name+".pid"), s.parent.RemoveAll(s.name+".delivered"),
		s.parent.RemoveAll(s.name+".lock"))
}

// holds says whether name in the selected parent is the file f is open on, not a symlink to it.
func (s cloneSelection) holds(name string, f *os.File) bool {
	at, err := s.parent.Lstat(name)
	return sameAs(f, at, err)
}

func sameAs(f *os.File, at fs.FileInfo, err error) bool {
	held, heldErr := f.Stat()
	return err == nil && heldErr == nil && os.SameFile(at, held)
}
