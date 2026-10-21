//go:build unix

package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// The real atm run on a terminal: the 30 lines a checks command prints fold, once it ends, under one header row in
// the Log box, with its name, its first line and its state; a click unfolds them, their Markdown rendered. Tab
// moves the keys to the Log box, which, under the ATM box on a narrow terminal, keeps its rows.
func TestRealATMLogHeaderAndFoldWithThirtyCheckLines(t *testing.T) {
	a, _, runDone := markdownRun(t, false)
	header := a.foldedLog()
	a.click(49) // under every box
	a.unfold(header)
	a.key("\t")
	a.drawn(func(s string) bool { return strings.Contains(s, "Log*") })
	if err := pty.Setsize(a.p, &pty.Winsize{Cols: 80, Rows: 50}); err != nil {
		t.Fatal(err)
	}
	a.drawn(func(s string) bool { return stackedLog.MatchString(s) && strings.Contains(s, "line-28") })
	runDone()
	// Under the Agent box, the agent's node output has the same header row and Markdown.
	live := liveAttach(t, 120, 40)
	live.send(`{"step":"agent","state":"started"}`)
	live.send(`{"step":"agent","output":"**Running tests**"}`)
	live.boldRow(live.drawn(func(s string) bool { return regexp.MustCompile(`▾ \S Agent · unit 1`).MatchString(s) }),
		"│   ", "the agent's output")
	live.send(`{"step":"agent","says":"**Running tests**"}`)
	live.boldRow(live.drawn(func(s string) bool { return strings.Contains(s, "✎ Running tests") }),
		"✎ ", "the agent's speech")
	live.end()
}

func TestRealATMLogScrollHoldsAcrossEviction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial int
		cols    uint16
		text    string
	}{
		{"60-to-110", 60, 120, "log line %03d"},
		{"200-to-250", 200, 120, "log line %03d"},
		{"wrapped-stacked", 200, 80, "log line %03d " + strings.Repeat("word ", 30) + "\n**continuation**"},
		{"fenced", 200, 120, "```\nlog line %03d\n```"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := liveAttach(t, tc.cols, 40)
			a.send(`{"step":"checks","state":"started"}`)
			a.send(`{"step":"checks","check":"test","state":"started"}`)
			output := func(i int) {
				a.send(fmt.Sprintf(`{"step":"checks","check":"test","output":%q}`, fmt.Sprintf(tc.text, i)))
			}
			for i := range tc.initial {
				output(i)
			}
			tail := fmt.Sprintf("log line %03d", tc.initial-1)
			a.drawn(func(s string) bool { return strings.Contains(s, tail) })
			a.key("\t")
			a.drawn(func(s string) bool { return strings.Contains(s, "Log*") })
			key := "\x1b[A"
			if tc.name == "wrapped-stacked" {
				key = "\x1b[5~"
			}
			a.key(key)
			s := a.drawn(func(s string) bool { return strings.Contains(s, "Log*") && !strings.Contains(s, tail) })
			lines := regexp.MustCompile(`log line \d+`)
			held := lines.FindAllString(s, -1)
			if len(held) == 0 {
				t.Fatalf("scrolling left no Log lines:\n%s", s)
			}
			for i := tc.initial; i < tc.initial+50; i++ {
				output(i)
			}
			a.send(`{"step":"checks","check":"appended","state":"started"}`)
			s = a.drawn(func(s string) bool { return strings.Contains(s, "appended") })
			if got := lines.FindAllString(s, -1); !slices.Equal(got, held) {
				t.Fatalf("50 new lines moved the Log from %v to %v:\n%s", held, got, s)
			}
			a.key("\x1b[F")
			a.drawn(func(s string) bool { return strings.Contains(s, fmt.Sprintf("log line %03d", tc.initial+49)) })
			output(tc.initial + 50)
			a.drawn(func(s string) bool { return strings.Contains(s, fmt.Sprintf("log line %03d", tc.initial+50)) })
			a.end()
		})
	}
}

