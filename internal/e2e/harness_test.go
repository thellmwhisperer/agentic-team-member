// Package e2e is ATM's behaviour contract (#150 step 1): the atm binary, run in a temp git repository
// against cmd/fakeagent linked under every agent's name and gh. Each later step of the port unskips the
// tests that name it and may turn none red.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var (
	atmBin  string // the atm under test, built once per package
	fakeDir string // claude, codex, opencode, pi and gh: all of them cmd/fakeagent
	exe     string // ".exe" on Windows
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "atm-e2e-")
	code := 1
	if err == nil {
		err = build(dir)
	}
	if err == nil {
		code = m.Run()
	} else {
		fmt.Fprintln(os.Stderr, "e2e:", err)
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func build(dir string) error {
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	atmBin, fakeDir = filepath.Join(dir, "atm"+exe), filepath.Join(dir, "fakes")
	fake := filepath.Join(dir, "fakeagent"+exe)
	for out, pkg := range map[string]string{atmBin: "../../cmd/atm", fake: "../../cmd/fakeagent"} {
		if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			return fmt.Errorf("go build %s: %v\n%s", pkg, err, b)
		}
	}
	if err := os.Mkdir(fakeDir, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"claude", "codex", "opencode", "pi", "gh"} {
		link := filepath.Join(fakeDir, name+exe)
		if os.Symlink(fake, link) != nil { // Windows without symlink rights: a hard link keeps argv[0]
			if err := os.Link(fake, link); err != nil {
				return err
			}
		}
	}
	return nil
}

// target is the Python fakes' repository: calc.py whose add returns a - b, one test, committed on main.
func target(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for path, text := range map[string]string{
		"calc.py":           "def add(a, b):\n    return a - b\n\n\ndef mul(a, b):\n    return abs(a * b)\n",
		"other.py":          "def other():\n    return 1\n",
		"tests/test_mul.py": "from calc import mul\n\n\ndef test_mul():\n    assert mul(2, 3) == 6\n",
		"pyproject.toml":    "[project]\nname = \"calc\"\nversion = \"0\"\n\n[tool.pytest.ini_options]\n",
		".gitignore":        "__pycache__/\n.pytest_cache/\n",
	} {
		writeFile(t, filepath.Join(repo, path), text)
	}
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "init")
	return repo
}

// issue is the e2e issue; its body is the criterion the follow-up scenario names.
const issue = "# add returns the difference\n\nadd(2, 3) returns -1 instead of 5.\n"

func issueFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "issue.md")
	writeFile(t, path, text)
	return path
}

// fakes is the environment of a process whose agent plays scenario: the fakes first in PATH, a fresh log.
func fakes(t *testing.T, scenario string) (env []string, log string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "fakeagent.jsonl")
	path := fakeDir + string(os.PathListSeparator) + os.Getenv("PATH")
	return append(os.Environ(), "PATH="+path, "FAKEAGENT_SCENARIO="+scenario, "FAKEAGENT_LOG="+log), log
}

type invocation struct {
	Name  string   `json:"name"`
	Args  []string `json:"args"`
	Brief string   `json:"brief"`
}

// invocations reads the fakes' log: one line per call, in order.
func invocations(t *testing.T, log string) []invocation {
	t.Helper()
	f, err := os.Open(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var calls []invocation
	lines := bufio.NewScanner(f)
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		var call invocation
		if err := json.Unmarshal(lines.Bytes(), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

// atm runs the atm under test in dir and returns its stdout and exit code; stderr goes to the test log.
func atm(t *testing.T, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var stdout, stderr strings.Builder
	cmd := exec.CommandContext(ctx, atmBin, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, &stdout, &stderr
	err := cmd.Run()
	if stderr.Len() > 0 {
		t.Logf("atm %v stderr:\n%s", args, stderr.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("atm %v did not finish in 2 minutes:\n%s", args, stdout.String())
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return stdout.String(), cmd.ProcessState.ExitCode()
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cfg := []string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=" + os.DevNull}
	out, err := exec.Command("git", append(append(cfg, "-C", dir), args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
