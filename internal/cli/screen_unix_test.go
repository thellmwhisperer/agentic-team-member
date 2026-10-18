//go:build unix

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	read, completed := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(read)
		closed := false
		b := make([]byte, 4096)
		for {
			n, err := p.Read(b)
			_, _ = shown.Write(b[:n])
			if !closed && strings.Contains(ansi.Strip(shown.String()), "q close") {
				closed = true
				close(completed)
			}
			if err != nil {
				return
			}
		}
	}()
	stdin := os.Stdin
	os.Stdin = tty
	c := root()
	c.SetArgs(args)
	c.SetOut(tty)
	done := make(chan error, 1)
	go func() { done <- c.Execute() }()
	select {
	case err = <-done: // plain output or failure before opening the screen
	case <-completed:
		select {
		case err = <-done: // the run ended: its screen quit without q
		case <-time.After(5 * time.Second):
			t.Error("completed native pipeline did not exit when the run ended")
			if _, writeErr := p.Write([]byte("q")); writeErr != nil {
				t.Fatal(writeErr)
			}
			err = <-done
		}
	}
	os.Stdin = stdin
	_ = tty.Close()
	<-read
	_ = p.Close()
	return shown.String(), run.ExitCode(err)
}

func TestRunOnATerminalShowsTheScreenUnlessPlainLinesAreAsked(t *testing.T) {
	dir := setupRetryRepo(t)
	config := "harness: claude\ninstall: \"\"\ntest: \"\"\ntypecheck: \"\"\nlint: \"\"\n" +
		"test_file: \"\"\ntest_patterns: []\ndocs_patterns: []\ndelivery: \"\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	onPath(t, "claude", "gh")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	shown, code := onTerminal(t, "run", "--watcher", watcher(t, dir, "w", 0), "retry.md")
	if s := ansi.Strip(shown); code != 2 || !strings.Contains(s, "╭─ ATM") || !strings.Contains(s, "✗ failed") ||
		!strings.Contains(s, "╭─ Findings - E 1") || !strings.Contains(s, "base ref") {
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
