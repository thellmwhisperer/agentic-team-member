package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Git for Windows' cmd/git.exe and bin/sh.exe launch the actual tools in
// mingw64/bin and usr/bin. Avoid that extra process for every test command.
// Other installations keep their existing PATH resolution.
func nativeWindowsTestTools() error {
	var dirs []string
	for _, tool := range []struct{ name, dir string }{{"git", "mingw64/bin"}, {"sh", "usr/bin"}} {
		path, err := exec.LookPath(tool.name)
		if err != nil {
			continue
		}
		wrapperDir := filepath.Dir(path)
		if name := strings.ToLower(filepath.Base(wrapperDir)); name != "cmd" && name != "bin" {
			continue
		}
		nativeDir := filepath.Join(filepath.Dir(wrapperDir), filepath.FromSlash(tool.dir))
		if info, err := os.Stat(filepath.Join(nativeDir, tool.name+".exe")); err == nil && !info.IsDir() {
			dirs = append(dirs, nativeDir)
		}
	}
	if len(dirs) == 0 {
		return nil
	}
	return os.Setenv("PATH", strings.Join(append(dirs, os.Getenv("PATH")), string(os.PathListSeparator)))
}

func TestWindowsTestToolsResolveNativeExecutables(t *testing.T) {
	for _, tc := range []struct {
		name, gitDir, shDir string
		native              bool
	}{
		{"launchers", "cmd", "bin", true},
		{"no native tools", "cmd", "bin", false},
		{"custom tools", "custom", "custom", true},
		{"already native", "mingw64/bin", "usr/bin", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "Git with spaces")
			suffix := ""
			if runtime.GOOS == "windows" {
				suffix = ".exe"
			}
			files := []string{tc.gitDir + "/git" + suffix, tc.shDir + "/sh" + suffix}
			if tc.native {
				files = append(files, "mingw64/bin/git.exe", "usr/bin/sh.exe")
			}
			for _, file := range files {
				path := filepath.Join(root, filepath.FromSlash(file))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, path, "not executed", 0o755)
			}
			sep := string(os.PathListSeparator)
			original := filepath.Join(root, filepath.FromSlash(tc.gitDir)) + sep +
				filepath.Join(root, filepath.FromSlash(tc.shDir))
			t.Setenv("PATH", original)
			if err := nativeWindowsTestTools(); err != nil {
				t.Fatal(err)
			}
			want := original
			if tc.name == "launchers" {
				want = filepath.Join(root, "mingw64", "bin") + sep + filepath.Join(root, "usr", "bin") + sep + original
			}
			if got := os.Getenv("PATH"); got != want {
				t.Fatalf("PATH = %q; want %q", got, want)
			}
		})
	}
}

// The Windows test setup may bypass Git for Windows' launchers, but must keep
// real Git and shell execution, including arguments, working directories and failures.
func TestTestToolsPreserveGitAndShellExecution(t *testing.T) {
	root := repo(t, "git@example.com:owner/repo.git", atmYAML)
	cmd := exec.Command("sh", "-c", `
test "$1" = 'argument with spaces' || exit 1
test "$(git branch --show-current)" = main || exit 2
test "$(git show HEAD:a.txt)" = broken || exit 3
test "$(git remote get-url origin)" = 'git@example.com:owner/repo.git' || exit 4
printf '%s\n' shell-and-git-ok
exit 7
`, "fixture", "argument with spaces")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 7 ||
		strings.TrimSpace(string(out)) != "shell-and-git-ok" {
		t.Fatalf("shell output = %q, error = %v; want shell-and-git-ok and exit 7", out, err)
	}
}
