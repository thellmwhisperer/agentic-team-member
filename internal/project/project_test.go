package project

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFindGitHubRemoteRequiresExactHost(t *testing.T) {
	for _, tc := range []struct {
		remote string
		want   string
	}{
		{"git@github.com:octo/calc.git", "octo/calc"},
		{"https://github.com/octo/calc.git", "octo/calc"},
		{"https://notgithub.com/octo/calc.git", ""},
	} {
		t.Run(tc.remote, func(t *testing.T) {
			dir := t.TempDir()
			git(t, dir, "init")
			git(t, dir, "remote", "add", "origin", tc.remote)

			got, err := Find(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got.GitHub != tc.want {
				t.Errorf("Find(%q).GitHub = %q, want %q", tc.remote, got.GitHub, tc.want)
			}
		})
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", filepath.Clean(dir)}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
