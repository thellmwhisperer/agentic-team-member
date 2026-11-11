//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real atm run, without --follow, exits without any key within five seconds of the gate ending.
func TestRunProcessEndsWhenTheRunEnds(t *testing.T) {
	nm := gateFake(t)
	_, sc := screenRun(t, "echo delivered")
	sc.until(func(s string) bool { return strings.Contains(s, "✓ passed") && strings.Contains(s, "⏸ review") })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	if err := os.WriteFile(filepath.Join(nm, "done"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- sc.f.cmd.Wait() }()
	for {
		select {
		case line, open := <-sc.f.lines:
			if open {
				sc.shown.WriteString(line + "\n")
				continue
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("atm run: %v; stderr:\n%s", err, sc.f.errors())
				}
			case <-deadline.C:
				t.Fatal("atm run kept its process after closing its terminal")
			}
			if s := sc.final(); !strings.Contains(s, "✓ passed") || !gatePassed.MatchString(s) {
				t.Fatalf("atm run lost the completed run or gate in its final screen:\n%s", s)
			}
			return
		case <-deadline.C:
			t.Fatalf("atm run stayed alive more than 5 s after the gate ended, without any key:\n%s", sc.last())
		}
	}
}