func TestRealATMLogFenceContextSurvivesEviction(t *testing.T) {
	for _, tc := range []struct {
		name, opening, closing string
		cols                   uint16
	}{
		{"backticks", "```go", "```", 120},
		{"tildes", "~~~~text", "~~~~", 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := liveAttach(t, tc.cols, 40)
			a.send(`{"step":"checks","state":"started"}`)
			a.send(`{"step":"checks","check":"test","state":"started"}`)
			output := func(text string) {
				a.send(fmt.Sprintf(`{"step":"checks","check":"test","output":%q}`, text))
			}
			output(tc.opening)
			for i := 1; i < 200; i++ {
				output(fmt.Sprintf("**literal-%03d**", i))
			}
			a.drawn(func(s string) bool { return strings.Contains(s, "literal-199") })
			a.key("\t")
			a.drawn(func(s string) bool { return strings.Contains(s, "Log*") })
			a.key("\x1b[A")
			a.drawn(func(s string) bool { return !strings.Contains(s, "literal-199") })
			codeRows := func() []string {
				t.Helper()
				styled := a.styled()
				plain := ansi.Strip(styled)
				literals := regexp.MustCompile(`literal-\d+`).FindAllString(plain, -1)
				code := regexp.MustCompile("\x1b\\[33m"+`\*\*literal-\d+\*\*`).FindAllString(styled, -1)
				if len(literals) == 0 || len(code) != len(literals) {
					t.Fatalf("fenced output must keep literal asterisks and code styling:\n%q", styled)
				}
				return code
			}
			held := codeRows()
			for i := 200; i < 250; i++ {
				output(fmt.Sprintf("**literal-%03d**", i))
			}
			a.send(`{"step":"checks","check":"appended","state":"started"}`)
			a.drawn(func(s string) bool { return strings.Contains(s, "appended") })
			if got := codeRows(); !slices.Equal(got, held) {
				t.Fatalf("eviction changed retained text or styling: before %q; after %q", held, got)
			}
			a.send(`{"step":"checks","check":"test","state":"passed"}`)
			s := a.drawn(func(s string) bool { return strings.Contains(s, "▸ ") && strings.Contains(s, "literal-050") })
			if got := codeRows(); len(got) != 1 {
				t.Fatalf("folded Log must show one literal code preview: %q", got)
			}
			a.click(rowOf(s, "▸ "))
			a.drawn(func(s string) bool { return strings.Contains(s, "literal-249") })
			codeRows()
			a.key("\x1b[H")
			a.drawn(func(s string) bool { return strings.Contains(s, "▾ ") && strings.Contains(s, "literal-050") })
			codeRows()
			a.key("\x1b[F")
			a.drawn(func(s string) bool { return strings.Contains(s, "literal-249") })
			codeRows()
			for _, wrong := range []string{"~~~", "``", tc.closing + "info"} {
				output(wrong)
			}
			output("**literal-250**")
			a.drawn(func(s string) bool { return strings.Contains(s, "literal-250") })
			codeRows()
			output(tc.closing)
			output("**after fence**")
			a.drawn(func(s string) bool { return strings.Contains(s, "after fence") })
			if !strings.Contains(a.styled(), "\x1b[1mafter fence") || strings.Contains(ansi.Strip(a.styled()), "**after fence**") {
				t.Fatalf("closing the retained fence must restore Markdown:\n%q", a.styled())
			}
			output(tc.opening)
			a.send(`{"step":"checks","check":"next","state":"started"}`)
			a.send(`{"step":"checks","check":"next","output":"**Running tests**"}`)
			a.boldRow(a.drawn(func(s string) bool { return strings.Contains(s, "Running tests") }), "│ ", "the next command")
			a.end()
		})
	}
}

var stackedLog = regexp.MustCompile(`(?m)^╭─ Log\* `)

