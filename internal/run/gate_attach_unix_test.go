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
agent_step=${FAKE_NM_STEP:-review}
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
	printf '    %s,%s,1,1200\n    test,pending,0,0\n    lint,skipped,0,0\n' "$agent_step" "$step"
	[ ! -f "$FAKE_NM/status.toon" ] || cat "$FAKE_NM/status.toon" ;;
"axi logs --step "*" --full")
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
	assertGateReport(t, a)
	if err := os.WriteFile(filepath.Join(nm, "fixing"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fixing := a.drawn(func(s string) bool {
		return regexp.MustCompile(`│ [⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏] review  1\.2 s +running │`).MatchString(s) &&
			regexp.MustCompile(`│ ▾ [⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏] review +│`).MatchString(s)
	})
	if !strings.Contains(fixing, finding) || strings.Contains(fixing, "internal/run/run.go:") {
		t.Fatalf("fixing lost or invented a finding position:\n%s", fixing)
	}
	if !strings.Contains(fixing, "▾ Ficheros revisados (2)") {
		t.Fatalf("polling lost the open reviewed paths:\n%s", fixing)
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

func assertGateReport(t *testing.T, a *attached) {
	t.Helper()
	s := a.drawn(func(s string) bool { return strings.Contains(s, "checking the delivery") })
	for _, raw := range []string{"{", `"findings"`, `"risk_level"`, `\"`, "pid="} {
		if strings.Contains(s, raw) {
			t.Fatalf("the gate paints raw agent output %q:\n%s", raw, s)
		}
	}
	for _, want := range []string{"✎ Reviewing the diff", "▾ Informe", "Review completed.",
		`warning internal/tui/gate.go:17 Keep "gate" visible.`, "Riesgo: low", "Only the screen changes.",
		"Gate tests pass.", "▸ Ficheros revisados (2)"} {
		if !strings.Contains(s, want) {
			t.Fatalf("the gate lacks readable report content %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "internal/run/gate_attach_unix_test.go") {
		t.Fatalf("reviewed paths are not folded:\n%s", s)
	}
	a.click(rowOf(s, "▸ Ficheros revisados"))
	s = a.drawn(func(s string) bool { return strings.Contains(s, "internal/run/gate_attach_unix_test.go") })
	a.click(rowOf(s, "▾ Informe"))
	s = a.drawn(func(s string) bool { return strings.Contains(s, "▸ Informe") })
	if strings.Contains(s, "Riesgo: low") || strings.Contains(s, "Ficheros revisados") {
		t.Fatalf("folding Informe left its content visible:\n%s", s)
	}
	a.click(rowOf(s, "▸ Informe"))
	a.drawn(func(s string) bool { return strings.Contains(s, "▾ Ficheros revisados (2)") })
}

func TestRealAttachReadsGateFixAndCheckLogs(t *testing.T) {
	for _, tc := range []struct {
		name, log    string
		want, absent []string
	}{
		{"fix", `codex started pid=2
Fixing **the gate**
{"summary":"Fixed the gate."}
{"findings":[{"severity":"error","file":"gate.go","description":"No position given."}]}
Fix complete
codex exited pid=2 status=success`,
			[]string{"✎ Fixing the gate", "▾ Informe", "Fixed the gate.", "error gate.go No position given.", "✎ Fix complete"},
			[]string{"{", "pid=", "gate.go:", "Ficheros revisados", "Comandos ejecutados", "Test "}},
		{"typecheck", "Checking packages\n{not a report}\n{\"packages\":3}\nChecks passed",
			[]string{"Checking packages", "{not a report}", `{"packages":3}`, "Checks passed"},
			[]string{"✎", "Informe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nm := gateFake(t)
			t.Setenv("FAKE_NM_STEP", tc.name)
			fixture, err := os.ReadFile(filepath.Join(nm, "review.toon"))
			if err != nil {
				t.Fatal(err)
			}
			header, _, _ := strings.Cut(string(fixture), "lines:")
			lines := strings.Split(tc.log, "\n")
			fixture = []byte(header + fmt.Sprintf("lines: %d total\nlog[%d]{line}:\n", len(lines), len(lines)))
			for _, line := range lines {
				fixture = append(fixture, []byte(fmt.Sprintf("  %q\n", line))...)
			}
			if err := os.WriteFile(filepath.Join(nm, "review.toon"), fixture, 0o600); err != nil {
				t.Fatal(err)
			}
			a := liveAttach(t, 120, 40)
			a.delivered(t.TempDir())
			a.drawn(func(s string) bool { return strings.Contains(s, "▸ ⏸ "+tc.name) })
			a.click(1)
			s := a.drawn(func(s string) bool { return strings.Contains(s, "▾ ⏸ "+tc.name) })
			for _, want := range tc.want {
				if !strings.Contains(s, want) {
					t.Fatalf("the gate lacks %q:\n%s", want, s)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(s, absent) {
					t.Fatalf("the gate unexpectedly paints %q:\n%s", absent, s)
				}
			}
			if err := os.WriteFile(filepath.Join(nm, "done"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			a.exits()
		})
	}
}

func TestRealAttachFoldsGateReports(t *testing.T) {
	for _, step := range []string{"review", "fix"} {
		t.Run(step, func(t *testing.T) {
			nm := gateFake(t)
			t.Setenv("FAKE_NM_STEP", step)
			log := `{"summary":"Report completed.","findings":[{"severity":"warning","file":"gate.go","line":17,"description":"Keep the gate visible."}],"risk_level":"low","reviewed_paths":["reviewed.go"]}`
			fixture := fmt.Sprintf("step: %s\nlines: 3 total\nlog[3]{line}:\n  codex started pid=1\n  %q\n  codex exited pid=1 status=success\n", step, log)
			if err := os.WriteFile(filepath.Join(nm, "review.toon"), []byte(fixture), 0o600); err != nil {
				t.Fatal(err)
			}
			a := liveAttach(t, 120, 40)
			a.delivered(t.TempDir())
			assertHidden := func(s string) {
				t.Helper()
				for _, absent := range []string{"Report completed.", "warning gate.go:17", "Keep the gate visible.",
					"Riesgo: low", "Ficheros revisados", "reviewed.go", "pid=", "{"} {
					if strings.Contains(s, absent) {
						t.Fatalf("folded gate report paints %q:\n%s", absent, s)
					}
				}
			}
			s := a.drawn(func(s string) bool { return strings.Contains(s, "▸ ⏸ "+step) })
			if strings.Count(s, "Informe") != 1 {
				t.Fatalf("folded step must preview one report row:\n%s", s)
			}
			assertHidden(s)
			a.click(1)
			s = a.drawn(func(s string) bool { return strings.Contains(s, "▾ ⏸ "+step) })
			for _, want := range []string{"▾ Informe", "Report completed.", "warning gate.go:17 Keep the gate visible.",
				"Riesgo: low", "▸ Ficheros revisados (1)"} {
				if !strings.Contains(s, want) {
					t.Fatalf("open gate report lacks %q:\n%s", want, s)
				}
			}
			a.click(rowOf(s, "▸ Ficheros revisados"))
			a.drawn(func(s string) bool { return strings.Contains(s, "reviewed.go") })
			a.click(1)
			s = a.drawn(func(s string) bool { return strings.Contains(s, "▸ ⏸ "+step) })
			assertHidden(s)
			a.click(1)
			s = a.drawn(func(s string) bool { return strings.Contains(s, "reviewed.go") })
			a.click(rowOf(s, "▾ Informe"))
			s = a.drawn(func(s string) bool { return strings.Contains(s, "▸ Informe") })
			assertHidden(s)
			a.click(1)
			a.drawn(func(s string) bool { return strings.Contains(s, "▸ ⏸ "+step) })
			a.click(1)
			s = a.drawn(func(s string) bool { return strings.Contains(s, "▾ ⏸ "+step) })
			if !strings.Contains(s, "▸ Informe") {
				t.Fatalf("opening the step lost the report's fold:\n%s", s)
			}
			assertHidden(s)
			a.click(rowOf(s, "▸ Informe"))
			a.drawn(func(s string) bool { return strings.Contains(s, "reviewed.go") })
			if err := os.WriteFile(filepath.Join(nm, "done"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			a.exits()
		})
	}
}

func assertGateFolded(t *testing.T, a *attached, folded string) {
	t.Helper()
	for _, want := range []string{`│ ⏸ review  1\.2 s +waiting │`, `│ ○ test +pending │`, `│ – lint +skipped │`,
		`│ ▸ ⏸ review +│`, `│ ✎ Reviewing the diff +│`} {
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
		!strings.Contains(last, "checking the delivery") || !strings.Contains(ansi.Strip(last), "▾ ✓ review") ||
		!regexp.MustCompile(`│ ✓ review  1\.2 s +passed │`).MatchString(ansi.Strip(last)) {
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

func TestRealAttachKeepsGateLogPosition(t *testing.T) {
	for _, step := range []string{"review", "fix"} {
		t.Run(step, func(t *testing.T) {
			nm := gateFake(t)
			t.Setenv("FAKE_NM_STEP", step)
			fixture := "findings[12]{id,severity,file,action,description}:\n"
			for i := range 12 {
				fixture += fmt.Sprintf("  finding-%02d,warning,gate.go,auto-fix,Finding %02d\n", i, i)
			}
			log := []string{"Before report", `{"summary":"First report."}`, "After first report",
				"Continuation after first report", `{"summary":"Second report."}`, "After second report",
				"Continuation after second report"}
			fixture += fmt.Sprintf("lines: %d total\nlog[%d]{line}:\n", len(log), len(log))
			for _, line := range log {
				fixture += fmt.Sprintf("  %q\n", line)
			}
			if err := os.WriteFile(filepath.Join(nm, "review.toon"), []byte(fixture), 0o600); err != nil {
				t.Fatal(err)
			}
			a := liveAttach(t, 120, 12)
			a.delivered(t.TempDir())
			a.drawn(func(s string) bool { return strings.Contains(s, "waiting") })
			top := func(s string) string { return logColumn(strings.Split(s, "\n")[1]) }
			a.key("\x1b[H")
			a.drawn(func(s string) bool { return strings.Contains(top(s), "▸ ⏸ "+step) })
			a.click(1)
			a.drawn(func(s string) bool { return strings.Contains(top(s), "▾ ⏸ "+step) })
			for i, target := range []struct {
				row  int
				text string
			}{{1, "✎ Before report"}, {4, "✎ After first report"}, {5, "Continuation after first report"},
				{8, "✎ After second report"}, {10, "finding-00"}, {11, "finding-01"}} {
				a.key("\x1b[H" + strings.Repeat("\x1b[B", target.row))
				a.drawn(func(s string) bool { return strings.Contains(top(s), target.text) })
				state := "running"
				if i%2 == 0 {
					if err := os.WriteFile(filepath.Join(nm, "fixing"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					state = "waiting"
					if err := os.Remove(filepath.Join(nm, "fixing")); err != nil {
						t.Fatal(err)
					}
				}
				updated := regexp.MustCompile(step + `  1\.2 s +` + state)
				s := a.drawn(updated.MatchString)
				if !strings.Contains(top(s), target.text) {
					t.Fatalf("gate update moved the reading position from %q:\n%s", target.text, s)
				}
			}
			if err := os.WriteFile(filepath.Join(nm, "done"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			a.exits()
		})
	}
}

func longGateLog(t *testing.T, nm string) []string {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join(nm, "review.toon"))
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := strings.Cut(string(fixture), "lines:")
	long := header + "lines: 83 total\nlog[83]{line}:\n" +
		"  **Reviewing the diff**\n  reading internal/run/run.go\n  checking the delivery\n"
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
