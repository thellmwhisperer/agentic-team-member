package run

import (
	"os"
	"os/exec"
)

func closeCloneLease(dir string) {
	if f, ok := cloneLeases.LoadAndDelete(dir); ok {
		_ = f.(*os.File).Close()
	}
}

func tryCloneExclusive(dir string) (*os.File, bool, error) {
	f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	locked, err := tryLockCloneExclusive(f)
	if err != nil || !locked {
		_ = f.Close()
		return nil, false, err
	}
	return f, true, nil
}

func inheritCloneLease(cmd *exec.Cmd, dir string) error {
	if f, ok := cloneLeases.Load(dir); ok {
		return inheritCloneFile(cmd, f.(*os.File))
	}
	return nil
}
