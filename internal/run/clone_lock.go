package run

import (
	"os"
	"sync"
)

// cloneSelections holds, for each lock openCloneLock opened on Unix, the clone it selected with it: its parent
// and its directory, opened once, so that the retention decision and removal use the selected directory.
// Windows selects none and works by path.
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
