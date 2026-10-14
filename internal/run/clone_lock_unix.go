//go:build unix

package run

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

func lockCloneShared(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_SH) }

// openCloneLock opens the lock at path, <parent>/<name>.lock, and selects the clone <parent>/<name> with it.
func openCloneLock(path string) (*os.File, error) {
	parent, err := openCloneParent(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	s := cloneSelection{parent: parent, name: strings.TrimSuffix(filepath.Base(path), ".lock")}
	clone, err := openDir(parent, s.name)
	if err == nil {
		s.dir, err = clone.Open(".")
		_ = clone.Close()
	}
	var lock *os.File
	if err == nil {
		lock, err = parent.OpenFile(s.name+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	}
	if err == nil && !s.holds(s.name+".lock", lock) {
		_ = lock.Close()
		err = fmt.Errorf("%s is not a plain file", path)
	}
	if err != nil {
		if s.dir != nil {
			_ = s.dir.Close()
		}
		_ = parent.Close()
		return nil, err
	}
	cloneSelections.Store(lock, s)
	return lock, nil
}

// openCloneParent opens dir from /, one name at a time, refusing any symlink on the way, an inherited TMPDIR
// alias among them, but Darwin's own /var -> private/var.
func openCloneParent(dir string) (*os.Root, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "darwin" && (dir == "/var" || strings.HasPrefix(dir, "/var/")) && systemVar(r) {
		dir = "/private" + dir
	}
	for _, name := range strings.FieldsFunc(dir, func(c rune) bool { return c == '/' }) {
		next, err := openDir(r, name)
		_ = r.Close()
		if err != nil {
			return nil, err
		}
		r = next
	}
	return r, nil
}

// systemVar says whether /, open as r, holds Darwin's /var -> private/var, the link and / both root's and
// writable by no one else.
func systemVar(r *os.Root) bool {
	link, err := r.Lstat("var")
	top, topErr := r.Stat(".")
	target, linkErr := r.Readlink("var")
	return errors.Join(err, topErr, linkErr) == nil && target == "private/var" && rootOwned(link) && rootOwned(top)
}

func rootOwned(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && fi.Mode().Perm()&0o022 == 0
}

// openDir opens name in r, when it is a directory and not a symlink to one.
func openDir(r *os.Root, name string) (*os.Root, error) {
	at, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	next, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if fi, err := next.Stat("."); err != nil || !os.SameFile(at, fi) {
		_ = next.Close()
		return nil, errors.Join(err, fmt.Errorf("%s is not a plain directory", name))
	}
	return next, nil
}

func tryLockCloneExclusive(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
		return false, nil
	}
	return err == nil, err
}

func inheritCloneFile(cmd *exec.Cmd, f *os.File) error {
	cmd.ExtraFiles = append(cmd.ExtraFiles, f)
	return nil
}

func removeCloneDir(dir string) (bool, error) {
	err := os.RemoveAll(dir)
	return err == nil, err
}
