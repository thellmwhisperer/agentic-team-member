package run

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	system "github.com/thellmwhisperer/agentic-team-member"
)

// runSystemPrompt keeps the exact file bytes, so resume does not depend on the original file.
type runSystemPrompt struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Text   string `json:"text"`
}

func (a *agent) readPrompts() error {
	if a.systemPromptFile == "" {
		return a.readTexts()
	}
	path, err := filepath.Abs(a.systemPromptFile)
	var b []byte
	if err == nil {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return fmt.Errorf("--system-prompt: %w", err)
	}
	a.systemPrompt = &runSystemPrompt{File: path, SHA256: fmt.Sprintf("%x", sha256.Sum256(b)), Text: string(b)}
	return a.readTexts()
}

func (a agent) prompt() string {
	prompt := a.text("system-md", system.Prompt)
	if a.systemPrompt != nil {
		return prompt + "\n\n" + a.systemPrompt.Text
	}
	return prompt
}

// systemEnvironment uses OpenCode's system instruction files and Cursor's always-applied system rules.
// rule is a temporary Cursor rule the caller removes after the invocation.
func (a agent) systemEnvironment(clone string, env []string) ([]string, string, error) {
	if a.systemPrompt == nil && a.texts["system-md"] == nil {
		return env, "", nil
	}
	switch a.Harness {
	case "opencode":
		path := filepath.Join(a.dir, "system-prompt.md")
		if err := os.WriteFile(path, []byte(a.prompt()), 0o600); err != nil {
			return env, "", err
		}
		raw := ""
		for _, kv := range env {
			if strings.HasPrefix(kv, "OPENCODE_CONFIG_CONTENT=") {
				raw = strings.TrimPrefix(kv, "OPENCODE_CONFIG_CONTENT=")
			}
		}
		cfg := map[string]any{}
		if err := json.Unmarshal([]byte(cmp.Or(raw, "{}")), &cfg); err != nil {
			return env, "", err
		}
		if cfg == nil {
			cfg = map[string]any{}
		}
		instructions, _ := cfg["instructions"].([]any)
		cfg["instructions"] = append(instructions, path)
		b, _ := json.Marshal(cfg)
		return append(env, "OPENCODE_CONFIG_CONTENT="+string(b)), "", nil
	case "cursor":
		dir := filepath.Join(clone, ".cursor", "rules")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return env, "", err
		}
		f, err := os.CreateTemp(dir, "atm-system-prompt-*.mdc")
		if err != nil {
			return env, "", err
		}
		_, err = f.WriteString("---\nalwaysApply: true\n---\n" + a.prompt())
		err = errors.Join(err, f.Close(), exclude(clone, "/.cursor/rules/atm-system-prompt-*.mdc"))
		return env, f.Name(), err
	}
	return env, "", nil
}
