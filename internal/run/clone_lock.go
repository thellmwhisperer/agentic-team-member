package run

import (
	"os"
	"os/exec"
	"sync"
)

// cloneSelections holds, for each lock openCloneLock opened on Unix, the clone it selected with it: its parent
// and its directory, opened once, so that the decision to keep it and its removal act on that clone, never on
// whatever its path names later. Windows selects none and works by path.
var cloneSelections sync.Map // *os.File: cloneSelection

type cloneSelection struct {
	parent *os.Root
	name   string
	dir    *os.File
}

func selected(lock *os.File) (cloneSelection, bool) {
	s, ok := cloneSelections.Load(lock)
	sel, _ := s.(cloneSelection)
	return sel, ok
}

// closeClone closes lock and what it selected.
func closeClone(lock *os.File) error {
	if s, ok := cloneSelections.LoadAndDelete(lock); ok {
		_ = s.(cloneSelection).dir.Close()
		_ = s.(cloneSelection).parent.Close()
	}
	return lock.Close()
}

func closeCloneLease(dir string) {
	if f, ok := cloneLeases.LoadAndDelete(dir); ok {
		_ = f.(*os.File).Close()
	}
}

func tryCloneExclusive(dir string) (*os.File, bool, error) {
	f, err := openCloneLock(dir + ".lock")
	if err != nil {
		return nil, false, err
	}
	locked, err := tryLockCloneExclusive(f)
	if err != nil || !locked {
		_ = closeClone(f)
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
