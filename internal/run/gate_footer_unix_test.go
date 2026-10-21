//go:build unix

package run

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// The real atm attach answers the gate from its footer through no-mistakes' own commands, in the run's clone: a, f
// and s approve, fix and skip the step waiting; enter takes a line, a fix's instructions, naming the findings it
// fixes by the ids the right column shows, or, while the reviewer asks, the answer; x, then y, aborts.
func TestRealAttachFooterAnswersTheGate(t *testing.T) {
	nm, clone := gateFake(t), t.TempDir()
	setFindings(t, nm, "findings[2]{id,severity,file,action,description}:\n"+
		`  delivery,error,internal/run/run.go,auto-fix,"Delivery drops the \"gate\", verdict and all."`+"\n"+
		`  gate,warning,internal/tui/gate.go,auto-fix,"The footer lacks the gate's keys."`+"\n")
	a := liveAttach(t, 200, 40)
	a.delivered(clone)
	a.sees(func(s string) bool { return strings.Contains(s, "x abort") && !strings.Contains(s, "a approve") })
	a.sees(func(s string) bool {
		return strings.Contains(s, "delivery error internal/run/run.go") &&
			strings.Contains(s, "gate warning internal/tui/gate.go") && strings.Contains(s, "a approve")
	})
	a.key("\x1b[<0;60;2M\x1b[<0;60;2m") // a click on review's header, to see what the fake records in its log
	a.sees(func(s string) bool { return strings.Contains(s, "▾ ⏸ review") })
	a.answers(`\{axi\}\{respond\}\{--action\}\{approve\} `, "a")
	a.awaitingAgain(nm)
	a.answers(`\{axi\}\{respond\}\{--action\}\{fix\}\{--findings\}\{delivery,gate\} `, "f", "\r", "\r")
	a.awaitingAgain(nm)
	a.answers(`\{axi\}\{respond\}\{--action\}\{skip\} `, "s")
	a.awaitingAgain(nm)
	a.footer("fix › █", "\r")
	a.footer("fix › oops█", "oops")
	a.footer("a approve", "\x1b")
	a.footer("enter fix with a line", "f")
	a.footer("fix › █", "\r")
	a.footer("fix › keep it shorx█", "keep it", " ", "shorx")
	a.footer("fix › keep it short█", "\x7f", "t")
	a.answers(`\{--findings\}\{delivery,gate\}\{--instructions\}\{keep it short\} `, "\r")
	a.awaitingAgain(nm)
	a.footer("fix › gate: only the footer█", "\r", "gate: only the footer")
	a.answers(`\{--findings\}\{gate\}\{--ignore\}\{delivery\}\{--instructions\}\{only the footer\} `, "\r")
	a.awaitingAgain(nm)
	long := strings.Repeat("keep 界é ", 20) + "visible tail"
	if err := pty.Setsize(a.p, &pty.Winsize{Cols: 80, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	a.footer("visible tail█", "f", "\r", long)
	lines := strings.Split(strings.TrimRight(a.screen(), "\n"), "\n")
	line := lines[len(lines)-1]
	if !strings.Contains(line, "fix › ") || ansi.StringWidth(line) > 80 || strings.Contains(line, long) {
		t.Fatalf("the editable suffix and cursor must fit one terminal row: %q", line)
	}
	a.key("\r")
	a.awaitingAgain(nm)
	if err := pty.Setsize(a.p, &pty.Winsize{Cols: 200, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	setFindings(t, nm, "findings[4]{id,severity,file,action,description}:\n"+
		`  question-q1,warning,,ask,"Keep the old name?"`+"\n"+
		`  question-q2,warning,,ask,"Keep the old layout?"`+"\n"+
		`  delivery,error,internal/run/run.go,auto-fix,"Delivery drops the \"gate\", verdict and all."`+"\n"+
		`  gate,warning,internal/tui/gate.go,auto-fix,"The footer lacks the gate's keys."`+"\n")
	a.sees(func(s string) bool { return strings.Contains(s, "question-q1") && strings.Contains(s, "enter answer") })
	a.footer("answer › rejected█", "\r", "rejected")
	a.answers("Answer refused", "\r")
	a.footer("answer › keep it█", "\r", "keep it")
	a.answers(`\{axi\}\{answer\}\{--question\}\{question-q1\}\{--answer\}\{keep it\} `, "\r")
	a.footer("y abort the gate", "x")
	a.footer("enter answer", "n")
	a.footer("answer › keep layout█", "\r", "keep layout")
	a.answers(`\{axi\}\{answer\}\{--question\}\{question-q2\}\{--answer\}\{keep layout\} `, "\r")
	a.sees(func(s string) bool { return !strings.Contains(s, "enter answer") && !strings.Contains(s, "a approve") })
	a.footer("y abort the gate", "x")
	a.key("y")
	a.exits()
	log, _ := os.ReadFile(filepath.Join(nm, "log"))
	if strings.Count(string(log), " axi respond ") != 6 ||
		!strings.Contains(string(log), "--instructions "+long+"\n") ||
		strings.Count(string(log), "--question question-q1 --answer keep it\n") != 1 ||
		strings.Count(string(log), "--question question-q2 --answer keep layout\n") != 1 {
		t.Fatalf("each action must send exactly one complete response:\n%s", log)
	}
	if !strings.Contains(string(log), clone+" axi respond --action approve\n") ||
		!strings.Contains(string(log), clone+" axi abort\n") {
		t.Fatalf("the footer did not answer the gate in the run's clone:\n%s", log)
	}
}

// setFindings makes the findings review shows those of table, a TOON findings table.
func setFindings(t *testing.T, nm, table string) {
	t.Helper()
	path := filepath.Join(nm, "review.toon")
	toon, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	set := regexp.MustCompile(`(?s)findings\[.*?\n(lines:)`).ReplaceAllString(string(toon), table+"$1")
	if err := os.WriteFile(path, []byte(set), 0o600); err != nil {
		t.Fatal(err)
	}
}

// answers types keys and reads the screen until review's log shows what the fake no-mistakes recorded, want.
func (a *attached) answers(want string, keys ...string) {
	a.t.Helper()
	for _, k := range keys {
		a.key(k)
	}
	recorded := regexp.MustCompile(want)
	a.sees(recorded.MatchString)
}

// footer types each of keys and reads the screen until its footer shows want.
func (a *attached) footer(want string, keys ...string) {
	a.t.Helper()
	for _, k := range keys {
		a.key(k)
	}
	a.sees(func(s string) bool { return strings.Contains(s, want) })
}

func (a *attached) awaitingAgain(nm string) {
	a.t.Helper()
	a.sees(func(s string) bool { return !strings.Contains(s, "a approve") && !strings.Contains(s, "fix ›") })
	if err := os.Remove(filepath.Join(nm, "fixing")); err != nil {
		a.t.Fatal(err)
	}
	a.sees(func(s string) bool { return strings.Contains(s, "a approve") })
}

func TestRealAttachFooterKeepsNavigation(t *testing.T) {
	for _, mode := range []string{"abort", "text"} {
		t.Run(mode, func(t *testing.T) {
			nm := gateFake(t)
			longGateLog(t, nm)
			a := liveAttach(t, 120, 12)
			a.delivered(t.TempDir())
			a.footer("a approve")
			a.key("\x1b[<0;45;2M\x1b[<0;45;2m")
			a.footer("review-line-79")
			if mode == "abort" {
				a.footer("y abort the gate", "x")
			} else {
				a.footer("fix › thq█", "\r", "thq")
			}
			a.key("\t\x1b[H")
			s := a.sees(func(s string) bool { return strings.Contains(s, "– Issue") })
			if !strings.Contains(s, "review-line-79") {
				t.Fatalf("Tab and Home must scroll ATM: %s", s)
			}
			a.key("\t")
			if mode == "abort" {
				a.footer("y abort the gate", "x")
			}
			a.key("\x1b[A")
			a.sees(func(s string) bool {
				return !strings.Contains(s, "delivery error") && strings.Contains(s, "review-line-79")
			})
			if mode == "abort" {
				a.footer("y abort the gate", "x")
			}
			a.key("\x1b[B")
			a.footer("delivery error")
			if mode == "abort" {
				a.footer("y abort the gate", "x")
				a.key("q")
			} else {
				a.footer("fix › thq█")
				a.key("\x03")
			}
			a.exits()
			log, _ := os.ReadFile(filepath.Join(nm, "log"))
			if strings.Contains(string(log), " axi abort") || strings.Contains(string(log), " axi respond") {
				t.Fatalf("navigation or exit answered the gate: %s", log)
			}
		})
	}
}

func TestRealAttachFooterAnswersStatusQuestions(t *testing.T) {
	nm := gateFake(t)
	if err := os.WriteFile(filepath.Join(nm, "status.toon"), []byte(`gate:
  step: review
  findings[3]{id,severity,file,action,description}:
    delivery,error,internal/run/run.go,auto-fix,Status copy
    question-q1,warning,,ask,Keep the old name?
    question-q2,warning,,ask,Keep the old layout?
`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := liveAttach(t, 200, 40)
	a.delivered(t.TempDir())
	s := a.sees(func(s string) bool {
		return strings.Contains(s, "question-q1") && strings.Contains(s, "question-q2") && strings.Contains(s, "enter answer")
	})
	if strings.Count(s, "delivery error internal/run/run.go") != 1 || strings.Contains(s, "Status copy") {
		t.Fatalf("status must preserve log findings without duplicating IDs: %s", s)
	}
	a.key("\x1b[<0;60;2M\x1b[<0;60;2m")
	a.sees(func(s string) bool { return strings.Contains(s, "▾ ⏸ review") })
	a.footer("answer › keep name█", "\r", "keep name")
	a.answers(`\{axi\}\{answer\}\{--question\}\{question-q1\}\{--answer\}\{keep name\} `, "\r")
	a.footer("answer › keep layout█", "\r", "keep layout")
	a.answers(`\{axi\}\{answer\}\{--question\}\{question-q2\}\{--answer\}\{keep layout\} `, "\r")
	a.sees(func(s string) bool { return !strings.Contains(s, "enter answer") && !strings.Contains(s, "a approve") })
	a.key("q")
	a.exits()
}
