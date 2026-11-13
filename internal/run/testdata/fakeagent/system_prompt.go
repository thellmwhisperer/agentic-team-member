package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// receivedSystemPrompt reads each harness's system instruction input, separately from its brief.
func receivedSystemPrompt(name string, args []string) string {
	switch name {
	case "claude", "pi":
		return after(args, "--append-system-prompt")
	case "codex":
		for i, arg := range args {
			if i > 0 && args[i-1] == "-c" && strings.HasPrefix(arg, "developer_instructions=") {
				text, _ := strconv.Unquote(strings.TrimPrefix(arg, "developer_instructions="))
				return text
			}
		}
	case "opencode":
		var cfg struct {
			Instructions []string `json:"instructions"`
		}
		_ = json.Unmarshal([]byte(os.Getenv("OPENCODE_CONFIG_CONTENT")), &cfg)
		var texts []string
		for _, path := range cfg.Instructions {
			b, _ := os.ReadFile(path)
			texts = append(texts, string(b))
		}
		return strings.Join(texts, "\n\n")
	case "cursor-agent":
		paths, _ := filepath.Glob(".cursor/rules/atm-system-prompt-*.mdc")
		var texts []string
		for _, path := range paths {
			b, _ := os.ReadFile(path)
			if strings.HasPrefix(string(b), "---\nalwaysApply: true\n---\n") {
				texts = append(texts, strings.TrimPrefix(string(b), "---\nalwaysApply: true\n---\n"))
			}
		}
		return strings.Join(texts, "\n\n")
	}
	return ""
}
