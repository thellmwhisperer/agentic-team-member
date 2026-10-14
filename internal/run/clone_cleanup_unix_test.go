//go:build unix

package run

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// readOnlyClone fills clone with cache/old.txt and cache/newdir/keep.txt, both directories mode 0555, and
// returns keep.txt. It skips the test where 0555 does not stop the owner, as for root.
func readOnlyClone(t *testing.T, clone string) string {
	t.Helper()
	newdir := filepath.Join(clone, "cache", "newdir")
	if err := os.MkdirAll(newdir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(clone, "cache", "old.txt"), "keep")
	write(t, filepath.Join(newdir, "keep.txt"), "yes")
	t.Cleanup(func() { writable(clone) })
	for _, d := range []string{newdir, filepath.Join(clone, "cache")} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(newdir, "probe"), nil, 0o600); err == nil {
		t.Skip("0555 is not enforced here")
	}
	return filepath.Join(newdir, "keep.txt")
}

// writable makes every directory in dir writable again, for the test's own cleanup.
func writable(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
}

func exist(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			t.Fatalf("%s is gone: %v", p, err)
		}
	}
}

// An eligible clone holding owner-controlled read-only directories goes entirely, with its sidecars; a link in
// it to a read-only directory outside goes as a link, the bytes and modes behind it untouched.
func TestReleaseRemovesReadOnlyCloneContent(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "atm-run-20261001-120000")
	external := t.TempDir()
	file := filepath.Join(external, "outside")
	write(t, file, "keep these bytes")
	if err := os.Mkdir(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(clone, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(external, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writable(external) })
	readOnlyClone(t, clone)
	write(t, clone+".pid", "1")
	before := fixtures(t, external, file)
	if err := release(clone, ""); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{clone, clone + ".lock", clone + ".pid"} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("%s outlived release: %v", p, err)
		}
	}
	if after := fixtures(t, external, file); !slices.Equal(after, before) {
		t.Fatalf("release changed what the link names: %v, want %v", after, before)
	}
}

// A clone another process still leases stays whole, read-only content and all.
func TestReleaseKeepsALeasedReadOnlyClone(t *testing.T) {
	clone := filepath.Join(t.TempDir(), "atm-run-20261001-120000")
	keep := readOnlyClone(t, clone)
	write(t, clone+".lock", "")
	lease, err := os.OpenFile(clone+".lock", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	if err := lockCloneShared(lease); err != nil {
		t.Fatal(err)
	}
	if err := release(clone, ""); err != nil {
		t.Fatal(err)
	}
	exist(t, keep, filepath.Join(clone, "cache", "old.txt"), clone+".lock")
}

// A file removal cannot unlink, an immutable one, is an error: what remains of the clone and its sidecars stay.
func TestReleaseKeepsSidecarsWhenContentCannotGo(t *testing.T) {
	chflags, err := exec.LookPath("chflags")
	if err != nil {
		t.Skip("no chflags here")
	}
	clone := filepath.Join(t.TempDir(), "atm-run-20261001-120000")
	keep := readOnlyClone(t, clone)
	if out, err := exec.Command(chflags, "uchg", keep).CombinedOutput(); err != nil {
		t.Fatalf("chflags: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(chflags, "nouchg", keep).Run() })
	write(t, clone+".delivered", "atm/fix main\n")
	write(t, clone+".pid", "1")
	fakeGH(t, "MERGED", false)
	if err := release(clone, "owner/repo"); err == nil {
		t.Fatal("release reported success with an immutable file left")
	}
	exist(t, keep, clone+".lock", clone+".delivered", clone+".pid")
}

// A clone a failed removal left without .git, inside the project's own repository, is never answered for by
// that repository: with GitHub saying its PR merged it goes; without GitHub it is kept. The project's refs and
// FETCH_HEAD stay as they were.
func TestReleaseOfAHalfRemovedCloneAsksNoEnclosingRepository(t *testing.T) {
	for _, c := range []struct {
		name string
		pr   string
	}{{"PR merged", "MERGED"}, {"no GitHub", ""}} {
		t.Run(c.name, func(t *testing.T) {
			bare := t.TempDir()
			gitT(t, bare, "init", "-q", "--bare")
			root := repo(t, bare, atmYAML)
			gitT(t, root, "push", "-q", "origin", "main")
			gitT(t, root, "branch", "atm/fix") // merged, were the project asked
			project := func() string {
				head, _ := os.ReadFile(filepath.Join(root, ".git", "FETCH_HEAD"))
				return gitT(t, root, "for-each-ref") + string(head)
			}
			was := project()
			clone := filepath.Join(root, ".atm", "clones", "atm-run-20261001-120000")
			keep := readOnlyClone(t, clone)
			write(t, clone+".delivered", "atm/fix main\n")
			repoName := ""
			if c.pr != "" {
				fakeGH(t, c.pr, false)
				repoName = "owner/repo"
			}
			if err := release(clone, repoName); err != nil {
				t.Fatal(err)
			}
			if now := project(); now != was {
				t.Fatalf("release asked the project's repository: %q, was %q", now, was)
			}
			if c.pr == "" {
				exist(t, keep, filepath.Join(clone, "cache", "old.txt"), clone+".delivered", clone+".lock")
			} else if left := clones(t, root); len(left) != 0 {
				t.Fatalf("a merged clone outlived release: %v", left)
			}
		})
	}
}
