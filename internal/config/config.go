// Package config is ATM's configuration in layers, each overriding the one before (#150 point 5): the
// built-in defaults, the global ~/.config/atm/config.yaml, the project's .atm.yaml, then flags.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

// Config is every key ATM reads. Any key may sit in any layer; the global file is meant for the agent
// (harness, model, effort), .atm.yaml for the project.
type Config struct {
	Harness  string `yaml:"harness"`
	Model    string `yaml:"model"`  // empty: the harness's default
	Effort   string `yaml:"effort"` // empty: the harness's default
	Commands struct {
		Test      string `yaml:"test"`
		Lint      string `yaml:"lint"`
		Typecheck string `yaml:"typecheck"`
	} `yaml:"commands"`
	Forbidden map[string][]string `yaml:"forbidden"` // by language
	Timeouts  struct {
		Agent int `yaml:"agent"` // seconds
		Test  int `yaml:"test"`  // seconds
	} `yaml:"timeouts"`
	Delivery struct {
		Command string `yaml:"command"`
	} `yaml:"delivery"`
}

// ProjectFile is the project layer, in the project root.
const ProjectFile = ".atm.yaml"

var (
	harnesses = []string{"claude", "codex", "opencode", "pi"}
	efforts   = []string{"", "low", "medium", "high", "xhigh", "max"}
)

// Defaults is the first layer.
func Defaults() Config {
	var c Config
	c.Harness = "claude"
	c.Forbidden = map[string][]string{
		"python": {"type: ignore", "noqa"},
		"typescript": {"as any", "as unknown as", "as never", "{} as", ": any", "eslint-disable", "@ts-ignore",
			"@ts-expect-error"},
	}
	c.Timeouts.Agent, c.Timeouts.Test = 1800, 300
	c.Delivery.Command = `no-mistakes init && no-mistakes axi run --intent "$ATM_TITLE" ` +
		`${ATM_ISSUE:+--closes "$ATM_ISSUE"} --wait 2h`
	return c
}

// AddFlags defines the flags that override the files.
func AddFlags(flags *pflag.FlagSet) {
	flags.String("harness", "", "agent CLI: claude, codex, opencode or pi")
	flags.String("model", "", "the agent's model")
	flags.String("effort", "", "the agent's effort: low, medium, high, xhigh or max")
}

// Load is the defaults, overridden by the global file, the .atm.yaml in root (none when root is empty)
// and the flags set in flags.
func Load(root string, flags *pflag.FlagSet) (Config, error) {
	c := Defaults()
	paths := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "atm", "config.yaml"))
	}
	if root != "" {
		paths = append(paths, filepath.Join(root, ProjectFile))
	}
	for _, path := range paths {
		if err := layer(&c, path); err != nil {
			return c, err
		}
	}
	for name, value := range map[string]*string{"harness": &c.Harness, "model": &c.Model, "effort": &c.Effort} {
		if flags.Changed(name) {
			*value, _ = flags.GetString(name)
		}
	}
	return c, c.validate()
}

// layer decodes the file at path, if any, over c. A key Config lacks is an error, not a silent no-op.
func layer(c *Config, path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (c Config) validate() error {
	switch {
	case !slices.Contains(harnesses, c.Harness):
		return fmt.Errorf("harness %q: want one of %v", c.Harness, harnesses)
	case !slices.Contains(efforts, c.Effort):
		return fmt.Errorf("effort %q: want one of %v", c.Effort, efforts[1:])
	case c.Timeouts.Agent <= 0 || c.Timeouts.Test <= 0:
		return fmt.Errorf("timeouts must be positive seconds, got agent %d, test %d", c.Timeouts.Agent,
			c.Timeouts.Test)
	}
	return nil
}
