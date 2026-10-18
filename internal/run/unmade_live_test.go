package run

import (
	"os"
	"path/filepath"
	"testing"
)

// Exercise the real Checks boundary with disposable Git repositories. The oracle
// is the pre-agent bytes, and install must see new ignored content removed.
func TestChecksRestoresIgnoredBytesBeforeInstall(t *testing.T) {
	for _, mutation := range []string{"rewrite", "delete", "symlink"} {
		t.Run(mutation, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			commitBase(t, root, ".gitignore", "dep/\n")
			runDir := checksStashRunDir(t, root)
			dir, sha := checksStashClone(t, root)
			write := func(name string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(dir, "dep"), 0o700); err != nil {
				t.Fatal(err)
			}
			original := []byte("old\r\n\x00\xff\n")
			write("dep/state", []byte("old\n"))
			write("dep/state [x] file", original)
			write("expected", original)
			if err := before(dir); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"dep/state", "dep/state [x] file"} {
				if mutation == "rewrite" {
					write(name, []byte("new\n"))
					continue
				}
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
				if mutation == "symlink" {
					if err := os.Symlink("../a.txt", filepath.Join(dir, name)); err != nil {
						t.Fatal(err)
					}
				}
			}
			write("dep/link", []byte("agent-created"))
			write("a.txt", []byte("fixed\n"))
			write("a_test.sh", []byte("test -e dep/link && test \"$(cat a.txt)\" = fixed\n"))
			c := checksStashConfig(t, root)
			c.Install = "set -x; test ! -e dep/link && cmp expected 'dep/state [x] file' && " +
				"test ! -L dep/state && { grep -q old dep/state && touch dep/link || true; }"
			result, err := checks(dir, runDir, sha, "fix", "a_test.sh", c)
			t.Logf("Checks result: %+v", result)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(dir, "dep/state [x] file"))
			if err != nil || string(b) != string(original) {
				t.Fatalf("restored bytes = %q, error = %v", b, err)
			}
			t.Logf("restored bytes=%q; install observed no agent link and recreated it; "+
				"proof rejected base and accepted fixed source", b)
		})
	}
}
