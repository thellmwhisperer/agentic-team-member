package run

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// git runs git in dir and returns its trimmed stdout; a failure carries stderr.
func git(dir string, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Stderr = dir, &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// gitLines is git's non-empty output lines; a failed git is none.
func gitLines(dir string, args ...string) []string {
	out, _ := git(dir, args...)
	var lines []string
	for line := range strings.Lines(out) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// clone makes a full clone of repo at baseRef under repo/.atm/clones, detached at the base commit, and
// returns its path and that commit. A clone, not a worktree: worktrees share the stash, and two runs
// popping it swapped their work (4-oct-2026).
func clone(repo, baseRef string) (dir, sha string, err error) {
	if sha, err = git(repo, "rev-parse", "--verify", baseRef+"^{commit}"); err != nil {
		return "", "", fmt.Errorf("base ref %q: %w", baseRef, err)
	}
	if dir, err = claim(filepath.Join(repo, ".atm", "clones")); err != nil {
		return "", "", err
	}
	for _, step := range [][]string{
		{"-C", repo, "clone", "-q", "--local", "--no-checkout", "--config", "core.autocrlf=false", repo, dir},
		{"-C", dir, "fetch", "-q", repo, sha},
		{"-C", dir, "checkout", "-q", "--detach", sha},
	} {
		if _, err := git("", step...); err != nil {
			return dir, sha, err
		}
	}
	exclude := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return dir, sha, err
	}
	f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return dir, sha, err
	}
	_, err = f.WriteString("\n/.atm/\n") // ATM's scratch, invisible to git even when the target does not ignore it
	if err = errors.Join(err, f.Close()); err != nil {
		return dir, sha, err
	}
	if status, err := git(dir, "status", "--porcelain"); err != nil || status != "" {
		return dir, sha, errors.Join(err, fmt.Errorf("the clone is dirty before the run:\n%s", status))
	}
	return dir, sha, nil
}

// claim makes a fresh atm-run-<timestamp> directory under root: mkdir either succeeds or the name is
// taken, so two runs started in the same second never share one.
func claim(root string) (string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(root, "atm-run-"+time.Now().Format("20060102-150405"))
	for n := 1; ; n++ {
		dir := base
		if n > 1 {
			dir = fmt.Sprintf("%s-%d", base, n)
		}
		err := os.Mkdir(dir, 0o755)
		if !errors.Is(err, fs.ErrExist) {
			return dir, err
		}
	}
}
