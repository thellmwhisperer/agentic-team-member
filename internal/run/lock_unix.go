//go:build unix

package run

import (
	"errors"
	"os"
	"syscall"
)

// lock takes the lock at path, nil when another process holds it. The system drops it when its holder dies.
func lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, err
	}
	return f, nil
}

// pidAlive says whether process pid runs.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return pid > 0 && (err == nil || errors.Is(err, syscall.EPERM))
}
