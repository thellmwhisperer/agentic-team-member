//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// A delivery command that ends with exit 1 while its gate goes on, as no-mistakes axi run does once its default
// hold elapses. The real atm run's screen shows Delivery running, never failed, while the gate goes on and the run
// waits for its launcher's answer; once answered, the screen follows the run to its real end.
func TestRealRunScreenKeepsDeliveryRunningWhileItsGateGoesOn(t *testing.T) {
	nm := gateFake(t)
	if err := os.WriteFile(filepath.Join(nm, "fixing"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	top, sc := screenRun(t, `printf "error: wait of 8m0s elapsed while driving the run\n"; exit 1`)
	delivering := regexp.MustCompile(`Delivery  \d+\.\d s +running`)
	sc.until(func(s string) bool { return delivering.MatchString(s) && strings.Contains(s, "review") })
	runs := waitingRuns(t, top)
	sc.until(func(s string) bool {
		return strings.Contains(s, "⏸ waiting") && strings.Contains(s, "atm answer") &&
			strings.Contains(s, "approve|fix|stop") && strings.Contains(s, "╭─ Gate")
	})
	sc.during(2 * time.Second) // the screen draws the run's wait for its answer
	failed := regexp.MustCompile(`Delivery  \d+\.\d s +failed`)
	if s := sc.last(); strings.Contains(s, "✗ Delivery") || failed.MatchString(s) {
		t.Fatalf("Delivery showed failed while its gate goes on:\n%s", s)
	}
	if _, err := sc.f.term.Write([]byte("\t")); err != nil {
		t.Fatal(err)
	}
	focused := regexp.MustCompile(`Findings[^\n]*\*`)
	sc.until(focused.MatchString)
	for _, cols := range []uint16{80, 100} {
		if err := pty.Setsize(sc.f.term, &pty.Winsize{Rows: 36, Cols: cols}); err != nil {
			t.Fatal(err)
		}
		if err := sc.f.cmd.Process.Signal(syscall.SIGWINCH); err != nil {
			t.Fatal(err)
		}
		sc.until(func(s string) bool {
			header, _, _ := strings.Cut(s, "\n")
			return ansi.StringWidth(header) == int(cols) && strings.Contains(s, "⏸ waiting") &&
				strings.Contains(s, "atm answer") && strings.Contains(s, "approve|fix|stop") &&
				strings.Index(s, "Findings") < strings.Index(s, "╭─ Gate")
		})
	}
	answer := exec.Command(sc.f.cmd.Path, "answer", runs[0].Run, "approve")
	answer.Dir = top
	if out, err := answer.CombinedOutput(); err != nil {
		t.Fatalf("atm answer: %v\n%s", err, out)
	}
	sc.gateEnds(nm)
	if s := sc.last(); strings.Contains(s, "Findings") || strings.Contains(s, "atm answer") ||
		!strings.Contains(s, "╭─ Gate") {
		t.Fatalf("answered decision still hides the gate:\n%s", s)
	}
}

// waitingRuns is the runs of top once the only one waits for its launcher's answer, 30 s at most.
func waitingRuns(t *testing.T, top string) []Outcome {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		runs, err := Runs(top)
		if err == nil && len(runs) == 1 && runs[0].Waits() {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("want one run waiting for an answer, got %+v, %v", runs, err)
		}
	}
}
