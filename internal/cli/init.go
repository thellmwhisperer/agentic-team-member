package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"github.com/thellmwhisperer/agentic-team-member/internal/project"
)

// atmYAML is the .atm.yaml atm init writes. Values are JSON, which YAML reads as it is.
var atmYAML = template.Must(template.New(config.ProjectFile).Funcs(template.FuncMap{"json": func(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // && stays && in the delivery command
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}}).Parse(`# ATM's project config. It overrides the built-in defaults and ~/.config/atm/config.yaml (harness,
# model, effort, which may also be set here); flags override it.

# Shell lines run in the clone. Empty: detected from the repository.
commands:
  test: {{json .Commands.Test}}
  lint: {{json .Commands.Lint}}
  typecheck: {{json .Commands.Typecheck}}

# Patterns no line the agent adds may contain, by language.
forbidden:
{{- range $language, $patterns := .Forbidden}}
  {{$language}}: {{json $patterns}}
{{- end}}

# Seconds before ATM kills the agent, and a test run.
timeouts:
  agent: {{.Timeouts.Agent}}
  test: {{.Timeouts.Test}}

# A shell line run in the clone after a green verdict, with ATM_TITLE, ATM_ISSUE, ATM_BRANCH, ATM_CLONE,
# ATM_REPORT and ATM_PONYTAIL in its environment. Empty skips delivery.
delivery:
  command: {{json .Delivery.Command}}
`))

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Write a commented .atm.yaml with the defaults and ignore .atm/; run twice, it changes nothing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := project.Find(".")
			if err != nil {
				return err
			}
			if err := writeAtmYAML(p.Path(config.ProjectFile)); err != nil {
				return err
			}
			return ignoreAtm(p.Path(".gitignore"))
		},
	}
}

// writeAtmYAML writes the defaults to path unless a file is already there.
func writeAtmYAML(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	err = atmYAML.Execute(f, config.Defaults())
	return errors.Join(err, f.Close())
}

// ignoreAtm appends /.atm/ to the .gitignore at path unless a line already ignores .atm.
func ignoreAtm(path string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(b)
	for line := range strings.Lines(text) {
		if slices.Contains([]string{".atm", ".atm/", "/.atm", "/.atm/"}, strings.TrimSpace(line)) {
			return nil
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return os.WriteFile(path, fmt.Appendf([]byte(text), "/%s/\n", project.Dir), 0o644)
}
