package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// watcher writes a lease for watcher id in the repository at dir, held by this process, its heartbeat beat ago.
func watcher(t *testing.T, dir, id string, beat time.Duration) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "started": time.Now().Add(-beat),
		"heartbeat": time.Now().Add(-beat)})
	path := filepath.Join(dir, ".atm", "watch", id+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRunWithoutALiveWatcherDoesNotStart(t *testing.T) {
	dir := repo(t, "https://example.com/o/r.git")
	if err := os.WriteFile(filepath.Join(dir, "retry.md"), []byte("# Retry\nType: fix\nRetry.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	watcher(t, dir, "stale", 31*time.Second)
	for _, args := range [][]string{
		{"run", "retry.md"},
		{"run", "--watcher", "nobody", "retry.md"},
		{"run", "retry.md", "--watcher", "stale"},
		{"axi", "run", "retry.md"},
	} {
		_, err := atm(t, args...)
		if run.ExitCode(err) != 2 || err == nil || err.Error() !=
			"no watcher: start atm watch in the session that launches this run and pass --watcher <id>" {
			t.Fatalf("atm %v: exit %d, %v", args, run.ExitCode(err), err)
		}
	}
	for _, path := range []string{"clones", "atm.sock", "runs"} {
		if _, err := os.Stat(filepath.Join(dir, ".atm", path)); !os.IsNotExist(err) {
			t.Fatalf(".atm/%s exists: %v", path, err)
		}
	}
}

func TestDoctorSaysWhetherAWatcherLives(t *testing.T) {
	dir := repo(t, "https://example.com/o/r.git")
	onPath(t, "claude")
	if out, err := atm(t, "init"); err != nil {
		t.Fatalf("atm init: %v\n%s", err, out)
	}
	out, err := atm(t, "doctor")
	if ls := lines(t, out); err == nil || ls[len(ls)-1] != "skill: missing" || ls[len(ls)-2] !=
		"fail watcher: none alive: start atm watch in the session that launches the run" {
		t.Fatalf("doctor without a watcher: %v\n%s", err, out)
	}
	watcher(t, dir, "w1", 0)
	out, err = atm(t, "doctor")
	if ls := lines(t, out); err != nil || ls[len(ls)-1] != "skill: missing" || ls[len(ls)-2] != "ok watcher: w1" {
		t.Fatalf("doctor with watcher w1: %v\n%s", err, out)
	}
}
