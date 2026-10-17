package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// coverDiffFail is the two ways out, in the order the issue fixes them.
const coverDiffFail = "cover-diff: these changed lines are not executed by any end-to-end run of " +
	"the real atm binary. Delete them, or write the end-to-end test that reaches them from a real atm command."

// A scratch module whose binary is compiled with -cover and run end to end with argument a must make
// cover-diff.sh fail naming exactly the changed lines of the b branch, its statement and the comment
// inside it, and nothing else: not the a line, not the comment above main (outside every block), not
// the blank line the comment brings (outside every block), not the _test.go line (excluded). The same
// tree and the same coverage data must give the same output twice.
func TestCoverDiffNamesTheUncoveredChangedLines(t *testing.T) {
	dir, covdir, base := scratchModule(t)
	coveredRun(t, dir, covdir)

	script, err := filepath.Abs(filepath.Join("..", "scripts", "cover-diff.sh"))
	if err != nil {
		t.Fatal(err)
	}
	check := func() string {
		cmd := exec.Command(script)
		cmd.Dir = dir
		cmd.Env = append(slices.DeleteFunc(os.Environ(), func(s string) bool {
			return strings.HasPrefix(s, "GOCOVERDIR=") || strings.HasPrefix(s, "ATM_COVER_BASE=")
		}), "GOCOVERDIR="+covdir, "ATM_COVER_BASE="+base)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		if err == nil {
			t.Fatalf("cover-diff.sh passed; want it to fail naming the b lines:\n%s", out.String())
		}
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("cover-diff.sh did not run: %v", err)
		}
		return out.String()
	}
	first := check()
	if want := coverDiffFail + "\nmain.go:16\nmain.go:17\n"; first != want {
		t.Fatalf("cover-diff.sh output =\n%s\nwant\n%s", first, want)
	}
	if second := check(); second != first {
		t.Fatalf("cover-diff.sh printed twice different output:\n%s\n---\n%s", first, second)
	}
}

// scratchModule commits the base of a two-branch main and leaves a working tree that changes both
// statements, adds a comment inside the b branch, one above main and a _test.go file, with an empty
// GOCOVERDIR beside it.
func scratchModule(t *testing.T) (dir, covdir, base string) {
	t.Helper()
	dir = t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "cover-diff@example.com")
	gitRun(t, dir, "config", "user.name", "cover-diff")
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/coverdiff\n\ngo 1.27\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main

import "os"

func main() {
	arg := ""
	if len(os.Args) > 1 {
		arg = os.Args[1]
	}
	if arg == "a" {
		print("a\n")
	}
	if arg == "b" {
		print("b\n")
	}
}
`)
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "base")

	writeFile(t, filepath.Join(dir, "main.go"), `package main

import "os"

// added above main

func main() {
	arg := ""
	if len(os.Args) > 1 {
		arg = os.Args[1]
	}
	if arg == "a" {
		print("a changed\n")
	}
	if arg == "b" {
		print("b changed\n")
		// only with b
	}
}
`)
	writeFile(t, filepath.Join(dir, "extra_test.go"), "package main\n")
	gitRun(t, dir, "add", "extra_test.go")

	covdir = filepath.Join(dir, "covdata")
	if err := os.Mkdir(covdir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, covdir, strings.TrimSpace(gitOut(t, dir, "rev-parse", "HEAD"))
}

// coveredRun is the end-to-end run of the scratch module: its binary, compiled with -cover, runs
// with argument a and leaves its counters in covdir.
func coveredRun(t *testing.T, dir, covdir string) {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	build := exec.Command("go", "build", "-cover", "-coverpkg=./...", "-o", bin, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -cover: %v\n%s", err, out)
	}
	run := exec.Command(bin, "a")
	run.Dir = dir
	run.Env = append(slices.DeleteFunc(os.Environ(), func(s string) bool {
		return strings.HasPrefix(s, "GOCOVERDIR=")
	}), "GOCOVERDIR="+covdir)
	if out, err := run.CombinedOutput(); err != nil || string(out) != "a changed\n" {
		t.Fatalf("bin a = %q, %v; want the a path end to end", out, err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
