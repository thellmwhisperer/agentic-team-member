//go:build windows

package run

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func lockCloneShared(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), 0, 0, 1, 0, &overlapped)
}

func openCloneLock(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func tryLockCloneExclusive(f *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped)
	if err == windows.ERROR_LOCK_VIOLATION {
		return false, nil
	}
	return err == nil, err
}

func inheritCloneFile(cmd *exec.Cmd, f *os.File) error {
	// Windows keeps a live clone by refusing to rename its working directory.
	// Passing the lock handle also lets unrelated later children keep its file open.
	_, _ = cmd, f
	return nil
}

func removeCloneDir(dir string) (bool, error) {
	removing := dir + ".removing"
	if err := os.Rename(dir, removing); err != nil {
		return false, nil
	}
	if err := os.RemoveAll(removing); err != nil {
		_ = os.Rename(removing, dir)
		return false, err
	}
	return true, nil
}
