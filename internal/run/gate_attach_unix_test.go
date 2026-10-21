//go:build unix

package run

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// gateNM answers like no-mistakes for the screen: axi status, in the run's clone, shows its branch's run, review
// running on its first call and then waiting with one finding, until $FAKE_NM/done makes the run complete; axi
// logs --step review shows review's log and finding. With $FAKE_NM/none the branch has no run. axi respond, answer
// and abort add their arguments, each in braces, to review's log, and abort makes the run complete.
const gateNM = `#!/bin/sh
echo "$PWD $*" >>"$FAKE_NM/log"
case "$*" in
"axi status")
	if [ -f "$FAKE_NM/none" ]; then printf 'current_branch: atm/the-unit\nruns_on_current_branch: 0\n'; exit 0; fi
	n=$(($(cat "$FAKE_NM/n" 2>/dev/null) + 1))
	echo "$n" >"$FAKE_NM/n"
	run=running step=awaiting_approval
	[ "$n" -gt 1 ] || step=running
	[ ! -f "$FAKE_NM/fixing" ] || step=fixing
	[ ! -f "$FAKE_NM/done" ] || run=completed step=completed
	printf 'run:\n  id: "01M5D0SWRMZ44NM79H2NNC3YZ8"\n  branch: atm/the-unit\n  status: %s\n' "$run"
	printf '  steps[3]{step,status,findings,duration_ms}:\n'
	printf '    review,%s,1,1200\n    test,pending,0,0\n    lint,skipped,0,0\n' "$step" ;;
"axi logs --step review --full")
	cat "$FAKE_NM/review.toon" ;;
"axi respond "*|"axi answer "*|"axi abort")
	if [ "$2" = respond ]; then
		[ ! -f "$FAKE_NM/fixing" ] || exit 1
		touch "$FAKE_NM/fixing"
	fi
	if [ "$2" = answer ]; then
		if [ "$6" = rejected ]; then printf "  Answer refused\n" >>"$FAKE_NM/review.toon"; exit 1; fi
		[ ! -f "$FAKE_NM/$4" ] || exit 1
		touch "$FAKE_NM/$4"
		[ "$4" != question-q2 ] || touch "$FAKE_NM/fixing"
	fi
	printf '  "%s"\n' "$(for a; do printf '{%s}' "$a"; done)" >>"$FAKE_NM/review.toon"
	[ "$2" != abort ] || touch "$FAKE_NM/done" ;;
*) exit 2 ;;
esac
`

// gateFake puts the fake no-mistakes first on PATH and returns its directory.
func gateFake(t *testing.T) string {
	t.Helper()
	tools, nm := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "no-mistakes"), []byte(gateNM), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/gate-review.toon")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nm, "review.toon"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_NM", nm)
	return nm
}

// delivered serves a run whose clone is clone up to its passed Delivery, and its end.
func (a *attached) delivered(clone string) {
	a.send(`{"step":"clone","state":"passed","clone":"` + clone + `","sha":"433ecbac"}`)
	a.send(`{"step":"delivery","state":"started"}`)
	a.send(`{"step":"delivery","state":"passed","branch":"atm/the-unit"}`)
	close(a.events)
	if err := <-a.served; err != nil {
		a.t.Fatal(err)
	}
}

