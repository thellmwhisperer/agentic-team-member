//go:build unix

package tui

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// shown is what a terminal showed so far; more says it showed more.
type shown struct {
	sync.Mutex
	b    []byte
	more chan struct{}
}

func (s *shown) Write(p []byte) (int, error) {
	s.Lock()
	s.b = append(s.b, p...)
	s.Unlock()
	select {
	case s.more <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (s *shown) since(i int) string {
	s.Lock()
	defer s.Unlock()
	return string(s.b[i:])
}

// until waits, 5 s at most, for ok to hold on what the terminal showed since i.
func (s *shown) until(i int, ok func(string) bool) bool {
	deadline := time.After(5 * time.Second)
	for !ok(s.since(i)) {
		select {
		case <-s.more:
		case <-deadline:
			return false
		}
	}
	return true
}


const exitAltScreen = "\x1b[?1049l"


func waitShowToOpen(t *testing.T, shown *shown) {
	t.Helper()
	if !shown.until(0, func(v string) bool { return strings.Contains(v, "\x1b[?1049h") }) {
		t.Fatal("Show did not open its screen")
	}
}

func waitShowReturn(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Show did not return")
	}
}

func TestShowColdCompletedRunQuitsLeavingItsScreen(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, tty := testTerminal(t, 69, 39)
	path := savedReport(t, completedReport)
	old := follow
	follow = func(_, _ string, _ int, _ <-chan struct{}, _ func(run.Frame)) (run.Outcome, error) {
		return run.Outcome{Run: "297-65", Outcome: "passed", Ended: time.Now(), Report: path}, nil
	}
	defer func() { follow = old }()
	s, read := readTerminal(master)
	done := make(chan struct{})
	var o run.Outcome
	var detached bool
	var showErr error
	go func() { o, detached, showErr = Show("", "297-65", tty, tty); close(done) }()
	waitShowReturn(t, done) // the run had ended: no q needed
	if showErr != nil || detached || o.Outcome != "passed" {
		t.Fatalf("completed close: %+v detached=%v err=%v", o, detached, showErr)
	}
	assertColdCompletedScreen(t, s)
	_ = tty.Close()
	_ = master.Close()
	<-read
}

// assertColdCompletedScreen says the screen opened and, once it quit, left the completed run's results on the
// terminal.
func assertColdCompletedScreen(t *testing.T, s *shown) {
	t.Helper()
	waitShowToOpen(t, s)
	if !s.until(0, func(v string) bool {
		i := strings.LastIndex(v, exitAltScreen)
		return i >= 0 && strings.Contains(ansi.Strip(v[i:]), "q close")
	}) {
		t.Fatalf("completed view did not finish drawing: %q", s.since(0))
	}
	v := s.since(0)
	i := strings.LastIndex(v, exitAltScreen)
	if left := ansi.Strip(v[i:]); !strings.Contains(left, "q close") || !strings.Contains(left, "21 min 58 s") {
		t.Fatalf("completed view lost its results once it quit: %q", left)
	}
}

func testTerminal(t *testing.T, cols, rows uint16) (*os.File, *os.File) {
	t.Helper()
	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close(); _ = tty.Close() })
	if err := pty.Setsize(tty, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		t.Fatal(err)
	}
	return master, tty
}

func readTerminal(master io.Reader) (*shown, <-chan struct{}) {
	s := &shown{more: make(chan struct{}, 1)}
	read := make(chan struct{})
	go func() { _, _ = io.Copy(s, master); close(read) }()
	return s, read
}
