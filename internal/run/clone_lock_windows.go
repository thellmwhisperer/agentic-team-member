//go:build windows

package run

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func lockCloneShared(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), 0, 0, 1, 0, &overlapped)
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
	if err := windows.SetHandleInformation(windows.Handle(f.Fd()), windows.HANDLE_FLAG_INHERIT,
		windows.HANDLE_FLAG_INHERIT); err != nil {
		return err
	}
	attr := cmd.SysProcAttr
	if attr == nil {
		attr = &syscall.SysProcAttr{}
		cmd.SysProcAttr = attr
	}
	attr.AdditionalInheritedHandles = append(attr.AdditionalInheritedHandles, syscall.Handle(f.Fd()))
	return nil
}
