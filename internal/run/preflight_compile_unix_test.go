//go:build unix

package run

import (
	"strings"
	"testing"
)

// TestPreflightRefusesWhenTestsDoNotCompile launches the real atm run on a repository whose .atm.yaml declares
// a compile command: non-zero stops the launch at preflight with its output, zero lets the run start, and no
// key behaves as before.
func TestPreflightRefusesWhenTestsDoNotCompile(t *testing.T) {
	bin := builtATM(t)
	t.Run("exits non-zero", func(t *testing.T) {
		root := repo(t, "local", atmYAML+"compile: 'test -f a_test.sh && echo create_test.go:267:27: "+
			"undefined: syscall.PathMax; exit 1'\n")
		out, code := preflightLaunch(t, bin, root, issueFile(t, issue))
		assertStopped(t, root, out, code, "compile", "create_test.go:267:27: undefined: syscall.PathMax")
	})
	t.Run("exits zero", func(t *testing.T) {
		root := repo(t, "local", atmYAML+"compile: 'test -f a_test.sh'\n")
		if end := preflightFinished(t, bin, root, issueFile(t, issue)); end.Outcome != "passed" {
			t.Fatalf("want the run started and passed, got %+v", end)
		}
	})
	t.Run("without the key", func(t *testing.T) {
		root := repo(t, "local", atmYAML)
		if end := preflightFinished(t, bin, root, issueFile(t, issue)); end.Outcome != "passed" {
			t.Fatalf("want the run passed, got %+v", end)
		}
	})
	t.Run("help lists it", func(t *testing.T) {
		out, _ := preflightLaunch(t, bin, repo(t, "local", atmYAML), "--help")
		if !strings.Contains(out, "\n  compile ") {
			t.Fatalf("atm run --help lacks compile:\n%s", out)
		}
	})
}