// The real atm run on a terminal: the folded thinking row of the Agent box and the folded header of the Log box
// render their first line's Markdown, as they do unfolded.
func TestRealATMFoldedRowsRenderMarkdown(t *testing.T) {
	a, agentDone, runDone := markdownRun(t, true)
	// The fake agent's last row, its Skill call, comes before it waits: the rows hold still for the click.
	s := a.drawn(func(s string) bool {
		return strings.Contains(s, "∴ ") && strings.Contains(s, "Running tests") && strings.Contains(s, "1 tool: Skill")
	})
	a.boldRow(s, "∴ ", "the folded thinking row")
	a.click(rowOf(s, "▸ ∴ "))
	a.boldRow(a.drawn(func(s string) bool { return strings.Contains(s, "▾ ∴ Running tests") }), "▾ ∴ ",
		"the unfolded thinking")
	agentDone()
	a.unfold(a.foldedLog())
	runDone()
}

// markdownRun is the real atm run of issue on a 120×50 terminal, its test command printing 30 lines whose first is
// **Running tests**, its typecheck held until the test ends. When holdAgent, the agent first thinks
// **Running tests** and waits for agentDone. runDone lets typecheck end and waits for the run to end.
func markdownRun(t *testing.T, holdAgent bool) (a *attached, agentDone, runDone func()) {
	bin := builtATM(t)
	dir := t.TempDir()
	checks, agent := filepath.Join(dir, "checks"), filepath.Join(dir, "agent")
	test := `printf "%s\n" "**Running tests**"; seq -f "line-%02g" 1 28; printf "%s\n" "\` + "`code-span\\`" +
		`"; sh a_test.sh`
	cfg := atmSet(atmSet(atmYAML, "test", test), "typecheck", fmt.Sprintf(`cat "%s" >/dev/null`, checks))
	top := gitT(t, repo(t, "https://example.com/o/r.git", cfg), "rev-parse", "--show-toplevel")
	stopServing(t, top)
	held(t, checks)
	held(t, agent)
	agentDone = func() {}
	if holdAgent {
		t.Setenv("FAKE_AGENT_STREAM", `{"type":"assistant","message":{"role":"assistant","content":[`+
			`{"type":"thinking","thinking":"**Running tests**\n\nthe agent thinks on"}]}}`+"\n")
		t.Setenv("FAKE_AGENT_WORK", fmt.Sprintf(`cat "%s" >/dev/null; `, agent)+fixWork)
		agentDone = func() { free(t, agent) }
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	a = &attached{t: t, cmd: exec.Command(bin, "run", issueFile(t, issue)), chunks: make(chan string, 64), rows: 50}
	a.cmd.Dir = top
	var err error
	if a.p, err = pty.StartWithSize(a.cmd, &pty.Winsize{Cols: 120, Rows: 50}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.cmd.Process.Kill(); _ = a.p.Close() })
	go a.read()
	return a, agentDone, func() {
		free(t, checks)
		timeout := time.After(2 * time.Minute)
		for open := true; open; {
			select {
			case _, open = <-a.chunks:
			case <-timeout:
				t.Fatal("atm run stayed open after typecheck ended")
			}
		}
		if err := a.cmd.Wait(); err != nil {
			t.Fatalf("atm run: %v", err)
		}
	}
}

// held is a FIFO at path a command reads to wait, freed when the test ends.
func held(t *testing.T, path string) {
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})
}

// free ends the read of the command that waits on FIFO path, once it reads.
func free(t *testing.T, path string) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// frameEnd is how atm attach ends each frame it draws: the cursor moved under it.
var frameEnd = regexp.MustCompile(`\x1b\[\d+;\d*H$`)

