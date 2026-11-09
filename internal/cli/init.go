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

// template is .atm.yaml with every key config requires and the default delivery.
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
# Runs one test: {file} is its path, {dir} its directory, {tests} selects Go tests in that file
# e.g. npx vitest run {file} or go test -run {tests} {dir}
test_file: ""
# Globs of the test files, e.g. ["**/*.test.ts"]
test_patterns: []
# Globs of the documentation files, e.g. ["**/*.md"]
docs_patterns: []
# Launches the gate; ATM's screen paints its steps and log.
delivery: sh scripts/deliver.sh
`

const deliveryScript = `#!/bin/sh
# ATM's delivery: every run works in a fresh clone, so no-mistakes is initialised there first.
# --wait 24h: axi run holds until the gate stops for an answer or ends; its default 8 minute hold would end this
# command with exit 1 while the gate goes on, and the run would take Delivery as failed.
no-mistakes init || exit 1
exec no-mistakes axi run --intent "$ATM_INTENT" --wait 24h
`

// initCmd writes the default config and delivery unless they exist, and ignores /.atm/.
func initCmd() *cobra.Command {
	return &cobra.Command{Use: "init", Short: "Write .atm.yaml to fill and ignore .atm/", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			root, err := toplevel()
			if err != nil {
				return err
			}
			for _, file := range []struct{ name, text string }{
				{".atm.yaml", template}, {"scripts/deliver.sh", deliveryScript},
			} {
				path := filepath.Join(root, file.name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
				if err == nil {
					_, err = f.WriteString(file.text)
					err = errors.Join(err, f.Close())
				}
				if err != nil && !errors.Is(err, fs.ErrExist) {
					return err
				}
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
