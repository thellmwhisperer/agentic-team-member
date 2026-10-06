package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const complete = `install: npm ci
test: npm test
typecheck: ""
lint:
test_file: npx vitest run {file}
test_patterns: ["**/*.test.ts"]
docs_patterns: []
delivery: ""
`

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadLayersEachOverrideTheOneBefore(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	write(t, filepath.Join(home, ".config", "atm", "config.yaml"), "harness: codex\nmodel: gpt-6\neffort: high\n")
	write(t, filepath.Join(root, ".atm.yaml"), complete+"model: gpt-6-mini\n")

	c, err := Load(root, home, Agent{Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Agent:   Agent{Harness: "codex", Model: "gpt-6-mini", Effort: "low"},
		Install: "npm ci", Test: "npm test", TestFile: "npx vitest run {file}",
		TestPatterns: []string{"**/*.test.ts"}, DocsPatterns: []string{},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}

func TestLoadDefaultsWithoutGlobalFile(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".atm.yaml"), complete)
	c, err := Load(root, t.TempDir(), Agent{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness != "claude" || c.Model != "" || c.Effort != "" {
		t.Fatalf("defaults: got %+v", c.Agent)
	}
}

func TestLoadMissingKeyIsConfigIncomplete(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".atm.yaml"), strings.Replace(complete, "lint:\n", "", 1))
	_, err := Load(root, t.TempDir(), Agent{})
	if err == nil || !strings.Contains(err.Error(), "config incomplete") || !strings.Contains(err.Error(), "lint") {
		t.Fatalf("want config incomplete naming lint, got %v", err)
	}
}

func TestLoadWithoutProjectFileIsConfigIncomplete(t *testing.T) {
	_, err := Load(t.TempDir(), t.TempDir(), Agent{})
	if err == nil || !strings.Contains(err.Error(), "config incomplete") {
		t.Fatalf("want config incomplete, got %v", err)
	}
}

func TestLoadRejectsUnknownKeysAndProjectKeysInGlobalFile(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, ".atm.yaml"), complete+"runner: pytest\n")
	if _, err := Load(root, home, Agent{}); err == nil || !strings.Contains(err.Error(), "runner") {
		t.Fatalf("unknown project key: got %v", err)
	}
	write(t, filepath.Join(root, ".atm.yaml"), complete)
	write(t, filepath.Join(home, ".config", "atm", "config.yaml"), "test: pytest\n")
	if _, err := Load(root, home, Agent{}); err == nil || !strings.Contains(err.Error(), "test") {
		t.Fatalf("project key in global file: got %v", err)
	}
}

func TestLoadTestFileNeedsFileOrDir(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".atm.yaml"), strings.Replace(complete, "{file}", "", 1))
	if _, err := Load(root, t.TempDir(), Agent{}); err == nil || !strings.Contains(err.Error(), "test_file") {
		t.Fatalf("want a test_file error, got %v", err)
	}
}

func TestLoadRejectsUnknownHarness(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".atm.yaml"), complete)
	if _, err := Load(root, t.TempDir(), Agent{Harness: "aider"}); err == nil || !strings.Contains(err.Error(), "aider") {
		t.Fatalf("want an unknown harness error, got %v", err)
	}
}
