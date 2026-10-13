package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// onPath puts executable files first on PATH.
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

// lines is doctor's output, one check a line, checked to start with ok or fail, without its last line, checked
// to say how to say hello to the agent.
func lines(t *testing.T, out string) []string {
	t.Helper()
	ls := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.HasPrefix(ls[len(ls)-1], "run atm doctor --agent to say hello to ") {
		t.Fatalf("want the last line to say how to say hello to the agent, got\n%s", out)
	}
	ls = ls[:len(ls)-1]
	for _, l := range ls {
		if !strings.HasPrefix(l, "ok ") && !strings.HasPrefix(l, "fail ") {
			t.Fatalf("want every line to start with ok or fail, got %q in\n%s", l, out)
		}
	}
	return ls
}

func TestDoctorPassesWhenEveryCheckPasses(t *testing.T) {
	for _, c := range []struct{ origin, want string }{
		{"https://github.com/o/r.git", "ok git|ok claude|ok gh|ok .atm.yaml|ok watcher"},
		{"https://example.com/o/r.git", "ok git|ok claude|ok .atm.yaml|ok watcher"},
	} {
		t.Run(c.origin, func(t *testing.T) {
			watcher(t, repo(t, c.origin), "w", 0)
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
		doctorRepoWithConfig(t, "test: go test ./...\n")
		out, err := atm(t, "doctor")
		ls := lines(t, out)
		if len(ls) != 5 || !strings.HasPrefix(ls[3], "fail .atm.yaml") || !strings.Contains(ls[3], "lint") {
			t.Fatalf("want 5 checks, .atm.yaml failing on lint, got\n%s", out)
		}
		if run.ExitCode(err) == 0 {
			t.Fatal("want a non-zero exit")
		}
	})
	t.Run("duplicate keys", func(t *testing.T) {
		doctorRepoWithConfig(t, "install: ''\ninstall: ''\n")
		out, err := atm(t, "doctor")
		ls := lines(t, out)
		if len(ls) != 5 || !strings.HasPrefix(ls[3], "fail .atm.yaml") {
			t.Fatalf("want five one-line checks and .atm.yaml failing, got\n%s", out)
		}
		if run.ExitCode(err) == 0 {
			t.Fatal("want a non-zero exit")
		}
	})
}

func doctorRepoWithConfig(t *testing.T, config string) string {
	t.Helper()
	dir := repo(t, "https://github.com/o/r.git")
	watcher(t, dir, "w", 0)
	onPath(t, "claude", "gh")
	if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
