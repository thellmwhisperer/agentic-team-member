//go:build unix

package run

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// The real atm attach, on a terminal: while ATM owns the screen, the right column shows the step that runs, its
// name and elapsed time until it has output, the agent's events while an agent runs, and the findings beside.
func TestRealAttachShowsEveryRunningStep(t *testing.T) {
	a := liveAttach(t, 120, 40)
	a.send(`{"step":"issue","state":"started"}`)
	a.until(`Issue · [0-9.]+ s`)
	a.send(`{"step":"issue","state":"passed","title":"Screen","number":7}`)
	a.send(`{"step":"contract","state":"started"}`)
	a.until(`Contract · [0-9.]+ s`)
	a.send(`{"step":"contract","output":"contract-output-line"}`)
	a.until(`contract-output-line`)
	// The ATM row's elapsed time redraws with the placeholder's: twice, and the placeholder never came back.
	if shown := a.until(`Contract  [0-9.]+ s`) + a.until(`Contract  [0-9.]+ s`); strings.Contains(shown, "Contract · ") {
		t.Fatalf("output did not replace the placeholder:\n%s", shown)
	}
	a.send(`{"step":"contract","state":"passed"}`)
	a.send(`{"step":"agent","state":"started"}`)
	a.send(`{"step":"agent","says":"agent-says-hello"}`)
	a.until(`✎ agent-says-hello`)
	a.send(`{"step":"agent","state":"passed"}`)
	a.send(`{"step":"ponytail","state":"started"}`)
	a.send(`{"step":"ponytail","state":"passed","kept":true,` +
		`"report":{"findings":[{"file":"a.go","family":"dead code","finding":"unused helper"}]}}`)
	a.until(`Findings - W 1`)
	a.send(`{"step":"delivery","state":"started"}`)
	a.until(`Delivery · [0-9.]+ s`)
	a.end()
}

// The real atm attach names the agent's harness, model and effort in the Agent box's title, effort only when set,
// beside the ATM box and under it.
func TestRealAttachNamesAgentSettings(t *testing.T) {
	for _, c := range []struct {
		cols         uint16
		effort, want string
	}{
		{120, "max", `Agent · pi zai/glm-5\.3 · max`},
		{60, "", `Agent · pi zai/glm-5\.3\*? ─`},
	} {
		t.Run(c.want, func(t *testing.T) {
			a := liveAttach(t, c.cols, 40)
			a.send(fmt.Sprintf(`{"step":"issue","state":"started",`+
				`"agent":{"harness":"pi","model":"zai/glm-5.3","effort":%q,"harness_args":[]}}`, c.effort))
			a.send(`{"step":"issue","state":"passed"}`)
			a.send(`{"step":"agent","state":"started"}`)
			a.until(c.want)
			a.end()
		})
	}
}

// The real atm attach wraps the issue's title: whole within three lines, else three and a hint whose key, i,
// shows every line and folds them again.
func TestRealAttachWrapsAndFoldsIssueTitle(t *testing.T) {
	t.Run("short", func(t *testing.T) {
		a := liveAttach(t, 60, 40)
		a.send(`{"step":"issue","state":"started"}`)
		a.send(`{"step":"issue","state":"passed","title":"A short title that wraps onto a few lines of the ATM box ` +
			`and ends with tail-short","number":7}`)
		if shown := a.until(`tail-short`); strings.Contains(shown, "unfold title") {
			t.Fatalf("a title within three lines has a hint:\n%s", shown)
		}
		a.end()
	})
	t.Run("long", func(t *testing.T) {
		a := liveAttach(t, 60, 40)
		a.send(`{"step":"issue","state":"started"}`)
		a.send(`{"step":"issue","state":"passed","title":"` + strings.Repeat("long words wrap ", 15) +
			`tail-long","number":8}`)
		if shown := a.until(`… i unfold title`); strings.Contains(shown, "tail-long") {
			t.Fatalf("a long title shows whole before i:\n%s", shown)
		}
		a.key("i")
		a.until(`tail-long`)
		a.until(`i fold title`)
		a.key("i")
		a.until(`… i unfold title`)
		a.end()
	})
	// While Contract runs under the ATM box, one screen, with no key pressed, shows a title of three lines whole,
	// and of a longer one its first three lines and the hint, never the rest. At 60 columns a line holds 11 words.
	t.Run("three lines while running", func(t *testing.T) { compactTitleWhileRunning(t, 30, 30, false) })
	t.Run("compact long while running", func(t *testing.T) { compactTitleWhileRunning(t, 50, 33, true) })
	// On a short terminal, beside and under the ATM box, every screen keeps the title's start and the running
	// step, and the ATM box's keys reach every line of the title, folded and unfolded.
	for _, cols := range []uint16{120, 60} {
		t.Run(fmt.Sprintf("short %d×9", cols), func(t *testing.T) {
			a := liveAttach(t, cols, 9)
			words := make([]string, 40)
			for i := range words {
				words[i] = fmt.Sprintf("w%02d", i)
			}
			a.send(`{"step":"issue","state":"started"}`)
			a.send(`{"step":"issue","state":"passed","title":"` + strings.Join(words, " ") + `","number":8}`)
			a.send(`{"step":"contract","state":"started"}`)
			tokens := append([]string{"i fold title", "i unfold title"}, words...)
			seen := map[string]bool{}
			a.scrollsTitle(seen, tokens, "w00")
			a.key("\x1b[H") // home
			a.scrollsTitle(seen, tokens, "i unfold title")
			a.key("i")
			a.scrollsTitle(seen, tokens, append(words, "i fold title")...)
			a.end()
		})
	}
}

