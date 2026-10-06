//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A test run that changes the clone leaves a verdict about code that is no longer what the agent left.
func TestRedGreenVoidsTheVerdictWhenTheCloneChanges(t *testing.T) {
	clone := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cfg := []string{"-C", clone, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false",
			"-c", "core.hooksPath=" + os.DevNull}
		if out, err := exec.Command("git", append(cfg, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for name, text := range map[string]string{"fix": "", "test_fix.sh": ""} {
		if err := os.WriteFile(filepath.Join(clone, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Fails without fix, passes with it, and leaves a new file behind each time.
	argv := []string{"sh", "-c", "echo run >> side.txt; test -f fix"}
	f, err := os.Create(filepath.Join(t.TempDir(), "worker.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	v := redGreen(clone, "test_fix.sh", argv, time.Minute, &runLog{f: f, out: &strings.Builder{}})
	if v.OK || !strings.Contains(v.Message, "void") {
		t.Errorf("verdict %+v, want it void", v)
	}
}
