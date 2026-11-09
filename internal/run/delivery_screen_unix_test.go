//go:build unix

package run

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A delivery that holds no-mistakes until its gate ends, as axi run --wait 2h does, writing as it goes. The real
// atm run keeps its screen all along: the pipeline shows the gate's steps below Delivery while Delivery runs, and
// the delivery's own text never takes the terminal.
func TestRealRunKeepsItsScreenWhileDeliveryHoldsTheGate(t *testing.T) {
	nm := gateFake(t)
	_, sc := screenRun(t, `printf "run: running\n  review: running\n"; until [ -f "$FAKE_NM/done" ]; do sleep 0.1; `+
		`done; printf "run: completed\n"`)
	delivering := regexp.MustCompile(`Delivery  \d+\.\d s +running`)
	sc.until(func(s string) bool { return delivering.MatchString(s) && strings.Contains(s, "⏸ review") })
	if strings.Contains(sc.shown.String(), "\x1b[?1049l") {
		t.Fatalf("the screen gave the terminal away during Delivery:\n%q", sc.shown.String())
	}
	sc.gateEnds(nm)
	if s := ansi.Strip(sc.shown.String()); !strings.Contains(s, "✓ Delivery") {
		t.Fatalf("Delivery never showed passed:\n%s", s)
	}
	if n := strings.Count(sc.shown.String(), "\x1b[?1049l"); n != 1 {
		t.Fatalf("the screen left the terminal %d times, want once at its close:\n%q", n, sc.shown.String())
	}
}
