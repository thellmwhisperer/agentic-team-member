//go:build unix

package run

import (
	"os"
	"os/exec"
	"syscall"
)

func lockCloneShared(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_SH) }

func openCloneLock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
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
