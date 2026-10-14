package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAcceptsAnOptionalAgentTimeout(t *testing.T) {
	for _, line := range []string{"", "agent_timeout: 45m\n", "agent_timeout: 1h\n"} {
		root := t.TempDir()
		write(t, filepath.Join(root, ".atm.yaml"), complete+line)
		if _, err := Load(root, t.TempDir(), Agent{}); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		t.Logf("loaded .atm.yaml with %q", strings.TrimSpace(line))
	}
}

func TestLoadRejectsAnInvalidAgentTimeout(t *testing.T) {
	for _, v := range []string{"", `""`, "~", "null", "45", "abc", "0", "0s", "-5m", "9999999999h"} {
		t.Run(v, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, ".atm.yaml"), complete+"agent_timeout: "+v+"\n")
			if _, err := Load(root, t.TempDir(), Agent{}); err == nil || !strings.Contains(err.Error(), "agent_timeout") {
				t.Fatalf("want an error naming agent_timeout, got %v", err)
			}
		})
	}
}
