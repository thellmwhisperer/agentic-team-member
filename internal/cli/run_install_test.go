package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunRefusesInstallBeforeBackgroundLaunch(t *testing.T) {
	if os.Getenv("ATM_TEST_INSTALL_REFUSAL") == "1" {
		os.Args = []string{"atm", "run", "--watcher", "w", "retry.md"}
		Execute()
		return
	}
	for _, source := range []string{"/.tmp/slopslint/x", "/.atm/clones/atm-run-install/x"} {
		t.Run(source, func(t *testing.T) {
			dir := setupConcurrentRunRepo(t)
			watcher(t, dir, "w", 0)
			top, err := toplevel()
			if err != nil {
				t.Fatal(err)
			}
			install := "cp " + filepath.ToSlash(top) + source + " .tmp/x"
			if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte(installConfig(install)), 0o600); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(exe, "-test.run=^TestRunRefusesInstallBeforeBackgroundLaunch$")
			cmd.Env = append(os.Environ(), "ATM_TEST_INSTALL_REFUSAL=1")
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("want exit 2, got %v\n%s", err, out)
			}
			if want := "atm: install reaches outside the clone: " + install + "\n"; string(out) != want {
				t.Fatalf("want %q, got %q", want, out)
			}
			for _, path := range []string{"clones", "atm.sock", "runs"} {
				if _, err := os.Stat(filepath.Join(dir, ".atm", path)); !os.IsNotExist(err) {
					t.Fatalf(".atm/%s exists: %v", path, err)
				}
			}
		})
	}
}
