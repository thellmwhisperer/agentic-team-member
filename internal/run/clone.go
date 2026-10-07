package run

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// clone sweeps root/.atm/clones, then claims a clone there of root at base's commit, detached, with install
// run in it. It returns the clone even when a later step fails, so the caller can release it.
//
// Next to each clone, <clone>.pid holds its run's pid, then each process group the run started there, negative,
// and, once its run's delivery command ended, <clone>.delivered holds
// "<branch> <base>": what tells a later run whether the clone is still needed.
func clone(root, base, install, repo string) (dir, sha string, err error) {
	if sha, err = git(root, "rev-parse", "--verify", base+"^{commit}"); err != nil {
		return "", "", fmt.Errorf("base ref %q: %w", base, err)
	}
	clones := filepath.Join(root, ".atm", "clones")
	sweep(clones, repo)
	if dir, err = claim(clones, time.Now()); err != nil {
		return "", sha, err
	}
	hold(dir, true) // before its pid file, which a sweep needs to remove it
	if err = os.WriteFile(dir+".pid", []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return dir, sha, err
	}
	for _, args := range [][]string{
		{"clone", "-q", "--local", "--no-checkout", root, dir},
		{"-C", dir, "fetch", "-q", root, sha},
		{"-C", dir, "checkout", "-q", "--detach", sha},
	} {
		if _, err = git("", args...); err != nil {
			return dir, sha, err
		}
	}
	if err = exclude(dir, "/.atm/"); err != nil || install == "" { // ATM's scratch
		return dir, sha, err
	}
	if _, err = command(dir, "sh", "-c", install); err != nil {
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
		if err := os.Mkdir(dir, 0o755); !errors.Is(err, fs.ErrExist) {
			return dir, err
		}
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
		b, err := os.ReadFile(dir + ".pid")
		// No pid yet is a run between its claim and its pid file. ponytail: a run that died right there
		// leaves its empty clone for good; removing pid-less clones by age is the upgrade.
		if err != nil {
			continue
		}
		if held(dir, strings.Fields(string(b))) || kept(dir, repo) {
			continue
		}
		_ = remove(dir) // a clone that will not go is tried again by the next run
	}
}

// held says whether a process of the run of clone dir still runs, from pids, its pid file: the run's own, or,
// negative, a process group the run started, which outlives it when it dies.
func held(dir string, pids []string) bool {
	for _, p := range pids {
		pid, err := strconv.Atoi(p)
		if err != nil || pid > 0 && running(dir, pid) || pid < 0 && groupAlive(-pid) {
			return true
		}
	}
	return len(pids) == 0 // a pid file being written
}

// note adds the process group of cmd, just started, to the pid file of the clone it runs in, if it runs in one.
func note(cmd *exec.Cmd) {
	f, err := os.OpenFile(cmd.Dir+".pid", os.O_APPEND|os.O_WRONLY, 0)
	if err == nil {
		_, _ = fmt.Fprintf(f, " -%d", cmd.Process.Pid) // it leads its group
		_ = f.Close()
	}
}

// release ends the run's hold on its clone dir: removed, unless kept.
func release(dir, repo string) error {
	if dir == "" || kept(dir, repo) {
		return nil
	}
	return remove(dir)
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

func remove(dir string) error {
	return errors.Join(os.RemoveAll(dir), os.RemoveAll(dir+".pid"), os.RemoveAll(dir+".delivered"))
}

// alive says whether process pid runs. ponytail: outside the background process, a dead run's pid the OS gave
// to another process reads as alive and keeps its clone until that process ends; comparing start times is the
// upgrade.
func alive(pid int) bool {
	p, err := os.FindProcess(pid) // on Windows, fails for a process that is gone
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
