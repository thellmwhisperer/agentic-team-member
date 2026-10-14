package run

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// nested is an ignored repository with a commit, an untracked file and a read-only directory, beside old.txt.
const nested = "mkdir -p cache/nested && cd cache/nested && git init -q && echo tracked > tracked.txt && " +
	"git add . && git commit -qm t && echo untracked > untracked.txt && echo old > ../old.txt && chmod 555 ."

// nestedKept says nested is as it was: its git data, bytes and mode, and old.txt beside it.
const nestedKept = `test "$(git -C cache/nested status --porcelain)" = "?? untracked.txt" && ` +
	"is cache/nested/tracked.txt tracked && is cache/nested/untracked.txt untracked && is cache/old.txt old && " +
	"ro cache/nested && test ! -e cache/debris"

// unmadeCases are a clone as its agent finds it, old, the agent's work, work, and kept, what holds before
// install: shell lines run in the clone, $EXT a directory outside it.
var unmadeCases = []struct {
	name, old, work, kept string
	proof                 bool
}{
	{name: "new nested repository", work: "mkdir -p .tmp/go-tmp/T/001 && git -C .tmp/go-tmp/T/001 init -q && " +
		"echo x > .tmp/go-tmp/T/001/x", kept: "test ! -e .tmp"},
	{name: "new read-only module cache", work: "d=.tmp/go-mod/m@v1/.github/workflows && mkdir -p $d && " +
		"echo b > $d/build.yml && chmod 555 $d .tmp/go-mod/m@v1/.github .tmp/go-mod/m@v1", kept: "test ! -e .tmp"},
	{name: "old nested repository beside new debris", old: nested, work: "echo d > cache/debris", kept: nestedKept},
	{name: "old nested repository with new content in it", old: nested, work: "chmod 755 cache/nested && " +
		"echo h > cache/nested/helper && mkdir cache/nested/sub && echo d > cache/nested/sub/d && " +
		"chmod 555 cache/nested && echo d > cache/debris",
		kept: nestedKept + " && test ! -e cache/nested/helper && test ! -e cache/nested/sub"},
	{name: "proof cannot use new inner helper", old: nested, work: "chmod 755 cache/nested && " +
		"echo h > cache/nested/helper && chmod 555 cache/nested && echo d > cache/debris && " +
		"echo fixed > a.txt && echo 'test -e cache/nested/helper && grep -q fixed a.txt' > a_test.sh",
		kept: nestedKept + " && test ! -e cache/nested/helper", proof: true},
	{name: "old read-only directory", old: "mkdir -p cache/ro && echo old > cache/ro/old.txt && chmod 555 cache/ro",
		work: "chmod 755 cache/ro && echo d > cache/ro/debris && chmod 555 cache/ro",
		kept: "is cache/ro/old.txt old && ro cache/ro && test ! -e cache/ro/debris"},
	{name: "new read-only directory with a file to deliver",
		work: "mkdir new && echo keep > new/keep.txt && echo d > new/debris && chmod 555 new",
		kept: "is new/keep.txt keep && ro new && test ! -e new/debris"},
	{name: "links out of the clone",
		old: `mkdir "$EXT/ro" && echo sentinel > "$EXT/ro/sentinel" && chmod 555 "$EXT/ro"`,
		work: `mkdir -p .tmp/fresh cache && git -C .tmp/fresh init -q && ln -s "$EXT/ro" .tmp/fresh/out && ` +
			`ln -s "$EXT/ro" cache/link`,
		kept: `test ! -e .tmp && test ! -e cache && is "$EXT/ro/sentinel" sentinel && ro "$EXT/ro"`},
}

func TestUnmadeKeepsWhatWasAndRemovesWhatIsNew(t *testing.T) {
	for _, c := range unmadeCases {
		t.Run(c.name, func(t *testing.T) {
			dir, err := unmadeChecks(t, c.old, c.work, c.kept, c.proof)
			if c.proof {
				if err == nil || !strings.Contains(err.Error(), "fails with the fix") {
					t.Fatalf("proof using removed helper must fail after running: %v", err)
				}
			} else if err != nil {
				t.Fatalf("checks: %v", err)
			}
			if _, err := sh(dir, "set -x; test -e installed"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnmadeFailureBlocksInstallAndProof(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("an immutable file needs chflags, which only macOS gives a user")
	}
	dir, err := unmadeChecks(t, "", "mkdir cache && echo x > cache/stuck && chflags uchg cache/stuck", "true", false)
	t.Cleanup(func() { _ = exec.Command("chflags", "nouchg", filepath.Join(dir, "cache", "stuck")).Run() })
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("want the real permission error, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "installed")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("install ran after cleanup failed: %v", err)
	}
}

// unmadeChecks is the error of checks, whose install checks kept and touches installed, on a clone where old ran,
// before recorded it, then work ran as its agent: shell lines, $EXT, a directory outside the clone.
func unmadeChecks(t *testing.T, old, work, kept string, proof bool) (dir string, err error) {
	t.Helper()
	root := repo(t, "https://example.com/owner/repo.git", atmYAML)
	commitBase(t, root, ".gitignore", "cache/\n.tmp/\ndebris\n")
	runDir := checksStashRunDir(t, root)
	dir, sha := checksStashClone(t, root)
	ext := t.TempDir()
	t.Setenv("EXT", ext)
	t.Setenv("ATM_TEST_GOOS", runtime.GOOS)
	t.Cleanup(func() {
		_ = exec.Command("chmod", "-R", "u+w", root, ext).Run()
		if err := release(dir, ""); err != nil {
			t.Error(err)
		}
	})
	run := func(line string) {
		if _, err := sh(dir, line); line != "" && err != nil {
			t.Fatal(err)
		}
	}
	run(old)
	if err := before(dir); err != nil {
		t.Fatal(err)
	}
	run(work)
	c := checksStashConfig(t, root)
	c.Install = "set -x; is() { printf '%s\\n' \"$2\" | cmp - \"$1\"; }; " +
		`ro() { test "$ATM_TEST_GOOS" = windows || test -n "$(find "$1" -prune -perm 555)"; }; ` +
		kept + " && touch installed"
	typ, test := "chore", ""
	if proof {
		typ, test = "fix", "a_test.sh"
	}
	_, err = checks(dir, runDir, sha, typ, test, c)
	return dir, err
}
