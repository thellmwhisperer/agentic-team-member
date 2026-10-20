package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

const briefYAML = `install: "true"
test: sh a_test.sh
typecheck: sh -n a_test.sh
lint: ""
test_file: sh {file}
test_patterns: ["**/*_test.sh"]
docs_patterns: ["**/*.md", "docs/**"]
delivery: ""
`

// These SCOPE lines keep each agent in its allowed directories.
const (
	reachScope = "- Read, search and run only inside this clone: never the repository around it, `.atm/runs/` " +
		"or another clone."
	workerReachScope = "- Read, search and run only inside this clone; you may also read and write your " +
		"scratch directory below.\n" +
		"  Never access the repository around it, the rest of `.atm/runs/` or another clone."
	netScope = "- No network, except the project's package manager installing a dependency."
)

func TestRunWritesTheContractForTheTaskType(t *testing.T) {
	for typ, proof := range map[string]string{
		"":           "Implement the issue",
		"fix":        "Implement the issue",
		"feature":    "Implement the issue",
		"greenfield": "Implement the issue",
		"refactor":   "Implement the issue",
		"tests":      "Implement the issue",
		"docs":       "Implement the issue",
		"chore":      "Implement the issue",
	} {
		t.Run(typ, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", briefYAML)
			issue := "Retry once on timeout."
			if typ != "" {
				issue += "\n\nType: " + typ
			}
			var out bytes.Buffer
			if err := Run("t", []string{issueFile(t, "# Retry\n"+issue+"\n")}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			evs := events(t, &out)
			if evs[5]["step"] != "contract" || evs[5]["state"] != "passed" {
				t.Fatalf("want the contract step passed, got %v", evs)
			}
			b, err := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "brief.md"))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.ReplaceAll(string(b), "\r\n", "\n")
			first, _, _ := strings.Cut(got, "\n")
			if !strings.Contains(first, "`AGENTS.md`") || !strings.Contains(first, "this brief wins") {
				t.Errorf("first line does not point to AGENTS.md: %q", first)
			}
			for _, want := range []string{"The issue, verbatim:", "### Retry\n\n" + issue + "\n", proof,
				"`**/*_test.sh`", "`**/*.md`, `docs/**`",
				"- Test: the tests of each package", "- Typecheck: `sh -n a_test.sh`",
				"- Lint: none", "`ponytail` skill", "`git commit`, `git push`, `git rebase`, any `gh` command",
				"`sleep` in tests", `"test_file"`, "summary, as a note", workerReachScope, netScope} {
				if !strings.Contains(got, want) {
					t.Errorf("brief.md lacks %q:\n%s", want, got)
				}
			}
			for _, unwanted := range []string{"CLAUDE.md", "Allowed paths", "{{", "`sh a_test.sh`", "Task type:", "red test"} {
				if strings.Contains(got, unwanted) {
					t.Errorf("brief.md has %q:\n%s", unwanted, got)
				}
			}
		})
	}
}

// The suite runs once per issue, in Checks and in CI: neither ATM's own brief nor its gate runs the project's
// test command.
func TestSuiteRunsOnce(t *testing.T) {
	root := filepath.Join("..", "..")
	var c config.Config
	b, err := os.ReadFile(filepath.Join(root, ".atm.yaml"))
	if err == nil {
		err = yaml.Unmarshal(b, &c)
	}
	if err != nil || c.Test != "scripts/test.sh" || c.TestFile == "" {
		t.Fatalf("want ATM's .atm.yaml with test scripts/test.sh and a test_file, got %+v, %v", c, err)
	}
	for _, typ := range []string{"", "fix", "feature", "greenfield", "refactor", "tests", "docs", "chore", "unknown"} {
		got, err := brief(Issue{Title: "Retry", Body: "Retry once.\nType: " + typ}, c, "/scratch")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, c.Test) || !strings.Contains(got, "- Test: the tests of each package") {
			t.Errorf("%s brief: want package tests in VERIFY and not %s:\n%s", typ, c.Test, got)
		}
	}
	var gate struct{ Commands map[string]string }
	b, err = os.ReadFile(filepath.Join(root, ".no-mistakes.yaml"))
	if err == nil {
		err = yaml.Unmarshal(b, &gate)
	}
	if test, ok := gate.Commands["test"]; err != nil || ok {
		t.Fatalf("want .no-mistakes.yaml without a test command, got test %q, %v", test, err)
	}
}
