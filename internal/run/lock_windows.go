package run

import (
	"errors"
	"os"
	"syscall"
)

// lock takes the lock at path, nil when another process holds it: the file, opened shared with no one. The
// system drops it when its holder dies.
func lock(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, syscall.Errno(32)) { // ERROR_SHARING_VIOLATION
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// pidAlive says whether process pid runs: it opens and has not exited.
func pidAlive(pid int) bool {
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = syscall.CloseHandle(h) }()
	var code uint32
	return syscall.GetExitCodeProcess(h, &code) == nil && code == 259 // STILL_ACTIVE
}
