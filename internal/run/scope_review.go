package run

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// reviewScratch empties the worker's scratch directory under the run's directory and so outside the clone,
// and points the agent's TMPDIR and the clone boundary check at it: a fresh one per call.
func (a *agent) reviewScratch(name string) (string, error) {
	dir := filepath.Join(a.dir, name+"-scratch")
	root, err := os.OpenRoot(a.dir)
	if err == nil {
		defer func() { _ = root.Close() }()
		err = drop(root, name+"-scratch", nil)
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	a.env = append(slices.DeleteFunc(slices.Clone(a.env), func(kv string) bool {
		return strings.HasPrefix(kv, "TMPDIR=")
	}), "TMPDIR="+dir)
	a.scratch = dir
	return dir, nil
}
