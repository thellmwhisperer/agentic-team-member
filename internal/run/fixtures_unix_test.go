//go:build unix

package run

import (
	"os"
	"path/filepath"
	"testing"
)

// builtATM is the real atm every real-binary test shares, built once per process.
func builtATM(t *testing.T) string {
	t.Helper()
	return sharedATM(t, "follow-test")
}

func stopServing(t *testing.T, top string) {
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(top, ".atm", "serve.log")); err == nil {
			killServeProcesses(t, top)
		}
	})
}
