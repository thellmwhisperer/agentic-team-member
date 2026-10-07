package run

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// clone sweeps root/.atm/clones, then claims a clone there of root at base's commit, detached, with install
// run in it. It returns the clone even when a later step fails, so the caller can release it.
//
// Next to each clone, <clone>.lock guards cleanup while the run or an inheriting process holds it.
// <clone>.delivered holds "<branch> <base>": what tells a later run whether the clone is still needed.

var cloneLeases sync.Map

func clone(root, base, install, repo string) (dir, sha string, err error) {
	if sha, err = git(root, "rev-parse", "--verify", base+"^{commit}"); err != nil {
		return "", "", fmt.Errorf("base ref %q: %w", base, err)
	}
	clones := filepath.Join(root, ".atm", "clones")
	sweep(clones, repo)
	if dir, err = claim(clones, time.Now()); err != nil {
		return "", sha, err
	}
	for _, args := range [][]string{
		{"clone", "-q", "--local", "--no-checkout", root, dir},
		{"-C", dir, "fetch", "-q", root, sha},
		{"-C", dir, "checkout", "-q", "--detach", sha},
	} {
		if _, err = commandFor(dir, "", "git", args...); err != nil {
			return dir, sha, err
		}
	}
	if err = exclude(dir, "/.atm/"); err != nil || install == "" { // ATM's scratch
		return dir, sha, err
	}
	if _, err = commandFor(dir, dir, "sh", "-c", install); err != nil {
		return dir, sha, fmt.Errorf("install: %w", err)
	}
	return dir, sha, nil
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

// sweep removes every clone under clones whose run is gone and that is not kept.
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
		if kept(dir, repo) {
			_ = lock.Close()
			continue
		}
		_ = remove(dir, lock)
	}
}

// release ends the run's hold on its clone dir: removed, unless kept.
func release(dir, repo string) error {
	if dir == "" {
		return nil
	}
	closeCloneLease(dir)
	lock, locked, err := tryCloneExclusive(dir)
	if err != nil || !locked {
		return err
	}
	if kept(dir, repo) {
		return lock.Close()
	}
	return remove(dir, lock)
}

// kept says whether the clone at dir delivered and its PR is still open: its branch is not merged into its
// base, as fetched again from the source, and GitHub, when configured, does not say its PR is closed.
func kept(dir, repo string) bool {
	b, err := os.ReadFile(dir + ".delivered")
	if err != nil {
		return false
	}
	branch, base, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	if _, err := git(dir, "fetch", "-q", "origin", base); err == nil {
		if merged, _ := git(dir, "branch", "--merged", "FETCH_HEAD", "--list", branch); merged != "" {
			return false
		}
	}
	if repo == "" {
		return true
	}
	// A gh that cannot tell keeps the clone: the next run asks again.
	state, err := command(dir, "gh", "pr", "view", branch, "--repo", repo, "--json", "state", "--jq", ".state")
	return err != nil || state == "OPEN"
}

func remove(dir string, lock *os.File) error {
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
