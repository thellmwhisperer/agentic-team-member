//go:build unix

package run

import (
	"os"
	"path/filepath"
	"testing"
)

// builtATM returns the shared follow-test atm build for this process.
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
