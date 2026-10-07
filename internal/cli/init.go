package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// template is .atm.yaml with every key config requires, empty for the project to fill.
const template = `# ATM's configuration for this repository.
# Every key is required; an empty value is a deliberate "none".
# harness, model and effort may go here too, over ~/.config/atm/config.yaml.

# Installs the dependencies in a fresh clone, e.g. npm ci
install: ""
# Runs the whole test suite, e.g. npm test
test: ""
# Typechecks, e.g. npx tsc --noEmit
typecheck: ""
# Lints, e.g. npx eslint .
lint: ""
# Runs one test: {file} is its path, {dir} its directory, e.g. npx vitest run {file}
test_file: ""
# Globs of the test files, e.g. ["**/*.test.ts"]
test_patterns: []
# Globs of the documentation files, e.g. ["**/*.md"]
docs_patterns: []
# Shell line that delivers the run's branch from its clone, e.g. a push and a PR
delivery: ""
`

// initCmd is atm init: .atm.yaml from template unless there is one, and /.atm/ in .gitignore unless it is.
func initCmd() *cobra.Command {
	return &cobra.Command{Use: "init", Short: "Write .atm.yaml to fill and ignore .atm/", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := toplevel()
			if err != nil {
				return err
			}
			f, err := os.OpenFile(filepath.Join(root, ".atm.yaml"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err == nil {
				_, err = f.WriteString(template)
				err = errors.Join(err, f.Close())
			}
			if err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			return ignore(filepath.Join(root, ".gitignore"), "/.atm/")
		}}
}

// ignore appends line to the .gitignore at path unless it has it.
func ignore(path, line string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(b)
	if slices.Contains(strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), line) {
		return nil
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return os.WriteFile(path, []byte(text+line+"\n"), 0o644)
}
