//go:build unix

package run

import (
	"os"
	"path/filepath"
	"testing"
)

func builtATM(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "atm")
	buildATM(t, bin, "follow-test")
	return bin
}

func stopServing(t *testing.T, top string) {
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(top, ".atm", "serve.log")); err == nil {
			killServeProcesses(t, top)
		}
	})
}
