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
		`"report":{"findings":[{"file":"a.go","family":"dead code","finding":"unused helper"}]},` +
		`"verdicts":[{"file":"a.go","finding":"unused helper","verdict":"kept"}]}`)
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

// The real screen folds issue #467's eleven-word title to one line, the arrow in front and an ellipsis at its end;
// i and a click on the arrow split it in whole lines, and fold it again. No label names the fold.
func TestRealScreenFoldsTheTitle(t *testing.T) {
	title := "The issue title folds to one line with the arrow and an ellipsis; i or a click unfolds it"
	a := liveAttach(t, 60, 40)
	a.send(`{"step":"issue","state":"started"}`)
	a.send(`{"step":"issue","state":"passed","title":"` + title + `","number":467}`)
	folded := regexp.MustCompile(`(?m)^│ ▸ #467 The issue title[^…▾]*… +\S+ \w+ │$`)
	// unfolded is the screen's title, read from its rows behind the open arrow, when every word shows.
	unfolded := func(s string) bool {
		at := rowOf(s, "▾ #467")
		if at < 0 {
			return false
		}
		var words []string
		for i, line := range strings.Split(s, "\n")[at:] {
			fields := strings.Fields(strings.Trim(line, "│ "))
			if len(fields) == 0 {
				break
			}
			if i == 0 {
				fields = fields[1 : len(fields)-2] // the arrow, then the state's icon and word
			}
			words = append(words, fields...)
		}
		return strings.Join(words, " ") == "#467 "+title
	}
	arrow := "\x1b[<0;3;2M\x1b[<0;3;2m" // a click on the ATM box's first row, its third column
	a.sees(folded.MatchString)
	a.key("i")
	a.sees(unfolded)
	a.key("i")
	a.sees(folded.MatchString)
	a.key(arrow)
	a.sees(unfolded)
	a.key(arrow)
	a.sees(folded.MatchString)
	a.send(`{"step":"contract","state":"started"}`)
	a.sees(runningContract.MatchString)
	for _, pos := range [][2]int{{55, 2}, {12, 2}, {2, 2}, {4, 2}, {3, 1}, {3, 3}} {
		t.Logf("ignore click %d,%d, folded and unfolded", pos[0], pos[1])
		click := fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", pos[0], pos[1], pos[0], pos[1])
		a.key(click + "i\t")
		a.sees(func(s string) bool { return strings.Contains(s, "Log*") && unfolded(s) })
		a.key(click + "i\t")
		a.sees(func(s string) bool { return strings.Contains(s, "ATM*") && folded.MatchString(s) })
	}
	if shown := ansi.Strip(a.raw.String()); strings.Contains(shown, "unfold title") {
		t.Fatalf("the screen names the fold:\n%s", shown)
	}
	a.end()
	for _, cols := range []uint16{60, 120} {
		t.Run(fmt.Sprintf("no arrow at %d columns", cols), func(t *testing.T) {
			b := liveAttach(t, cols, 40)
			b.send(`{"step":"issue","state":"started"}`)
			b.send(`{"step":"issue","state":"passed","title":"` + title + `","number":467}`)
			b.send(`{"step":"contract","state":"started"}`)
			b.sees(func(s string) bool { return strings.Contains(s, "▸ #467") && runningContract.MatchString(s) })
			b.send(`{"step":"issue","state":"passed","title":"Short title","number":467}`)
			s := b.sees(func(s string) bool { return strings.Contains(s, "#467 Short title") && runningContract.MatchString(s) })
			if line := strings.Split(s, "\n")[rowOf(s, "#467 Short title")]; strings.ContainsAny(line, "▸▾…") {
				t.Fatalf("a short title has an arrow:\n%s", s)
			}
			b.key(arrow + "\t")
			b.sees(func(s string) bool { return strings.Contains(s, "Log*") })
			b.send(`{"step":"issue","state":"passed","title":"` + title + `","number":467}`)
			b.sees(func(s string) bool { return strings.Contains(s, "▸ #467") })
			b.key(arrow)
			b.sees(func(s string) bool { return strings.Contains(s, "▾ #467") && strings.Contains(s, "unfolds it") })
			b.key(arrow)
			b.sees(func(s string) bool { return strings.Contains(s, "▸ #467") })
			b.end()
		})
	}
}

// The real atm attach folds the issue's title to one line: whole when it fits, else behind ▸ until i shows every
// line behind ▾ and folds them again.
func TestRealAttachWrapsAndFoldsIssueTitle(t *testing.T) {
	t.Run("short", func(t *testing.T) {
		a := liveAttach(t, 60, 40)
		a.send(`{"step":"issue","state":"started"}`)
		a.send(`{"step":"issue","state":"passed","title":"A short title, tail-short","number":7}`)
		if shown := a.until(`tail-short`); strings.ContainsAny(shown, "▸▾…") {
			t.Fatalf("a title within one line has an arrow:\n%s", shown)
		}
		a.end()
	})
	t.Run("long", func(t *testing.T) {
		a := liveAttach(t, 60, 40)
		a.send(`{"step":"issue","state":"started"}`)
		a.send(`{"step":"issue","state":"passed","title":"` + strings.Repeat("long words wrap ", 15) +
			`tail-long","number":8}`)
		if shown := a.until(`▸ #8 long words[^\n]*…`); strings.Contains(shown, "tail-long") {
			t.Fatalf("a long title shows whole before i:\n%s", shown)
		}
		a.key("i")
		a.until(`▾ #8 long words`)
		a.until(`tail-long`)
		a.key("i")
		a.until(`▸ #8 long words[^\n]*…`)
		a.end()
	})
	// While Contract runs under the ATM box, one screen, with no key pressed, shows of a long title its first words
	// on one line behind the arrow, never the rest. At 60 columns that line holds 10 words.
	t.Run("folded while running", func(t *testing.T) { compactTitleWhileRunning(t, 30, 10, true) })
	t.Run("folded long while running", func(t *testing.T) { compactTitleWhileRunning(t, 50, 10, true) })
	// On a short terminal, beside and under the ATM box, every screen keeps the title's start and the running
	// step, the folded title its arrow, and the ATM box's keys reach every line of the unfolded title.
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
			tokens := append([]string{"▾", "▸"}, words...)
			seen := map[string]bool{}
			a.scrollsTitle(seen, tokens, "w00")
			a.key("\x1b[H") // home
			if !seen["▸"] {
				t.Fatal("the folded title shows no arrow")
			}
			a.key("i")
			a.key("\x1b[H") // home: the unfolded title's lines are above the rows the box reads
			a.scrollsTitle(seen, tokens, append(words, "▾")...)
			a.end()
		})
	}
}

// compactTitleWhileRunning attaches at 60×30 to a run whose title is count words and whose Contract runs, and
// checks one screen shows, on one line, the first shown words, behind the fold arrow when hint, and no other word.
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
		return runningContract.MatchString(s) && strings.Contains(s, "▸ #7 w00") == hint
	})
	if n := len(regexp.MustCompile(`(?m)^.*\bw\d\d\b.*$`).FindAllString(s, -1)); n != 1 {
		t.Fatalf("the title shows on %d lines, not 1:\n%s", n, s)
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
