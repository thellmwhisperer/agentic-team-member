package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Both entry points must run the full suite with 45 minutes per package:
// Windows already needed nearly 28 minutes before the contract-repair tests.
// Capture the go invocation so a missing timeout or a narrowed suite fails.
func TestGlobalTestTimeoutIsFortyFiveMinutes(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	name := "go"
	if filepath.Ext(exe) == ".exe" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, args := range [][]string{{"make", "-s", "test"}, {"sh", "scripts/test.sh"}} {
		t.Run(args[0], func(t *testing.T) {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = ".."
			out, err := cmd.CombinedOutput()
			const want = "test\n-timeout\n45m\n./...\n"
			if err != nil || strings.ReplaceAll(string(out), "\r\n", "\n") != want {
				t.Fatalf("%v invoked go with %q, %v; want %q", args, out, err, want)
			}
		})
	}
}
