//go:build unix

package run

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

func TestRealScreenKeepsEverySlopCheckAttempt(t *testing.T) {
	builtATM(t)
	for _, named := range []bool{true, false} {
		t.Run(fmt.Sprintf("named failure %t", named), func(t *testing.T) {
			a, barrier := slopAttemptScreen(t, named)
			a.drawn(func(s string) bool { return strings.Contains(s, "command") && strings.Contains(s, "running") })
			a.key("\x1b[H")
			s := a.drawn(func(s string) bool { return strings.Contains(s, "✓ Issue") })
			attempts := 3
			if named {
				attempts = 2
			}
			seen := map[int]bool{}
			for scroll := 0; scroll < 40; scroll++ {
				panel := slopAttemptColumn(s)
				observeSlopAttempts(t, panel, attempts, named, seen)
				if len(seen) == attempts {
					break
				}
				a.key("\x1b[B")
				s = a.drawn(func(s string) bool { return slopAttemptColumn(s) != panel })
			}
			if len(seen) != attempts {
				t.Fatalf("issue 586: after success, earlier checks lost their result, file or duration: %v\n%s", seen, s)
			}
			free(t, barrier)
			deadline := time.After(time.Minute)
			for open := true; open; {
				select {
				case _, more := <-a.chunks:
					open = more
				case <-deadline:
					t.Fatal("ATM did not close after delivery")
				}
			}
			if err := a.cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func slopAttemptScreen(t *testing.T, named bool) (*attached, string) {
	t.Helper()
	barrier := filepath.Join(t.TempDir(), "delivery")
	held(t, barrier)
	failure := "failed"
	if named {
		failure = "b.txt"
	}
	lint := "! grep -q broken b.txt || { sleep 0.1; echo " + failure + "; exit 1; }"
	cfg := atmSet(atmSet(atmYAML, "lint", lint), "delivery", fmt.Sprintf("cat %q >/dev/null", barrier))
	top := gitT(t, repo(t, "https://example.com/o/r.git", cfg), "rev-parse", "--show-toplevel")
	t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest+"; printf 'good\\nextra\\n' > b.txt")
	t.Setenv("FAKE_PONYTAIL_WORK", "echo fixed > a.txt; echo broken > b.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", dependentCuts("a.txt", "b.txt"))
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	cmd := exec.Command(versionServer(t, top, "follow-test"), "run", issueFile(t, issue))
	cmd.Dir = top
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = terminal.Close() })
	a := &attached{t: t, cmd: cmd, p: terminal, chunks: make(chan string, 64), rows: 24}
	go a.read()
	return a, barrier
}

func slopAttemptColumn(screen string) string {
	var lines []string
	for _, line := range strings.Split(screen, "\n") {
		lines = append(lines, ansi.Cut(line, 2, 36))
	}
	return strings.Join(lines, "\n")
}

func observeSlopAttempts(t *testing.T, panel string, attempts int, named bool, seen map[int]bool) {
	t.Helper()
	flat := strings.Join(strings.Fields(panel), " ")
	for n := 1; n <= attempts; n++ {
		state, note := "failed", fmt.Sprintf("run %d failed", n)
		if n == attempts {
			state, note = "passed", fmt.Sprintf("run %d ok", n)
		} else if named {
			note += ": b.txt"
		}
		pattern := fmt.Sprintf(`checks run %d ([0-9.]+) s %s %s`, n, state, regexp.QuoteMeta(note))
		if match := regexp.MustCompile(pattern).FindStringSubmatch(flat); match != nil {
			duration, err := strconv.ParseFloat(match[1], 64)
			if err != nil || n < attempts && duration < 0.1 {
				t.Fatalf("failed attempt %d lost its elapsed time: %q", n, match[0])
			}
			seen[n] = true
		}
	}
}
