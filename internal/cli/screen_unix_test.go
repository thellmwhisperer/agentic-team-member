//go:build unix

package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// onTerminal runs atm with args on a new 120 by 40 terminal, its stdin and stdout, and returns what the terminal
// showed and atm's exit code.
func onTerminal(t *testing.T, args ...string) (string, int) {
	t.Helper()
	p, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := pty.Setsize(tty, &pty.Winsize{Cols: 120, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	var shown bytes.Buffer
	read := make(chan struct{})
	go func() { _, _ = io.Copy(&shown, p); close(read) }()
	stdin := os.Stdin
	os.Stdin = tty
	c := root()
	c.SetArgs(args)
	c.SetOut(tty)
	err = c.Execute()
	os.Stdin = stdin
	_ = tty.Close()
	<-read
	_ = p.Close()
	return shown.String(), run.ExitCode(err)
}

func TestRunOnATerminalShowsTheScreenUnlessPlainLinesAreAsked(t *testing.T) {
	dir := repo(t, "https://example.com/o/r.git") // no .atm.yaml: every run fails at once
	cleanBackgroundServer(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "retry.md"), []byte("# Retry\nType: fix\nRetry.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	shown, code := onTerminal(t, "run", "retry.md")
	if s := ansi.Strip(shown); code != 2 || !strings.Contains(s, "╭─ ATM ") || !strings.Contains(s, "✗ failed") ||
		!strings.Contains(s, "╭─ Findings - E 1") || !strings.Contains(s, "config incomplete") {
		t.Fatalf("exit %d, the terminal showed:\n%s", code, s)
	}
	for env, value := range map[string]string{"NO_COLOR": "1", "TERM": "dumb"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, value)
			shown, code := onTerminal(t, "attach", "retry-1")
			if code != 2 || strings.Contains(shown, "\x1b") || strings.Contains(shown, "╭") {
				t.Fatalf("exit %d, want plain lines, got %q", code, shown)
			}
		})
	}
}
