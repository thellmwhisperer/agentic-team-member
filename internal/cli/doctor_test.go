package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// onPath puts an executable file under each name first on PATH; doctor only looks them up.
func onPath(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// lines is doctor's output, one check a line, checked to start with ok or fail.
func lines(t *testing.T, out string) []string {
	t.Helper()
	ls := strings.Split(strings.TrimSpace(out), "\n")
	for _, l := range ls {
		if !strings.HasPrefix(l, "ok ") && !strings.HasPrefix(l, "fail ") {
			t.Fatalf("want every line to start with ok or fail, got %q in\n%s", l, out)
		}
	}
	return ls
}

func TestDoctorPassesWhenEveryCheckPasses(t *testing.T) {
	for _, c := range []struct{ origin, want string }{
		{"https://github.com/o/r.git", "ok git|ok claude|ok gh|ok .atm.yaml"},
		{"https://example.com/o/r.git", "ok git|ok claude|ok .atm.yaml"},
	} {
		t.Run(c.origin, func(t *testing.T) {
			repo(t, c.origin)
			onPath(t, "claude", "gh")
			if out, err := atm(t, "init"); err != nil {
				t.Fatalf("atm init: %v\n%s", err, out)
			}
			out, err := atm(t, "doctor")
			if err != nil {
				t.Fatalf("atm doctor: %v\n%s", err, out)
			}
			var names []string
			for _, l := range lines(t, out) {
				names = append(names, strings.SplitN(l, ":", 2)[0])
			}
			if got := strings.Join(names, "|"); got != c.want {
				t.Fatalf("got checks %s, want %s\n%s", got, c.want, out)
			}
		})
	}
}

func TestDoctorRunsEveryCheckAndFailsWhenAnyFails(t *testing.T) {
	t.Run("nothing on PATH", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("PATH", t.TempDir())
		t.Chdir(t.TempDir())
		out, err := atm(t, "doctor")
		ls := lines(t, out)
		if len(ls) != 3 || !strings.HasPrefix(ls[0], "fail git") || !strings.HasPrefix(ls[1], "fail claude") ||
			!strings.HasPrefix(ls[2], "fail .atm.yaml") {
			t.Fatalf("want git, claude and .atm.yaml to fail, got\n%s", out)
		}
		if run.ExitCode(err) == 0 {
			t.Fatal("want a non-zero exit")
		}
	})
	t.Run("a key missing", func(t *testing.T) {
		dir := repo(t, "https://github.com/o/r.git")
		onPath(t, "claude", "gh")
		if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte("test: go test ./...\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := atm(t, "doctor")
		ls := lines(t, out)
		if len(ls) != 4 || !strings.HasPrefix(ls[3], "fail .atm.yaml") || !strings.Contains(ls[3], "lint") {
			t.Fatalf("want 4 checks, .atm.yaml failing on lint, got\n%s", out)
		}
		if run.ExitCode(err) == 0 {
			t.Fatal("want a non-zero exit")
		}
	})
	t.Run("duplicate keys", func(t *testing.T) {
		dir := repo(t, "https://github.com/o/r.git")
		onPath(t, "claude", "gh")
		if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte("install: ''\ninstall: ''\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := atm(t, "doctor")
		ls := lines(t, out)
		if len(ls) != 4 || !strings.HasPrefix(ls[3], "fail .atm.yaml") {
			t.Fatalf("want four one-line checks and .atm.yaml failing, got\n%s", out)
		}
		if run.ExitCode(err) == 0 {
			t.Fatal("want a non-zero exit")
		}
	})
}
