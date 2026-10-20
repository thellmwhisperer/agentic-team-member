package run

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// reviewScratch empties the scratch directory of agent call name, a review or a worker, under the run's directory
// and so outside the clone, and points the agent's TMPDIR and the clone boundary check at it: a fresh one per call.
func (a *agent) reviewScratch(name string) (string, error) {
	dir := filepath.Join(a.dir, name+"-scratch")
	if err := os.RemoveAll(dir); err != nil {
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
