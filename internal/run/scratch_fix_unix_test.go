//go:build unix

package run

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFixTurnStartsWithReadOnlyFilesInScratch(t *testing.T) {
	builtATM(t)
	top := gitT(t, repo(t, "local", atmYAML), "rev-parse", "--show-toplevel")
	order := filepath.Join(t.TempDir(), "order")
	t.Setenv("FAKE_AGENT_ORDER", order)
	t.Setenv("FAKE_AGENT_WORK", fixWork+`; if test -f first-turn; then
  test ! -e "$TMPDIR/cache" || exit 1
else
  mkdir -p "$TMPDIR/cache/module"
  echo cached > "$TMPDIR/cache/module/source.go"
  chmod 444 "$TMPDIR/cache/module/source.go"
  chmod 555 "$TMPDIR/cache/module" "$TMPDIR/cache"
  echo broken > a.txt
  touch first-turn
fi`)
	bin := followUpsServer(t, top)
	_, label := question(t, bin, top, "test: exit status 1", 1,
		"run", "--follow", "--harness", "claude", issueFile(t, issue))
	scratch := filepath.Join(runDir(top, label), "worker-scratch")
	t.Cleanup(func() { writable(scratch) })
	for path, want := range map[string]os.FileMode{
		"cache": 0o555, "cache/module": 0o555, "cache/module/source.go": 0o444,
	} {
		info, err := os.Stat(filepath.Join(scratch, path))
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("scratch %s: want mode %o, got %v, %v", path, want, info, err)
		}
	}
	answerFailedCheck(t, bin, top, label, "fix", 0)
	if calls := workerCalls(t, order); calls != 2 {
		t.Fatalf("want initial worker and one fix turn, got %d", calls)
	}
	rep := e2eReport(t, top, label)
	if rep.FailedNode != "" || rep.node("checks").Result != "passed" ||
		len(rep.Decisions) != 1 || rep.Decisions[0].Answer != "fix" {
		t.Fatalf("fix did not continue through Checks: %+v", rep)
	}
}
