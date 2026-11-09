//go:build unix

package run

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// A delivery that holds no-mistakes until its gate ends, as axi run --wait 2h does, writing as it goes. The real
// atm run keeps its screen all along: the pipeline shows the gate's steps below Delivery while Delivery runs, and
// the delivery's own text never takes the terminal.
func TestRealRunKeepsItsScreenWhileDeliveryHoldsTheGate(t *testing.T) {
	nm := gateFake(t)
	bin := builtATM(t)
	held := `printf "run: running\n  review: running\n"; until [ -f "$FAKE_NM/done" ]; do sleep 0.1; done; ` +
		`printf "run: completed\n"`
	root := repo(t, "https://example.com/o/r.git", atmSet(atmYAML, "delivery", held))
	top := gitT(t, root, "rev-parse", "--show-toplevel")
	stopServing(t, top)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	f := startFollow(t, bin, top, true, "run", issueFile(t, issue))
	var shown strings.Builder
	delivering := regexp.MustCompile(`Delivery  \d+\.\d s +running`)
	until := func(ok func(string) bool) {
		t.Helper()
		deadline := time.After(2 * time.Minute)
		for !ok(ansi.Strip(shown.String())) {
			select {
			case l, open := <-f.lines:
				if !open {
					t.Fatalf("atm run ended early; stderr:\n%s\nthe terminal showed:\n%s", f.errors(),
						ansi.Strip(shown.String()))
				}
				shown.WriteString(l + "\n")
			case <-deadline:
				t.Fatalf("the screen never showed it:\n%s", ansi.Strip(shown.String()))
			}
		}
	}
	until(func(s string) bool {
		i := strings.LastIndex(s, "╭─ ATM")
		return i >= 0 && delivering.MatchString(s[i:]) && strings.Contains(s[i:], "⏸ review")
	})
	if strings.Contains(shown.String(), "\x1b[?1049l") {
		t.Fatalf("the screen gave the terminal away during Delivery:\n%q", shown.String())
	}
	if err := os.WriteFile(filepath.Join(nm, "done"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	until(func(s string) bool { return strings.Contains(s, "✓ Delivery") && strings.Contains(s, "q close") })
	if _, err := f.term.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	for l := range f.lines {
		shown.WriteString(l + "\n")
	}
	if err := f.cmd.Wait(); err != nil {
		t.Fatalf("atm run: %v; stderr:\n%s", err, f.errors())
	}
	if n := strings.Count(shown.String(), "\x1b[?1049l"); n != 1 {
		t.Fatalf("the screen left the terminal %d times, want once at its close:\n%q", n, shown.String())
	}
}
