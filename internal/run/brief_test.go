package run

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const briefYAML = `install: go mod download
test: go test ./...
typecheck: go vet ./...
lint: ""
test_file: go test {dir}
test_patterns: ["**/*_test.go"]
docs_patterns: ["**/*.md", "docs/**"]
delivery: ""
`

func TestRunWritesTheContractForTheTaskType(t *testing.T) {
	for typ, proof := range map[string]string{
		"fix":        "fails today on behaviour",
		"feature":    "possibly on a missing symbol",
		"greenfield": "possibly on a missing module",
		"refactor":   "the full suite passes before and after",
		"tests":      "passes on the base commit and after",
		"docs":       "do not touch source or tests",
		"chore":      "Reinstall dependencies (`go mod download`)",
	} {
		t.Run(typ, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", briefYAML)
			issue := "Retry once on timeout.\n\nType: " + typ
			var out bytes.Buffer
			if err := Run([]string{issueFile(t, "# Retry\n"+issue+"\n")}, &out); err != nil {
				t.Fatal(err)
			}
			evs := events(t, &out)
			if last := evs[len(evs)-1]; last["step"] != "contract" || last["state"] != "passed" {
				t.Fatalf("want the contract step passed last, got %v", evs)
			}
			b, err := os.ReadFile(filepath.Join(root, ".atm", "brief.md"))
			if err != nil {
				t.Fatal(err)
			}
			got := string(b)
			first, _, _ := strings.Cut(got, "\n")
			if !strings.Contains(first, "`AGENTS.md`") || !strings.Contains(first, "this brief wins") {
				t.Errorf("first line does not point to AGENTS.md: %q", first)
			}
			for _, want := range []string{"Task type: " + typ, "### Retry\n\n" + issue + "\n", proof,
				"`**/*_test.go`", "`**/*.md`, `docs/**`", "- Test: `go test ./...`", "- Typecheck: `go vet ./...`",
				"- Lint: none", "`ponytail` skill", "`git commit`, `git push`, `git rebase`, any `gh` command",
				"`sleep` in tests", `"test_file"`, `"follow_ups"`} {
				if !strings.Contains(got, want) {
					t.Errorf("brief.md lacks %q:\n%s", want, got)
				}
			}
			for _, unwanted := range []string{"CLAUDE.md", "Allowed paths", "{{"} {
				if strings.Contains(got, unwanted) {
					t.Errorf("brief.md has %q:\n%s", unwanted, got)
				}
			}
		})
	}
}
