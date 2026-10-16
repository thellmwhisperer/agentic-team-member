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
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Agent is the agent selection shared by configuration layers. Empty model or effort: the harness's default.
type Agent struct {
	Harness string `yaml:"harness"`
	Model   string `yaml:"model"`
	Effort  string `yaml:"effort"`
}

// Config is the repository configuration. Nothing in it is per language: the repository declares its commands.
type Config struct {
	Agent        `yaml:",inline"`
	Install      string   `yaml:"install"`
	Test         string   `yaml:"test"`
	Typecheck    string   `yaml:"typecheck"`
	Lint         string   `yaml:"lint"`
	TestFile     string   `yaml:"test_file"` // runs one test: {file} or {dir}; {tests} selects Go tests in that file
	TestPatterns []string `yaml:"test_patterns"`
	DocsPatterns []string `yaml:"docs_patterns"`
	Delivery     string   `yaml:"delivery"`
	AgentTimeout Timeout  `yaml:"agent_timeout"` // optional; 0 when absent, the run's default
}

// Timeout is agent_timeout: a positive duration in Go's syntax, as 45m.
type Timeout time.Duration

// UnmarshalYAML parses n with time.ParseDuration. yaml never calls it for a null, which Load rejects.
func (d *Timeout) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil || v <= 0 {
		return fmt.Errorf("agent_timeout %q: want a positive duration, as 45m or 1h", n.Value)
	}
	*d = Timeout(v)
	return nil
}

// ACP is the agents ATM speaks ACP with, by harness: the command line it runs in the clone, and, since it takes
// its model and effort from its own config, the error either fails the run with (rule 8).
var ACP = map[string]struct {
	Argv  []string
	Unset string
}{
	"hermes": {[]string{"hermes", "--ignore-rules", "acp"},
		"hermes takes model and effort from its own config; unset them in .atm.yaml"},
}

// required is every key .atm.yaml must declare; an empty value is a deliberate "none".
var required = []string{"install", "test", "typecheck", "lint", "test_file", "test_patterns", "docs_patterns",
	"delivery"}

// machine is ~/.config/atm/config.yaml: the agent, and the herdr workspace a follow opens a tab in.
type machine struct {
	Harness, Model, Effort string
	HerdrWorkspace         string `yaml:"herdr_workspace"`
}

// HerdrWorkspace is home's herdr_workspace, empty when unset. A missing file is empty, not an error.
func HerdrWorkspace(home string) (string, error) {
	m, err := readMachine(home)
	return m.HerdrWorkspace, err
}

func readMachine(home string) (machine, error) {
	m := machine{Harness: "claude"}
	_, err := decode(filepath.Join(home, ".config", "atm", "config.yaml"), &m)
	return m, err
}

// Load is the defaults, overridden by home's .config/atm/config.yaml, root's .atm.yaml and the non-empty
// fields of flags.
func Load(root, home string, flags Agent) (Config, error) {
	m, err := readMachine(home)
	if err != nil {
		return Config{}, err
	}
	c := Config{Agent: Agent{Harness: m.Harness, Model: m.Model, Effort: m.Effort}}
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
	if _, ok := keys["agent_timeout"]; ok && c.AgentTimeout == 0 {
		return c, errors.New("agent_timeout is empty: want a positive duration, as 45m or 1h")
	}
	if c.TestFile != "" && !strings.Contains(c.TestFile, "{file}") && !strings.Contains(c.TestFile, "{dir}") {
		return c, fmt.Errorf("test_file %q: want {file} or {dir} in it", c.TestFile)
	}
	c.Harness, c.Model, c.Effort = cmp.Or(flags.Harness, c.Harness), cmp.Or(flags.Model, c.Model),
		cmp.Or(flags.Effort, c.Effort)
	return c, harnessError(c.Agent)
}

// harnessError is why a's harness cannot run as a sets it, nil when it can.
func harnessError(a Agent) error {
	acp, isACP := ACP[a.Harness]
	switch {
	case !slices.Contains([]string{"claude", "codex", "opencode", "pi", "cursor"}, a.Harness) && !isACP:
		return fmt.Errorf("harness %q unknown, want claude, codex, opencode, pi, cursor or hermes", a.Harness)
	case isACP && (a.Model != "" || a.Effort != ""):
		return errors.New(acp.Unset)
	case a.Harness == "cursor" && a.Effort != "":
		return fmt.Errorf("cursor has no effort flag: put it in the model, as '<model>[effort=%s]'", a.Effort)
	}
	return nil
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
