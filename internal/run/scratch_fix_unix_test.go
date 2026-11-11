//go:build unix

package run

import (
	"path/filepath"
	"testing"
)

// The first turn leaves read-only files in its scratch directory and a red test; the red test runs again, still red,
// and the automatic fix turn starts with an empty scratch directory.
func TestFixTurnStartsWithReadOnlyFilesInScratch(t *testing.T) {
	builtATM(t)
	top := gitT(t, repo(t, "local", atmYAML), "rev-parse", "--show-toplevel")
	t.Cleanup(func() {
		scratches, _ := filepath.Glob(filepath.Join(top, ".atm", "runs", "*", "worker-scratch"))
		for _, scratch := range scratches {
			writable(scratch)
		}
	})
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
	followedRun(t, bin, top, 0, "--harness", "claude", issueFile(t, issue))
	if calls := workerCalls(t, order); calls != 2 {
		t.Fatalf("want initial worker and one fix turn, got %d", calls)
	}
	rep := e2eReport(t, top, lastRun(t, top, "passed"))
	if rep.FailedNode != "" || rep.node("checks").Result != "passed" || len(rep.Decisions) != 0 {
		t.Fatalf("the fix turn did not continue through Checks: %+v", rep)
	}
}
