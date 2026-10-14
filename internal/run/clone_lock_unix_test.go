//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// fileState is a file's bytes and mode, to tell it was not touched.
type fileState struct {
	b    string
	mode os.FileMode
}

func fixtures(t *testing.T, paths ...string) []fileState {
	t.Helper()
	var got []fileState
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		var b []byte
		if !fi.IsDir() {
			if b, err = os.ReadFile(p); err != nil {
				t.Fatal(err)
			}
		}
		got = append(got, fileState{string(b), fi.Mode()})
	}
	return got
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// An inherited TMPDIR that is a symlink is no trusted way to a clone: release through it changes nothing past
// the link and does not report the clone gone.
func TestReleaseThroughTMPDIRAliasChangesNothing(t *testing.T) {
	tree := t.TempDir()
	clone := filepath.Join(tree, "atm-run-20261001-120000")
	if err := os.Mkdir(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(clone, "work"), clone + ".lock", clone + ".pid"} {
		write(t, p, "external "+filepath.Base(p))
	}
	alias := filepath.Join(t.TempDir(), "tmp")
	if err := os.Symlink(tree, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", alias)
	paths := []string{clone, filepath.Join(clone, "work"), clone + ".lock", clone + ".pid"}
	before := fixtures(t, paths...)
	err := release(filepath.Join(os.TempDir(), filepath.Base(clone)), "")
	for _, p := range paths {
		if _, statErr := os.Lstat(p); statErr != nil {
			t.Fatalf("release through the alias removed %s (err %v)", p, err)
		}
	}
	if after := fixtures(t, paths...); !slices.Equal(before, after) {
		t.Fatalf("release through the alias changed the tree behind it: %v, want %v", after, before)
	}
	if err == nil {
		t.Fatal("release through the alias reported success")
	}
}

// A clone whose directory was replaced after its lock was taken is not the clone remove selected: the
// replacement and its sidecars stay.
func TestRemoveKeepsADirectoryThatReplacedTheSelectedClone(t *testing.T) {
	parent := t.TempDir()
	a := filepath.Join(parent, "atm-run-20261001-120000")
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(a, "a"), "a")
	lock, locked, err := tryCloneExclusive(a)
	if err != nil || !locked {
		t.Fatalf("lock A: %v, %v", locked, err)
	}
	if err := os.Rename(a, filepath.Join(parent, "a-moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(a, "b"), "b")
	write(t, a+".pid", "b")
	err = remove(a, lock)
	for _, p := range []string{filepath.Join(a, "b"), a + ".pid", a + ".lock"} {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Fatalf("remove deleted %s of the replacement (err %v)", p, err)
		}
	}
	if err == nil {
		t.Fatal("remove reported success for a clone it did not remove")
	}
}

// release decides on the clone it locked: A, open, is moved away as its decision starts and a merged clone B
// takes its path. A is kept, B is untouched.
func TestReleaseDecidesOnTheSelectedClone(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	a := oldClone(t, root, false, true, true)
	b := filepath.Join(t.TempDir(), "b")
	gitT(t, root, "clone", "-q", root, b)
	gitT(t, b, "checkout", "-q", "-b", "atm/fix")
	gitT(t, b, "fetch", "-q", "origin", "main") // B's own FETCH_HEAD: atm/fix is merged there
	write(t, filepath.Join(b, "b"), "b")
	head := gitT(t, b, "rev-parse", "HEAD")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// The first fetch moves A away and B into its place, then fetches where it was started.
	write(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$1" = fetch ] && [ -e "$ATM_TEST_SWAP" ]; then
	rm "$ATM_TEST_SWAP" && mv "$ATM_TEST_A" "$ATM_TEST_A.moved" && mv "$ATM_TEST_B" "$ATM_TEST_A" || exit 9
fi
exec "$ATM_TEST_GIT" "$@"
`)
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	swap := filepath.Join(t.TempDir(), "swap")
	write(t, swap, "")
	t.Setenv("ATM_TEST_SWAP", swap)
	t.Setenv("ATM_TEST_A", a)
	t.Setenv("ATM_TEST_B", b)
	t.Setenv("ATM_TEST_GIT", gitPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := release(a, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(swap); err == nil {
		t.Fatal("the decision ran no fetch")
	}
	if _, err := os.Stat(filepath.Join(a, "b")); err != nil {
		t.Fatalf("release removed B, which replaced the clone it selected: %v", err)
	}
	if got := gitT(t, a, "rev-parse", "HEAD"); got != head {
		t.Fatalf("B's HEAD moved to %s, want %s", got, head)
	}
	if _, err := os.Stat(a + ".moved"); err != nil {
		t.Fatalf("A, still open, was not kept: %v", err)
	}
	if _, err := os.Stat(a + ".delivered"); err != nil {
		t.Fatalf("A's sidecar went: %v", err)
	}
}

// A delivered clone whose branch is merged goes, with its sidecars, from the system temp directory.
func TestReleaseRemovesAMergedClone(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	a := oldClone(t, root, false, true, false)
	write(t, a+".pid", "1")
	if err := release(a, ""); err != nil {
		t.Fatal(err)
	}
	if left := clones(t, root); len(left) != 0 {
		t.Fatalf("a merged clone outlived release: %v", left)
	}
}
