//go:build unix

package run

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

func TestRealScreenScrollsEverySlopFinding(t *testing.T) {
	builtATM(t)
	for _, cols := range []uint16{100, 80} {
		t.Run(fmt.Sprint(cols), func(t *testing.T) {
			barrier := filepath.Join(t.TempDir(), "delivery")
			held(t, barrier)
			var paths []string
			work, cut := fixWork+"; mkdir -p internal/run", "true"
			for n := 1; n <= 6; n++ {
				path := fmt.Sprintf("internal/run/file%d.go", n)
				paths = append(paths, path)
				work += "; printf 'before\\nextra\\n' > " + path
				cut += "; printf 'after\\nextra\\n' > " + path
			}
			t.Setenv("FAKE_AGENT_WORK", work)
			t.Setenv("FAKE_PONYTAIL_WORK", cut)
			t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts(paths...))
			cfg := atmSet(atmYAML, "delivery", fmt.Sprintf("cat %q >/dev/null", barrier))
			top := gitT(t, repo(t, "https://example.com/o/r.git", cfg), "rev-parse", "--show-toplevel")
			bin := versionServer(t, top, "follow-test")
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("NO_COLOR", "")
			a := &attached{t: t, cmd: exec.Command(bin, "run", issueFile(t, issue)), chunks: make(chan string, 64), rows: 24}
			a.cmd.Dir = top
			var err error
			if a.p, err = pty.StartWithSize(a.cmd, &pty.Winsize{Cols: cols, Rows: 24}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.cmd.Process.Kill(); _ = a.p.Close() })
			go a.read()
			a.drawn(func(s string) bool { return strings.Contains(s, "Findings - W 6") && strings.Contains(s, "command") })
			a.key("\t")
			focused := a.drawn(func(s string) bool { return strings.Contains(s, "Findings - W 6*") })
			seen := map[string]bool{}
			inspect := func(s string) string {
				if !strings.HasPrefix(s, "╭─ ATM") || !strings.Contains(s, "q detach") || !strings.Contains(s, "╭─ Log") {
					t.Fatalf("issue 586: the 24-row frame must retain ATM, Findings, Log and footer:\n%s", s)
				}
				var lines []string
				for _, line := range strings.Split(s, "\n") {
					if cols == 100 {
						line = ansi.Cut(line, 40, 100)
					}
					lines = append(lines, line)
				}
				panel := strings.Join(lines, "\n")
				panel = panel[strings.Index(panel, "╭─ Findings"):]
				panel = strings.SplitN(panel, "╰", 2)[0]
				flat := strings.Join(strings.Fields(strings.ReplaceAll(panel, "│", " ")), " ")
				for _, path := range paths {
					want := "W rejected: does not shorten the diff: 12 net added lines, 12 before · " + path + " cut " + path
					seen[path] = seen[path] || strings.Contains(flat, want)
				}
				return panel
			}
			previous := inspect(focused)
			for _, key := range []string{"\x1b[H", "\x1b[B", "\x1b[A", "\x1b[6~", "\x1b[5~", "\x1b[F", "\x1b[H"} {
				a.key(key)
				s := a.drawn(func(s string) bool { return inspect(s) != previous })
				previous = inspect(s)
			}
			for n := 0; n < 24; n++ {
				complete := true
				for _, path := range paths {
					complete = complete && seen[path]
				}
				if complete {
					break
				}
				a.key("\x1b[B")
				previous = inspect(a.drawn(func(s string) bool { return inspect(s) != previous }))
			}
			for _, path := range paths {
				if !seen[path] {
					t.Fatalf("complete verdict, path and description never shared a visible frame: %s", path)
				}
			}
			a.key("\x1b[F")
			s := a.drawn(func(s string) bool { return strings.Contains(s, "cut internal/run/file6.go") })
			previous = inspect(s)
			y := rowOf(s, "╭─ Findings") + 2
			a.key(fmt.Sprintf("\x1b[<64;50;%dM", y))
			previous = inspect(a.drawn(func(s string) bool { return inspect(s) != previous }))
			a.key(fmt.Sprintf("\x1b[<65;50;%dM", y))
			a.drawn(func(s string) bool { return inspect(s) != previous })
			free(t, barrier)
			deadline := time.After(time.Minute)
			for open := true; open; {
				select {
				case chunk, more := <-a.chunks:
					open = more
					a.raw.WriteString(chunk)
				case <-deadline:
					t.Fatal("run did not finish")
				}
			}
			if err := a.cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			final := ansi.Strip(a.raw.String())
			final = strings.TrimSpace(final[strings.LastIndex(final, "╭─ ATM"):])
			if len(strings.Split(final, "\n")) > 24 || !strings.Contains(final, "q close") {
				t.Fatalf("final frame exceeds 100/80 x 24 including footer:\n%s", final)
			}
		})
	}
}

func TestRealFindingsFocusFollowsVisibility(t *testing.T) {
	a := liveAttach(t, 100, 24)
	a.send(`{"step":"ponytail","state":"passed","verdicts":[{"file":"a.go","finding":"unused","verdict":"kept"}]}`)
	a.drawn(func(s string) bool { return strings.Contains(s, "Findings - W 1") })
	a.key("\t")
	a.drawn(func(s string) bool { return strings.Contains(s, "Findings - W 1*") })
	a.send(`{"step":"agent","state":"started"}`)
	a.drawn(func(s string) bool { return strings.Contains(s, "Agent*") && !strings.Contains(s, "Findings") })
	a.send(`{"step":"agent","state":"passed"}`)
	a.send(`{"step":"delivery","state":"started"}`)
	a.drawn(func(s string) bool { return strings.Contains(s, "ATM*") && strings.Contains(s, "Findings - W 1") })
	a.key("\t")
	a.drawn(func(s string) bool {
		return strings.Contains(s, "Findings - W 1*") && strings.Contains(s, "╭─ Log")
	})
	a.send(`{"step":"delivery","state":"passed"}`)
	a.end()
}
