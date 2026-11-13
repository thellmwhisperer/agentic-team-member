package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

const machineProject = `harness: codex
model: repository-model
effort: high
install: install-command
test: test-command
e2e: e2e-command
compile: compile-command
typecheck: typecheck-command
lint: lint-command
test_file: test-command {file}
test_patterns: ["**/*_test.go"]
docs_patterns: ["**/*.md", "docs/**"]
delivery: delivery-command
agent_timeout: retired-agent
check_timeout: retired-check
`

func TestMachineRepoConfig(t *testing.T) {
	for _, origin := range []string{"https://example.com/client/project.git", "git@example.com:client/project.git",
		"ssh://git@example.com:2222/client/project.git/"} {
		t.Run(origin, func(t *testing.T) { machineRepoConfig(t, origin) })
	}
}

func machineRepoConfig(t *testing.T, origin string) {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=atm", "GIT_AUTHOR_EMAIL=atm@example.com",
			"GIT_COMMITTER_NAME=atm", "GIT_COMMITTER_EMAIL=atm@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("remote", "add", "origin", origin)
	git("commit", "-q", "--allow-empty", "-m", "base without config")
	dir := filepath.Join(home, ".config", "atm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	machine := "harness: claude\nmodel: machine-model\neffort: low\nrepository_overrides:\n" +
		"  client/another:\n    test: wrong-repository\n  client/project:\n    " +
		strings.ReplaceAll(strings.TrimSpace(machineProject), "\n", "\n    ") + "\n"
	writeMachineFile(t, filepath.Join(dir, "config.yaml"), machine)
	want := config.Config{
		Agent:   config.Agent{Harness: "codex", Model: "repository-model", Effort: "medium"},
		Install: "install-command", Test: "test-command", E2E: "e2e-command", Compile: "compile-command",
		Typecheck: "typecheck-command", Lint: "lint-command", TestFile: "test-command {file}",
		TestPatterns: []string{"**/*_test.go"}, DocsPatterns: []string{"**/*.md", "docs/**"},
		Delivery: "delivery-command", Ponytail: true,
		RetiredAgentTimeout: "retired-agent", RetiredCheckTimeout: "retired-check",
	}
	flags := config.Agent{Effort: "medium"}
	assertMachineConfig(t, root, home, flags, want)
	local, err := config.Load(root, home, flags)
	if err != nil || !reflect.DeepEqual(local, want) {
		t.Fatalf("Load: %+v, %v; want %+v", local, err, want)
	}
	project := filepath.Join(root, ".atm.yaml")
	writeMachineFile(t, project, "invalid: working-tree-only\n")
	assertMachineConfig(t, root, home, flags, want)
	committed := strings.Replace(machineProject, "test: test-command\n", "test: committed-test\n", 1)
	committed = strings.Replace(committed, "harness: codex\nmodel: repository-model\neffort: high\n", "", 1)
	writeMachineFile(t, project, committed)
	git("add", ".atm.yaml")
	git("commit", "-q", "-m", "committed config wins")
	want.Test, want.Agent = "committed-test", config.Agent{Harness: "claude", Model: "machine-model", Effort: "medium"}
	assertMachineConfig(t, root, home, flags, want)
	writeMachineFile(t, project, strings.Replace(committed, "lint: lint-command\n", "", 1))
	git("add", ".atm.yaml")
	git("commit", "-q", "-m", "incomplete config")
	if _, _, err := run.BaseConfig(root, "main", home, flags); err == nil || !strings.Contains(err.Error(), "lacks lint") {
		t.Fatalf("committed incomplete config must fail: %v", err)
	}
	git("remote", "set-url", "origin", "https://example.com/client/another.git")
	if _, _, err := run.BaseConfig(root, "HEAD~2", home, flags); err == nil ||
		!strings.Contains(err.Error(), "config incomplete") {
		t.Fatalf("another repository must not inherit client/project: %v", err)
	}
}

func assertMachineConfig(t *testing.T, root, home string, flags config.Agent, want config.Config) {
	t.Helper()
	got, _, err := run.BaseConfig(root, "main", home, flags)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("BaseConfig: %+v, %v; want %+v", got, err, want)
	}
}

func writeMachineFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
