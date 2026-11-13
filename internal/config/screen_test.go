package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestScreenIsAMachineChoice(t *testing.T) {
	for _, mode := range []string{"", "close", "keep", "other", "''", "null"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			if mode != "" {
				write(t, filepath.Join(home, ".config", "atm", "config.yaml"), "screen: "+mode+"\n")
			}
			keep, err := KeepScreen(home)
			if mode == "other" || mode == "''" || mode == "null" {
				if err == nil || !strings.Contains(err.Error(), "screen") ||
					!strings.Contains(err.Error(), "close or keep") {
					t.Fatalf("invalid screen %q: %v", mode, err)
				}
			} else if err != nil || keep != (mode == "keep") {
				t.Fatalf("screen %q: keep=%v, err=%v", mode, keep, err)
			}
		})
	}
	if _, err := Parse(".atm.yaml", []byte(complete+"screen: keep\n"), t.TempDir(), Agent{}); err == nil ||
		!strings.Contains(err.Error(), "screen") {
		t.Fatalf("repository accepted screen: %v", err)
	}
}
