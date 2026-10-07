package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// atm runs atm with args and returns what it printed.
func atm(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	c := root()
	c.SetArgs(args)
	c.SetOut(&out)
	c.SetErr(&out)
	err := c.Execute()
	return out.String(), err
}

// repo is a new git repository with origin, the working directory for the rest of the test, under a new home.
func repo(t *testing.T, origin string) string {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", origin}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(dir)
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInitWritesCompleteConfigAndIgnoresAtmOnce(t *testing.T) {
	for _, c := range []struct{ name, before, after string }{
		{"no .gitignore", "", "/.atm/\n"},
		{"no trailing newline", "node_modules", "node_modules\n/.atm/\n"},
		{"already ignored", "/.atm/\ndist/\n", "/.atm/\ndist/\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := repo(t, "https://example.com/o/r.git")
			ignore, yml := filepath.Join(dir, ".gitignore"), filepath.Join(dir, ".atm.yaml")
			if c.before != "" {
				if err := os.WriteFile(ignore, []byte(c.before), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if out, err := atm(t, "init"); err != nil {
				t.Fatalf("atm init: %v\n%s", err, out)
			}
			if got := read(t, ignore); got != c.after {
				t.Fatalf(".gitignore: got %q, want %q", got, c.after)
			}
			first := read(t, yml)
			if !strings.Contains(first, "# ") {
				t.Fatalf(".atm.yaml has no comments:\n%s", first)
			}
			if _, err := config.Load(dir, os.Getenv("HOME"), config.Agent{}); err != nil {
				t.Fatalf(".atm.yaml from init does not load: %v", err)
			}
			if out, err := atm(t, "init"); err != nil {
				t.Fatalf("second atm init: %v\n%s", err, out)
			}
			if read(t, ignore) != c.after || read(t, yml) != first {
				t.Fatal("a second atm init changed .gitignore or .atm.yaml")
			}
		})
	}
}

func TestInitKeepsAnExistingConfig(t *testing.T) {
	dir := repo(t, "https://example.com/o/r.git")
	yml := filepath.Join(dir, ".atm.yaml")
	if err := os.WriteFile(yml, []byte("test: go test ./...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := atm(t, "init"); err != nil {
		t.Fatalf("atm init: %v\n%s", err, out)
	}
	if got := read(t, yml); got != "test: go test ./...\n" {
		t.Fatalf("atm init overwrote .atm.yaml: %q", got)
	}
}