// compactTitleWhileRunning attaches at 60×30 to a run whose title is count words and whose Contract runs, and
// checks one screen shows, on three lines, the first shown words, the unfold hint when hint, and no other word.
func compactTitleWhileRunning(t *testing.T, count, shown int, hint bool) {
	a := liveAttach(t, 60, 30)
	words := make([]string, count)
	for i := range words {
		words[i] = fmt.Sprintf("w%02d", i)
	}
	a.send(`{"step":"issue","state":"started"}`)
	a.send(`{"step":"issue","state":"passed","title":"` + strings.Join(words, " ") + `","number":7}`)
	a.send(`{"step":"contract","state":"started"}`)
	s := a.sees(func(s string) bool {
		for _, w := range words[:shown] {
			if !strings.Contains(s, w) {
				return false
			}
		}
		return runningContract.MatchString(s) && strings.Contains(s, "… i unfold title") == hint
	})
	if n := len(regexp.MustCompile(`(?m)^.*\bw\d\d\b.*$`).FindAllString(s, -1)); n != 3 {
		t.Fatalf("the title shows on %d lines, not 3:\n%s", n, s)
	}
	for _, w := range words[shown:] {
		if strings.Contains(s, w) {
			t.Fatalf("a long title shows %s before i:\n%s", w, s)
		}
	}
	a.end()
}

// attached is the real atm attach on a cols×rows terminal of a run a fake background process serves.
type attached struct {
	t      *testing.T
	cmd    *exec.Cmd
	p      *os.File
	events chan string
	served chan error
	chunks chan string
	raw    strings.Builder
	from   int // where the next until looks, in the screen without ANSI
	rows   int
}