// drawn reads the screen until a whole frame of it matches ok, and returns it.
func (a *attached) drawn(ok func(screen string) bool) string {
	a.t.Helper()
	timeout := time.After(time.Minute)
	for {
		s := ansi.Strip(a.styled())
		if frameEnd.MatchString(a.raw.String()) && ok(s) {
			return s
		}
		select {
		case c, open := <-a.chunks:
			if !open {
				a.t.Fatalf("atm ended before the screen showed what was awaited:\n%s", s)
			}
			a.raw.WriteString(c)
		case <-timeout:
			a.t.Fatalf("the screen never showed what was awaited:\n%s", s)
		}
	}
}

var typecheckRuns = regexp.MustCompile(`typecheck[^│]*running`)

// logColumn is the Log box's part of screen row line: what is right of the ATM box.
func logColumn(line string) string {
	r := []rune(line)
	return string(r[min(len(r), 42):])
}

// foldedLog waits for typecheck to run and checks the ended test command shows in the Log box as one folded
// header row: its name, its first line rendered, its state, none of its other lines. It returns that row.
func (a *attached) foldedLog() int {
	a.t.Helper()
	s := a.drawn(typecheckRuns.MatchString)
	header := -1
	for i, line := range strings.Split(s, "\n") {
		l := logColumn(line)
		if strings.Contains(l, "line-") || strings.Contains(l, "code-span") {
			a.t.Fatalf("the ended test command's lines show folded:\n%s", s)
		}
		if strings.Contains(l, "test") && strings.Contains(l, "passed") {
			if header >= 0 || !strings.Contains(l, "▸ ") {
				a.t.Fatalf("want one folded header row for the test command:\n%s", s)
			}
			header = i
		}
	}
	if header < 0 {
		a.t.Fatalf("no header row for the test command in the Log box:\n%s", s)
	}
	a.boldRow(s, "▸ ", "the folded Log header")
	return header
}

// unfold clicks the Log box's header row y and checks the test command's 30 lines show, rendered.
func (a *attached) unfold(y int) {
	a.t.Helper()
	a.click(y)
	s := a.drawn(func(s string) bool { return strings.Contains(s, "line-28") })
	for _, want := range []string{"line-01", "code-span", "Running tests"} {
		if !strings.Contains(s, want) {
			a.t.Fatalf("the unfolded test command lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "`code-span`") {
		a.t.Fatalf("the unfolded lines show Markdown markers:\n%s", s)
	}
	a.boldRow(s, "│ ", "the unfolded Log lines")
}

// click presses and releases the mouse on screen row y, in the right column.
func (a *attached) click(y int) {
	a.key(fmt.Sprintf("\x1b[<0;60;%dM\x1b[<0;60;%dm", y+1, y+1))
}

// rowOf is the first row of screen s that shows text.
func rowOf(s, text string) int {
	for i, line := range strings.Split(s, "\n") {
		if strings.Contains(line, text) {
			return i
		}
	}
	return -1
}

// boldRow checks the rows of screen s that show mark and Running tests show it bold, with no asterisks.
func (a *attached) boldRow(s, mark, what string) {
	a.t.Helper()
	styled := strings.Split(a.styled(), "\n")
	found := false
	for i, line := range strings.Split(s, "\n") {
		if !strings.Contains(line, mark) || !strings.Contains(line, "Running tests") {
			continue
		}
		found = true
		if strings.Contains(line, "**Running tests**") || !strings.Contains(styled[i], "\x1b[1mRunning tests") {
			a.t.Fatalf("%s does not render its Markdown: %q\n%s", what, styled[i], s)
		}
	}
	if !found {
		a.t.Fatalf("%s does not show Running tests:\n%s", what, s)
	}
}

func (a *attached) read() {
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
}

func (a *attached) styled() string {
	rows := make([]string, a.rows)
	raw, at, last := a.raw.String(), 0, 0
	write := func(s string) {
		if s = strings.ReplaceAll(s, "\r", ""); ansi.Strip(s) != "" || strings.Contains(s, ansi.EraseLineRight) {
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
