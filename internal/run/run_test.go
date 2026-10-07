package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakes holds this test binary under each agent CLI's name, first on PATH in every repo test.
var fakes string

// TestMain doubles as a fake gh, which fakeGH puts on PATH, and as the fake agents in fakes.
func TestMain(m *testing.M) {
	switch name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"); name {
	case "claude", "codex", "opencode", "pi":
		os.Exit(fakeAgent(name))
	}
	if out, ok := os.LookupEnv("FAKE_GH_OUT"); ok {
		_ = os.WriteFile(os.Getenv("FAKE_GH_ARGS"), []byte(strings.Join(os.Args[1:], " ")), 0o600)
		fmt.Print(out)
		if os.Getenv("FAKE_GH_FAIL") != "" {
			os.Exit(1)
		}
		os.Exit(0)
	}
	dir, err := linkFakes()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake agents:", err)
		os.Exit(1)
	}
	fakes = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// linkFakes links this test binary under each agent CLI's name in a new directory.
func linkFakes() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "atm-fakes-")
	if err != nil {
		return "", err
	}
	for _, name := range []string{"claude", "codex", "opencode", "pi"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		link := filepath.Join(dir, name)
		if os.Symlink(exe, link) != nil { // Windows without symlink rights: a hard link keeps argv[0]
			if err := os.Link(exe, link); err != nil {
				return dir, err
			}
		}
	}
	return dir, nil
}

// fakeGH makes gh print out, failing when fail is set, and returns the file its arguments go to.
func fakeGH(t *testing.T, out string, fail bool) string {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	name := "gh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_OUT", out)
	t.Setenv("FAKE_GH_ARGS", filepath.Join(dir, "args"))
	if fail {
		t.Setenv("FAKE_GH_FAIL", "1")
	}
	return filepath.Join(dir, "args")
}

const atmYAML = `install: ""
test: sh a_test.sh
typecheck: ""
lint: ""
test_file: sh {file}
test_patterns: ["**/*_test.sh"]
docs_patterns: ["**/*.md"]
delivery: ""
`

// repo is a git repository with one commit on main, where a.txt is broken and a_test.sh passes, origin and
// .atm.yaml, the working directory for the rest of the test, whose agents are the fakes.
func repo(t *testing.T, origin, atm string) string {
	t.Helper()
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_AUTHOR_NAME", "atm")
	t.Setenv("GIT_AUTHOR_EMAIL", "atm@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "atm")
	t.Setenv("GIT_COMMITTER_EMAIL", "atm@example.com")
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitT(t, dir, "init", "-q", "-b", "main")
	for name, text := range map[string]string{"a.txt": "broken\n", "a_test.sh": "test -f a.txt\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"remote", "add", "origin", origin}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
		gitT(t, dir, args...)
	}
	if atm != "" {
		if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte(atm), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	return dir
}

// gitT runs git in dir and returns its trimmed output, failing the test when git fails.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func issueFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "issue.md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func events(t *testing.T, out io.Reader) []map[string]any {
	t.Helper()
	var evs []map[string]any
	dec := json.NewDecoder(out)
	for dec.More() {
		var ev map[string]any
		if err := dec.Decode(&ev); err != nil {
			t.Fatal(err)
		}
		evs = append(evs, ev)
	}
	return evs
}

// issueStep is the issue step's end event, after checking its start came first and nothing followed a failure.
func issueStep(t *testing.T, out io.Reader) map[string]any {
	t.Helper()
	evs := events(t, out)
	if len(evs) < 2 || evs[0]["step"] != "issue" || evs[0]["state"] != "started" || evs[1]["step"] != "issue" ||
		evs[1]["state"] == "failed" && len(evs) != 2 {
		t.Fatalf("want the issue step's start and end, got %v", evs)
	}
	return evs[1]
}

func TestRunReadsIssueFile(t *testing.T) {
	for _, c := range []struct {
		name, issue string
	}{
		{name: "LF", issue: "# Retry on timeout\n\nType: hotfix\n\nRetry once.\n"},
		{name: "CRLF", issue: "# Retry on timeout\r\n\r\nType: hotfix  \r\n\r\nRetry once.\r\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo(t, "https://example.com/owner/repo.git", atmYAML)
			path := issueFile(t, c.issue)
			var out bytes.Buffer
			if err := Run([]string{path}, &out); err != nil {
				t.Fatal(err)
			}
			end := issueStep(t, &out)
			if end["state"] != "passed" || end["title"] != "Retry on timeout" || end["type"] != "fix" {
				t.Fatalf("end event: %v", end)
			}
			if _, ok := end["number"]; ok {
				t.Fatalf("a file issue has no number: %v", end)
			}
		})
	}
}

func TestRunReadsGitHubIssueFromOrigin(t *testing.T) {
	repo(t, "git@github.com:owner/repo.git", atmYAML)
	args := fakeGH(t, `{"title":"Retry","body":"Type: feature\nRetry once."}`, false)
	var out bytes.Buffer
	if err := Run([]string{"--model", "m", "7"}, &out); err != nil {
		t.Fatal(err)
	}
	end := issueStep(t, &out)
	if end["state"] != "passed" || end["number"] != float64(7) || end["type"] != "feature" {
		t.Fatalf("end event: %v", end)
	}
	got, _ := os.ReadFile(args)
	if string(got) != "issue view 7 --repo owner/repo --json title,body" {
		t.Fatalf("gh args: %q", got)
	}
}

func TestRunDiesWhenIssueIsUnusable(t *testing.T) {
	cases := []struct {
		name, file, gh, why string
		viaGH, ghFails      bool
	}{
		{name: "unreadable file", why: "issue"},
		{name: "gh fails", viaGH: true, gh: "not found", ghFails: true, why: "gh issue view"},
		{name: "gh returns no JSON", viaGH: true, why: "JSON"},
		{name: "empty title", file: "# \nType: fix\nbody", why: "title"},
		{name: "empty body", file: "# Title\n", why: "body"},
		{name: "missing type", file: "# Title\nbody", why: "task type"},
		{name: "unknown type", viaGH: true, gh: `{"title":"T","body":"Type: bugfix\nbody"}`, why: "bugfix"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo(t, "https://github.com/owner/repo", atmYAML)
			arg := filepath.Join(t.TempDir(), "missing.md")
			if c.file != "" {
				arg = issueFile(t, c.file)
			}
			if c.viaGH {
				fakeGH(t, c.gh, c.ghFails)
				arg = "3"
			}
			var out bytes.Buffer
			err := Run([]string{arg}, &out)
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			}
			if end := issueStep(t, &out); end["state"] != "failed" || end["error"] != err.Error() {
				t.Fatalf("end event: %v", end)
			}
		})
	}
}

func TestRunNumberNeedsGitHubOrigin(t *testing.T) {
	repo(t, "https://gitlab.com/owner/repo.git", atmYAML)
	var out bytes.Buffer
	if err := Run([]string{"7"}, &out); err == nil || !strings.Contains(err.Error(), "GitHub") {
		t.Fatalf("want a GitHub origin error, got %v", err)
	}
}

func TestRunDiesOnIncompleteConfigBeforeReadingIssue(t *testing.T) {
	repo(t, "https://github.com/owner/repo", "test: go test ./...\n")
	var out bytes.Buffer
	err := Run([]string{issueFile(t, "# T\nType: fix\nbody")}, &out)
	if err == nil || !strings.Contains(err.Error(), "config incomplete") {
		t.Fatalf("want config incomplete, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("no step should start: %s", out.String())
	}
}
