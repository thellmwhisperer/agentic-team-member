//go:build unix

package run

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
	a.answers(`\{axi\}\{respond\}\{--action\}\{fix\}\{--findings\}\{delivery,gate\} `, "f")
	a.answers(`\{axi\}\{respond\}\{--action\}\{skip\} `, "s")
	a.footer("fix › █", "\r")
	a.footer("fix › oops█", "oops")
	a.footer("a approve", "\x1b")
	a.footer("fix › █", "\r")
	a.footer("fix › keep it shorx█", "keep it", " ", "shorx")
	a.footer("fix › keep it short█", "\x7f", "t")
	a.answers(`\{--findings\}\{delivery,gate\}\{--instructions\}\{keep it short\} `, "\r")
	a.footer("fix › gate: only the footer█", "\r", "gate: only the footer")
	a.answers(`\{--findings\}\{gate\}\{--ignore\}\{delivery\}\{--instructions\}\{only the footer\} `, "\r")
	setFindings(t, nm, "findings[3]{id,severity,file,action,description}:\n"+
		`  question-q1,warning,,ask,"Keep the old name?"`+"\n"+
		`  delivery,error,internal/run/run.go,auto-fix,"Delivery drops the \"gate\", verdict and all."`+"\n"+
		`  gate,warning,internal/tui/gate.go,auto-fix,"The footer lacks the gate's keys."`+"\n")
	a.sees(func(s string) bool { return strings.Contains(s, "question-q1") && strings.Contains(s, "enter answer") })
	a.footer("answer › keep it█", "\r", "keep it")
	a.answers(`\{axi\}\{answer\}\{--question\}\{question-q1\}\{--answer\}\{keep it\} `, "\r")
	a.footer("y abort the gate", "x")
	a.footer("enter answer", "n")
	a.footer("y abort the gate", "x")
	a.key("y")
	a.exits()
	log, _ := os.ReadFile(filepath.Join(nm, "log"))
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
