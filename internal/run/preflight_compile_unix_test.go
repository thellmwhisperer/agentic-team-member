//go:build unix

package run

import (
	"os"
	"path/filepath"
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
	t.Run("resume compiles the saved base", func(t *testing.T) {
		root := repo(t, "local", atmYAML+"compile: 'test ! -f broken_test.sh || { echo broken_test.sh; exit 1; }'\n")
		if err := os.WriteFile(filepath.Join(root, "broken_test.sh"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		gitT(t, root, "add", "broken_test.sh")
		gitT(t, root, "commit", "-q", "-m", "broken")
		saved := gitT(t, root, "rev-parse", "main")
		gitT(t, root, "rm", "-q", "broken_test.sh")
		gitT(t, root, "commit", "-q", "-m", "fixed")
		keepResumable(t, filepath.Join(root, ".atm", "runs", "failed-1"), resumePoint{Issue: "7", BaseRef: "main",
			Base: saved, Unit: 1, Original: &Issue{Title: "Retry on timeout", Text: issue}})
		out, code := preflightLaunch(t, bin, root, "--resume", "failed-1")
		assertNoStartedRun(t, root, out, code, 1, "preflight: compile: ")
		if !strings.Contains(out, "broken_test.sh") {
			t.Fatalf("want the saved base's compile output, got:\n%s", out)
		}
	})
	t.Run("help lists it", func(t *testing.T) {
		out, _ := preflightLaunch(t, bin, repo(t, "local", atmYAML), "--help")
		if !strings.Contains(out, "\n  compile ") {
			t.Fatalf("atm run --help lacks compile:\n%s", out)
		}
	})
}
