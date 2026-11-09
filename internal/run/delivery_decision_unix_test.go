//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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
	sc.until(func(s string) bool { return waitsForAnswer(s) && strings.Contains(s, "╭─ Gate") })
	sc.during(2 * time.Second) // the screen draws the run's wait for its answer
	sc.deliveryNotFailed()
	sc.without("wait of 8m0s", "the delivery command's output")
	if _, err := sc.f.term.Write([]byte("\t")); err != nil {
		t.Fatal(err)
	}
	focused := regexp.MustCompile(`Findings[^\n]*\*`)
	sc.until(focused.MatchString)
	for _, cols := range []uint16{80, 100} {
		sc.resized(cols, func(s string) bool {
			return waitsForAnswer(s) && strings.Index(s, "Findings") < strings.Index(s, "╭─ Gate")
		})
	}
	answer := exec.Command(sc.f.cmd.Path, "answer", runs[0].Run, "approve")
	answer.Dir = top
	if out, err := answer.CombinedOutput(); err != nil {
		t.Fatalf("atm answer: %v\n%s", err, out)
	}
	sc.gateEnds(nm)
	sc.gateNotHidden()
	sc.noAbortOnceOver()
}

// noAbortOnceOver fails the test when the last view, of a run and a gate both over, offers to abort the gate.
func (sc *screenRead) noAbortOnceOver() {
	sc.f.t.Helper()
	if s := sc.final(); !strings.Contains(s, "q close") || strings.Contains(s, "x abort") {
		sc.f.t.Fatalf("the last view offers to abort a gate over:\n%s", s)
	}
}

// without fails the test when the last screen shows text, which is what.
func (sc *screenRead) without(text, what string) {
	sc.f.t.Helper()
	if s := sc.last(); strings.Contains(s, text) {
		sc.f.t.Fatalf("the screen showed %s:\n%s", what, s)
	}
}

// waitsForAnswer says screen s shows the run waiting for its launcher's answer.
func waitsForAnswer(s string) bool {
	return strings.Contains(s, "⏸ waiting") && strings.Contains(s, "atm answer") &&
		strings.Contains(s, "approve|fix|stop")
}

// deliveryNotFailed fails the test when the last screen shows the Delivery row failed.
func (sc *screenRead) deliveryNotFailed() {
	sc.f.t.Helper()
	failed := regexp.MustCompile(`Delivery  \d+\.\d s +failed`)
	if s := sc.last(); strings.Contains(s, "✗ Delivery") || failed.MatchString(s) {
		sc.f.t.Fatalf("Delivery showed failed while its gate goes on:\n%s", s)
	}
}

// gateNotHidden fails the test when the last screen still shows the answered decision instead of the Gate box.
func (sc *screenRead) gateNotHidden() {
	sc.f.t.Helper()
	if s := sc.last(); strings.Contains(s, "Findings") || strings.Contains(s, "atm answer") ||
		!strings.Contains(s, "╭─ Gate") {
		sc.f.t.Fatalf("answered decision still hides the gate:\n%s", s)
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