func liveAttach(t *testing.T, cols, rows uint16) *attached {
	t.Helper()
	bin := builtATM(t)
	top := atmRepo(t)
	listener, err := net.Listen("unix", short(socket(top)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	a := &attached{t: t, events: make(chan string, 64), served: make(chan error, 1), chunks: make(chan string, 64),
		rows: int(rows)}
	go func() { a.served <- serveLive(listener, a.events) }()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	a.cmd = exec.Command(bin, "attach", "live-run")
	a.cmd.Dir = top
	if a.p, err = pty.StartWithSize(a.cmd, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.cmd.Process.Kill(); _ = a.p.Close() })
	go func() {
		defer close(a.chunks)
		b := make([]byte, 4096)
		for {
			n, err := a.p.Read(b)
			if n > 0 {
				a.chunks <- string(b[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return a
}

// serveLive answers one attach with events, as frames, and, once events is closed, the run's end.
func serveLive(l net.Listener, events <-chan string) error {
	c, err := l.Accept()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	var req request
	if err := json.NewDecoder(c).Decode(&req); err != nil {
		return err
	}
	enc := json.NewEncoder(c)
	for e := range events {
		if err := enc.Encode(reply{Frame: Frame{Event: json.RawMessage(e)}}); err != nil {
			return err
		}
	}
	return enc.Encode(reply{Run: &Outcome{Run: "live-run", Outcome: "passed", Ended: time.Now()}})
}

// send serves event e, stamped now.
func (a *attached) send(e string) {
	a.events <- strings.TrimSuffix(e, "}") + fmt.Sprintf(`,"ts":%q}`, time.Now().UTC().Format(time.RFC3339Nano))
}

func (a *attached) key(k string) {
	if _, err := a.p.Write([]byte(k)); err != nil {
		a.t.Fatal(err)
	}
}

// until reads the screen until what it drew since the last until matches pattern, and returns that.
func (a *attached) until(pattern string) string {
	a.t.Helper()
	want := regexp.MustCompile(pattern)
	timeout := time.After(time.Minute)
	for {
		shown := ansi.Strip(a.raw.String())
		since := shown[min(a.from, len(shown)):]
		if loc := want.FindStringIndex(since); loc != nil {
			a.from += loc[1]
			return since[:loc[1]]
		}
		select {
		case c, open := <-a.chunks:
			if !open {
				a.t.Fatalf("atm attach ended before it showed %q:\n%s", pattern, since)
			}
			a.raw.WriteString(c)
		case <-timeout:
			a.t.Fatalf("atm attach never showed %q:\n%s", pattern, since)
		}
	}
}

var runningContract = regexp.MustCompile(`Contract · [0-9.]+ s`)

// scrollsTitle waits for a screen with the start of issue #8's title, the running Contract and a token not
// seen yet, then scrolls the ATM box a line down, until it has seen every token of want.
func (a *attached) scrollsTitle(seen map[string]bool, tokens []string, want ...string) {
	a.t.Helper()
	for {
		s := a.sees(func(s string) bool {
			if !strings.Contains(s, "#8 w00") || !runningContract.MatchString(s) {
				return false
			}
			for _, w := range tokens {
				if !seen[w] && strings.Contains(s, w) {
					return true
				}
			}
			return false
		})
		for _, w := range tokens {
			seen[w] = seen[w] || strings.Contains(s, w)
		}
		done := true
		for _, w := range want {
			done = done && seen[w]
		}
		if done {
			return
		}
		a.key("\x1b[B") // down
	}
}

// sees reads the screen until it matches ok, and returns it.
func (a *attached) sees(ok func(screen string) bool) string {
	a.t.Helper()
	timeout := time.After(time.Minute)
	for {
		s := a.screen()
		if ok(s) {
			return s
		}
		select {
		case c, open := <-a.chunks:
			if !open {
				a.t.Fatalf("atm attach ended before the screen showed what was awaited:\n%s", s)
			}
			a.raw.WriteString(c)
		case <-timeout:
			a.t.Fatalf("the screen never showed what was awaited:\n%s", s)
		}
	}
}

var cursorMoves = regexp.MustCompile(`\x1b\[(\d*)(?:;\d*)?([HJ])|\n`)

// screen is the terminal's rows, without ANSI, as atm attach drew them so far on its alternate screen: each
// line it writes, on the row its cursor moved to.
func (a *attached) screen() string {
	rows := make([]string, a.rows)
	raw, at, last := a.raw.String(), 0, 0
	write := func(s string) {
		if s = strings.ReplaceAll(ansi.Strip(s), "\r", ""); s != "" {
			rows[at] = s
		}
	}
	for _, m := range cursorMoves.FindAllStringSubmatchIndex(raw, -1) {
		write(raw[last:m[0]])
		last = m[1]
		switch {
		case m[4] < 0:
			at = min(at+1, len(rows)-1)
		case raw[m[4]] == 'H':
			n, _ := strconv.Atoi(raw[m[2]:m[3]])
			at = min(max(n-1, 0), len(rows)-1)
		case raw[m[2]:m[3]] == "2":
			clear(rows)
		default:
			clear(rows[at+1:])
		}
	}
	write(raw[last:])
	return strings.Join(rows, "\n")
}

// end ends the run and waits for atm attach to quit by itself.
func (a *attached) end() {
	a.t.Helper()
	close(a.events)
	if err := <-a.served; err != nil {
		a.t.Fatal(err)
	}
	timeout := time.After(time.Minute)
	for open := true; open; {
		select {
		case _, open = <-a.chunks:
		case <-timeout:
			a.t.Fatal("atm attach stayed open after the run ended")
		}
	}
	if err := a.cmd.Wait(); err != nil {
		a.t.Fatalf("atm attach: %v", err)
	}
}