// After a run's Delivery, the real atm attach paints the gate the run's clone shows: its steps below ATM's in the
// ATM box and, in the right column, each started step's header, its log rendered as the Agent box renders
// Markdown, its first line while folded and every line once clicked open, and its findings, until the gate ends.
func TestRealAttachPaintsTheGate(t *testing.T) {
	nm, clone := gateFake(t), t.TempDir()
	a := liveAttach(t, 120, 40)
	a.delivered(clone)
	finding := `delivery error internal/run/run.go Delivery drops the "gate"`
	folded := a.sees(func(s string) bool { return strings.Contains(s, finding) && strings.Contains(s, "▸ ⏸ review") })
	assertGateFolded(t, a, folded)
	a.key("\x1b[<0;45;2M\x1b[<0;45;2m") // a click on review's header, the right column's first row
	a.sees(func(s string) bool {
		return strings.Contains(s, "▾ ⏸ review") && strings.Contains(s, "reading internal/run/run.go") &&
			strings.Contains(s, "checking the delivery") && strings.Contains(s, finding)
	})
	if err := os.WriteFile(filepath.Join(nm, "fixing"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fixing := a.sees(func(s string) bool {
		return regexp.MustCompile(`│ [⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏] review .*running │`).MatchString(s) &&
			regexp.MustCompile(`│ ▾ [⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏] review +│`).MatchString(s)
	})
	if !strings.Contains(fixing, finding) || strings.Contains(fixing, "internal/run/run.go:") {
		t.Fatalf("fixing lost or invented a finding position:\n%s", fixing)
	}
	if err := os.WriteFile(filepath.Join(nm, "done"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a.exits()
	assertGateLast(t, a)
	log, _ := os.ReadFile(filepath.Join(nm, "log"))
	if !strings.HasPrefix(string(log), clone+" axi status\n") {
		t.Fatalf("the screen did not ask no-mistakes in the run's clone:\n%s", log)
	}
}

func assertGateFolded(t *testing.T, a *attached, folded string) {
	t.Helper()
	for _, want := range []string{`│ ⏸ review .*waiting │`, `│ ○ test .*pending │`, `│ ▸ ⏸ review +│`,
		`│   Reviewing the diff +│`} {
		if !regexp.MustCompile(want).MatchString(folded) {
			t.Fatalf("the gate's screen lacks %q:\n%s", want, folded)
		}
	}
	if strings.Contains(folded, "reading internal/run/run.go") || strings.Contains(a.raw.String(), "**") ||
		!strings.Contains(a.raw.String(), "\x1b[1mReviewing the diff") {
		t.Fatalf("folded, review shows more than its first line, in bold:\n%q", a.raw.String())
	}
}

func assertGateLast(t *testing.T, a *attached) {
	t.Helper()
	last := a.raw.String()
	last = last[strings.LastIndex(last, "\x1b[?1049l"):]
	if !strings.Contains(last, "\x1b[1mReviewing the diff") || strings.Contains(last, "**") ||
		!strings.Contains(last, "checking the delivery") || !strings.Contains(ansi.Strip(last), "▾ ✓ review") {
		t.Fatalf("the gate's last screen lacks review, passed and unfolded in bold:\n%q", last)
	}
}

func TestRealAttachScrollsTheFullGateLog(t *testing.T) {
	nm := gateFake(t)
	want := longGateLog(t, nm)
	a := liveAttach(t, 120, 12)
	a.delivered(t.TempDir())
	folded := a.sees(func(s string) bool {
		return strings.Contains(s, "▸ ⏸ review") && strings.Contains(s, "Reviewing the diff")
	})
	if strings.Contains(folded, "review-line-") || strings.Contains(a.raw.String(), "**") ||
		!strings.Contains(a.raw.String(), "\x1b[1mReviewing the diff") {
		t.Fatalf("the full log lost its original folded first line:\n%s", folded)
	}
	unfoldedFrom := a.raw.Len()
	a.key("\x1b[<0;45;2M\x1b[<0;45;2m")
	rows := append([]string{"▾ ⏸ review"}, want...)
	rows = append(rows, `delivery error internal/run/run.go Delivery drops the "gate"`)
	page := func(top int) {
		a.sees(func(s string) bool {
			for _, line := range rows[top : top+7] {
				if !strings.Contains(s, line) {
					return false
				}
			}
			return true
		})
	}
	top := len(rows) - 7
	page(top)
	a.key("\t\x1b[H")
	pipeline := a.sees(func(s string) bool { return strings.Contains(s, "– Issue") })
	if !strings.Contains(pipeline, "review-line-79") {
		t.Fatalf("Tab and Home scrolled Gate instead of ATM:\n%s", pipeline)
	}
	a.key("\t")
	for top > 0 {
		top = max(0, top-6)
		a.key("\x1b[5~")
		page(top)
	}
	unfolded := a.raw.String()[unfoldedFrom:]
	if strings.Contains(unfolded, "**") || !strings.Contains(unfolded, "\x1b[1mReviewing the diff") {
		t.Fatalf("the unfolded first line lost Markdown rendering:\n%q", unfolded)
	}
	a.key("\x1b[F")
	a.sees(func(s string) bool { return strings.Contains(s, "review-line-79") })
	if err := os.WriteFile(filepath.Join(nm, "done"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a.exits()
}

func longGateLog(t *testing.T, nm string) []string {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join(nm, "review.toon"))
	if err != nil {
		t.Fatal(err)
	}
	long := strings.ReplaceAll(string(fixture), "3 total", "83 total")
	long = strings.ReplaceAll(long, "log[3]", "log[83]")
	want := []string{"Reviewing the diff", "reading internal/run/run.go", "checking the delivery"}
	for i := 0; i < 80; i++ {
		line := fmt.Sprintf("review-line-%02d", i)
		want = append(want, line)
		long += "  " + line + "\n"
	}
	if err := os.WriteFile(filepath.Join(nm, "review.toon"), []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	return want
}

func TestRealAttachShowsGateFindingsWithoutLogLines(t *testing.T) {
	nm := gateFake(t)
	fixture, err := os.ReadFile(filepath.Join(nm, "review.toon"))
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(string(fixture), "lines:")
	noLines := header + "lines: 0 total\nlog[0]{line}:\n"
	if err := os.WriteFile(filepath.Join(nm, "review.toon"), []byte(noLines), 0o600); err != nil {
		t.Fatal(err)
	}
	a := liveAttach(t, 120, 40)
	a.delivered(t.TempDir())
	a.sees(func(s string) bool {
		return strings.Contains(s, "▸ ⏸ review") &&
			strings.Contains(s, `delivery error internal/run/run.go Delivery drops the "gate"`)
	})
	a.key("\x1b[<0;45;2M\x1b[<0;45;2m")
	a.sees(func(s string) bool { return strings.Contains(s, "▾ ⏸ review") })
	if err := os.WriteFile(filepath.Join(nm, "done"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a.exits()
}

// With no gate run on the delivered branch, the real atm attach quits once the run ends, as it did.
func TestRealAttachWithoutAGateQuitsAtTheEnd(t *testing.T) {
	nm := gateFake(t)
	if err := os.WriteFile(filepath.Join(nm, "none"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := liveAttach(t, 120, 40)
	a.delivered(t.TempDir())
	a.exits()
	if log, _ := os.ReadFile(filepath.Join(nm, "log")); !strings.Contains(string(log), " axi status\n") {
		t.Fatalf("the screen did not ask no-mistakes:\n%s", log)
	}
}

// exits reads the screen until atm attach quits by itself.
func (a *attached) exits() {
	a.t.Helper()
	timeout := time.After(time.Minute)
	for ended := false; !ended; {
		select {
		case c, open := <-a.chunks:
			a.raw.WriteString(c)
			ended = !open
		case <-timeout:
			a.t.Fatalf("atm attach stayed open:\n%s", a.screen())
		}
	}
	if err := a.cmd.Wait(); err != nil {
		a.t.Fatalf("atm attach: %v", err)
	}
}
