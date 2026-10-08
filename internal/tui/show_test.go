//go:build unix

package tui

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
	"golang.org/x/term"
)

func TestDeliveryTakesTheTerminalAtItsFirstOutput(t *testing.T) {
	in, out, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	defer func() { _ = out.Close() }()
	p := tea.NewProgram(New("issue-1", 80, 24), tea.WithInput(in), tea.WithOutput(out))
	d := &delivery{p: p, in: in, out: &bytes.Buffer{}, keys: make(chan run.Input, 1)}
	d.frame(run.Frame{PTY: true})
	if d.read != nil {
		t.Fatal("the delivery's terminal took terminal input before its first output")
	}
	d.frame(run.Frame{Raw: []byte("hi")})
	if d.read == nil {
		t.Fatal("the delivery's first output did not transfer terminal input")
	}
	d.keyed.Cancel()
	<-d.read
	if d.state != nil {
		_ = term.Restore(int(d.in.Fd()), d.state)
	}
}

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

// deliveryRuns is Delivery's row, running with its time.
var deliveryRuns = regexp.MustCompile(`Delivery  \d+\.\d s +running`)

const exitAltScreen = "\x1b[?1049l"

// A delivery that writes nothing leaves ATM's screen live on the terminal; from its first output to its end,
// the terminal and its keys are the delivery's, and ATM's screen comes back after.
func TestDeliveryGetsTheTerminalFromItsFirstOutput(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for _, silent := range []bool{true, false} {
		t.Run(map[bool]string{true: "silent for a while", false: "writes at once"}[silent], func(t *testing.T) {
			p, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()
			if err := pty.Setsize(tty, &pty.Winsize{Cols: 120, Rows: 40}); err != nil {
				t.Fatal(err)
			}
			s := &shown{more: make(chan struct{}, 1)}
			read := make(chan struct{})
			go func() { _, _ = io.Copy(s, p); close(read) }()
			defer func(f func(string, string, *[2]int, <-chan run.Input, func(run.Frame)) (run.Outcome, error)) {
				follow = f
			}(follow)
			follow = deliveryRun(t, s, p, silent)
			o, _, err := Show("", "issue-1", tty, tty)
			_ = tty.Close()
			<-read
			if err != nil || o.Outcome != "passed" {
				t.Fatalf("Show = %+v, %v", o, err)
			}
			v := s.since(0)
			last := ansi.Strip(v[strings.LastIndex(v, exitAltScreen):])
			if !order(v, "delivery-says-hi", "\x1b[?1049h") || !order(last, "✓ Delivery", "fix/7 · passed") {
				t.Fatalf("ATM's screen did not come back after delivery, the terminal showed %q", v)
			}
		})
	}
}

// deliveryRun follows a run that gets to Delivery, whose command is silent for a while, when silent, then
// writes, gets the key k typed on keys, and passes: s is what the attached terminal showed.
func deliveryRun(t *testing.T, s *shown, keys io.Writer, silent bool) func(string, string, *[2]int, <-chan run.Input,
	func(run.Frame)) (run.Outcome, error) {
	return func(_, _ string, _ *[2]int, in <-chan run.Input, fn func(run.Frame)) (run.Outcome, error) {
		now := `"ts":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"`
		for _, ev := range append(unit[:12:12], `{"step":"checks","state":"passed","duration_ms":2}`,
			`{"step":"ponytail","state":"started"}`, `{"step":"ponytail","state":"passed","duration_ms":5}`,
			`{"step":"delivery","state":"started",`+now+`}`) {
			fn(run.Frame{Event: []byte(ev)})
		}
		if !s.until(0, func(v string) bool { return deliveryRuns.MatchString(ansi.Strip(v)) }) {
			t.Error("ATM's screen never showed Delivery running")
		}
		mark := len(s.since(0))
		fn(run.Frame{PTY: true})
		if silent {
			live := s.until(mark, func(v string) bool {
				return len(deliveryRuns.FindAllString(ansi.Strip(v), -1)) >= 3
			})
			if v := s.since(mark); !live || strings.Contains(v, exitAltScreen) {
				t.Errorf("while delivery wrote nothing, the terminal showed %q", v)
			}
		}
		fn(run.Frame{Raw: []byte("delivery-says-hi\r\n")})
		if !s.until(mark, func(v string) bool { return strings.Contains(v, "delivery-says-hi") }) ||
			!order(s.since(mark), exitAltScreen, "delivery-says-hi") {
			t.Errorf("delivery's output did not get the terminal: %q", s.since(mark))
		}
		_, _ = keys.Write([]byte("k"))
		for got := false; !got; {
			select {
			case i := <-in:
				got = string(i.Keys) == "k"
			case <-time.After(5 * time.Second):
				t.Error("delivery's keys did not reach it")
				got = true
			}
		}
		fn(run.Frame{Event: []byte(`{"step":"delivery","state":"passed","branch":"fix/7","duration_ms":9}`)})
		return run.Outcome{Outcome: "passed", Ended: time.Now()}, nil
	}
}
