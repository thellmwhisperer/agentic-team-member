// Package config is ATM's configuration in layers, each overriding the one before (#150 point 5): the
// built-in defaults, ~/.config/atm/config.yaml, the repository's .atm.yaml, then the flags of one run.
package config

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Agent is what the global file and the flags set. Empty model or effort: the harness's default.
type Agent struct {
	Harness string `yaml:"harness"`
	Model   string `yaml:"model"`
	Effort  string `yaml:"effort"`
}

// Config is every key ATM reads. Nothing in it is per language: the repository declares its commands.
type Config struct {
	Agent        `yaml:",inline"`
	Install      string   `yaml:"install"`
	Test         string   `yaml:"test"`
	Typecheck    string   `yaml:"typecheck"`
	Lint         string   `yaml:"lint"`
	TestFile     string   `yaml:"test_file"` // runs one test: {file} or {dir}
	TestPatterns []string `yaml:"test_patterns"`
	DocsPatterns []string `yaml:"docs_patterns"`
	Delivery     string   `yaml:"delivery"`
}

// required is every key .atm.yaml must declare; an empty value is a deliberate "none".
var required = []string{"install", "test", "typecheck", "lint", "test_file", "test_patterns", "docs_patterns",
	"delivery"}

// Load is the defaults, overridden by home's .config/atm/config.yaml, root's .atm.yaml and the non-empty
// fields of flags.
func Load(root, home string, flags Agent) (Config, error) {
	c := Config{Agent: Agent{Harness: "claude"}}
	if _, err := decode(filepath.Join(home, ".config", "atm", "config.yaml"), &c.Agent); err != nil {
		return c, err
	}
	project := filepath.Join(root, ".atm.yaml")
	keys, err := decode(project, &c)
	if err != nil {
		return c, err
	}
	var missing []string
	for _, k := range required {
		if _, ok := keys[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("config incomplete: %s lacks %s", project, strings.Join(missing, ", "))
	}
	if c.TestFile != "" && !strings.Contains(c.TestFile, "{file}") && !strings.Contains(c.TestFile, "{dir}") {
		return c, fmt.Errorf("test_file %q: want {file} or {dir} in it", c.TestFile)
	}
	c.Harness, c.Model, c.Effort = cmp.Or(flags.Harness, c.Harness), cmp.Or(flags.Model, c.Model),
		cmp.Or(flags.Effort, c.Effort)
	return c, nil
}

// decode decodes the file at path, if any, over v and returns its top-level keys. A key v lacks is an error.
func decode(path string, v any) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys := map[string]any{}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &keys); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return keys, nil
}
