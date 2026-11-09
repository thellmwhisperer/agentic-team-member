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
	sc.during(2 * time.Second) // the screen draws the run's wait for its answer
	if s := sc.last(); strings.Contains(s, "✗ Delivery") || strings.Contains(s, "failed") {
		t.Fatalf("Delivery showed failed while its gate goes on:\n%s", s)
	}
	answer := exec.Command(sc.f.cmd.Path, "answer", runs[0].Run, "approve")
	answer.Dir = top
	if out, err := answer.CombinedOutput(); err != nil {
		t.Fatalf("atm answer: %v\n%s", err, out)
	}
	sc.gateEnds(nm)
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
