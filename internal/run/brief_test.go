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
	netScope   = "- No network, except the project's package manager installing a dependency."
)

func TestRunWritesTheContractForTheTaskType(t *testing.T) {
	for typ, proof := range map[string]string{
		"fix":        "fails today on behaviour",
		"feature":    "possibly on a missing symbol",
		"greenfield": "possibly on a missing module",
		"refactor":   "the full suite passes before and after",
		"tests":      "passes on the base commit and after",
		"docs":       "do not touch source or tests",
		"chore":      "Reinstall dependencies (`true`)",
	} {
		t.Run(typ, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", briefYAML)
			issue := "Retry once on timeout.\n\nType: " + typ
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
			for _, want := range []string{"Task type: " + typ, "### Retry\n\n" + issue + "\n", proof,
				"`**/*_test.sh`", "`**/*.md`, `docs/**`",
				"- Test: the red test, through `sh {file}`, and the tests of each package", "- Typecheck: `sh -n a_test.sh`",
				"- Lint: none", "`ponytail` skill", "`git commit`, `git push`, `git rebase`, any `gh` command",
				"`sleep` in tests", `"test_file"`, `"follow_ups"`, workerReachScope, netScope} {
				if !strings.Contains(got, want) {
					t.Errorf("brief.md lacks %q:\n%s", want, got)
				}
			}
			for _, unwanted := range []string{"CLAUDE.md", "Allowed paths", "{{", "`sh a_test.sh`"} {
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
	for _, typ := range taskTypes {
		got, err := brief(Issue{Title: "Retry", Body: "Retry once.", Type: typ}, c, nil, "/scratch")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, c.Test) || !strings.Contains(got, "- Test: the red test, through `"+c.TestFile+"`") {
			t.Errorf("%s brief: want VERIFY with the red test and not %s:\n%s", typ, c.Test, got)
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
