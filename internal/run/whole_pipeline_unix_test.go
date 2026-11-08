//go:build unix

package run

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/creack/pty"
)

func TestRealScreenShowsTheWholePipelineFromTheStart(t *testing.T) {
	t.Run("delivery configured", func(t *testing.T) {
		nm := gateFake(t)
		writeFixture(t, filepath.Join(nm, "none"), "")
		for _, command := range []string{"status", "logs"} {
			held(t, filepath.Join(nm, "wait-"+command))
		}
		a := liveAttach(t, 120, 50, "echo delivered")
		first := a.sees(func(s string) bool {
			return strings.Contains(s, "○ Delivery") && strings.Contains(s, "q detach")
		})
		assertPendingGatePipeline(t, first)
		a.send(`{"step":"agent","state":"started"}`)
		a.send(`{"step":"agent","says":"working-before-delivery"}`)
		working := a.sees(func(s string) bool { return strings.Contains(s, "working-before-delivery") })
		assertPendingGatePipeline(t, working)
		if _, err := os.Stat(filepath.Join(nm, "log")); !os.IsNotExist(err) {
			t.Fatalf("the screen asked no-mistakes before delivery: %v", err)
		}
		t.Logf("first screen:\n%s\nagent screen:\n%s", first, working)
		if err := os.Remove(filepath.Join(nm, "none")); err != nil {
			t.Fatal(err)
		}
		a.send(`{"step":"agent","state":"passed"}`)
		a.delivered(t.TempDir())
		assertGatePendingWhileAnswering(t, a, nm)
		gate := a.sees(func(s string) bool {
			return regexp.MustCompile(`(?m)^│ ⏸ review  1\.2 s +waiting │`).MatchString(s) &&
				strings.Contains(s, "– lint") && !strings.Contains(s, "○ ci")
		})
		rows := regexp.MustCompile(`(?m)^│ \S+ review\b[^│\n]*│`).FindAllString(gate, -1)
		if len(rows) != 1 || strings.Contains(gate, "○ intent") || strings.Contains(gate, "○ ci") ||
			!regexp.MustCompile(`(?m)^│ ○ test +pending │`).MatchString(gate) ||
			!regexp.MustCompile(`(?m)^│ – lint +skipped │`).MatchString(gate) {
			t.Fatalf("the gate did not replace the pending rows with its own steps and states:\n%s", gate)
		}
		t.Logf("gate screen:\n%s", gate)
		writeFixture(t, filepath.Join(nm, "done"), "")
		a.exits()
	})
	t.Run("no delivery", func(t *testing.T) {
		a := liveAttach(t, 120, 50)
		a.send(`{"step":"agent","state":"started"}`)
		a.send(`{"step":"agent","says":"working-without-delivery"}`)
		s := a.sees(func(s string) bool { return strings.Contains(s, "working-without-delivery") })
		assertNoGatePipeline(t, s)
		a.end()
	})
}

func assertGatePendingWhileAnswering(t *testing.T, a *attached, nm string) {
	t.Helper()
	for _, command := range []string{"status", "logs"} {
		until(t, "the gate's "+command+" request", func() bool {
			log, _ := os.ReadFile(filepath.Join(nm, "log"))
			return strings.Contains(string(log), " axi "+command)
		})
		waiting := a.drawn(func(s string) bool { return strings.Contains(s, "q close") })
		assertPendingGatePipeline(t, waiting)
		t.Logf("screen while %s answers:\n%s", command, waiting)
		path := filepath.Join(nm, "wait-"+command)
		free(t, path)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRealScreenUsesRunDeliveryConfig(t *testing.T) {
	bin := builtATM(t)
	for _, c := range []struct{ name, base, working string }{
		{"base delivery", "echo delivered", ""},
		{"base without delivery", "", "echo delivered"},
	} {
		t.Run(c.name, func(t *testing.T) { screenRunDeliveryConfig(t, bin, c.base, c.working) })
	}
}

func screenRunDeliveryConfig(t *testing.T, bin, base, working string) {
	t.Helper()
	top := gitT(t, repo(t, "https://example.com/o/r.git", atmSet(atmYAML, "delivery", base)),
		"rev-parse", "--show-toplevel")
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	wait, release := holdFakeAgents(t, 1)
	server := startVersionServe(t, bin, top)
	writeConfig(t, top, atmSet(atmYAML, "delivery", working))
	out, err := versionLaunch(bin, top, issueFile(t, issue))
	if err != nil {
		t.Fatalf("atm run: %v\n%s", err, out)
	}
	wait()
	gitT(t, top, "add", ".atm.yaml")
	gitT(t, top, "commit", "-qm", "Change delivery after launch")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	a := &attached{t: t, cmd: exec.Command(bin, "attach"), chunks: make(chan string, 64), rows: 50}
	a.cmd.Dir = top
	if a.p, err = pty.StartWithSize(a.cmd, &pty.Winsize{Cols: 120, Rows: 50}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.cmd.Process.Kill(); _ = a.p.Close() })
	go a.read()
	first := a.sees(func(s string) bool { return strings.Contains(s, "q detach") })
	agent := a.sees(func(s string) bool { return strings.Contains(s, "╭─ Agent") })
	for _, screen := range []string{first, agent} {
		if base != "" {
			assertPendingGatePipeline(t, screen)
		} else {
			assertNoGatePipeline(t, screen)
		}
	}
	t.Logf("first screen:\n%s\nagent screen:\n%s", first, agent)
	a.key("q")
	a.exits()
	release()
	end, err := Attach(top, strings.TrimSpace(string(out)), io.Discard)
	if err != nil || end.Err() != nil {
		t.Fatalf("run ended %+v, %v", end, err)
	}
	endServe(bin, top, server)
}

func assertNoGatePipeline(t *testing.T, screen string) {
	t.Helper()
	if regexp.MustCompile(`(?m)^│ \S+ (intent|rebase|review|test|document|lint|push|pr|ci)\b`).MatchString(screen) {
		t.Fatalf("a run with no delivery shows gate rows:\n%s", screen)
	}
}

func assertPendingGatePipeline(t *testing.T, screen string) {
	t.Helper()
	rows := regexp.MustCompile(`(?m)^│ ○ (intent|rebase|review|test|document|lint|push|pr|ci) +pending │`).
		FindAllStringSubmatch(screen, -1)
	var got []string
	for _, row := range rows {
		got = append(got, row[1])
	}
	delivery := regexp.MustCompile(`(?m)^│ \S+ Delivery\b`).FindStringIndex(screen)
	if strings.Join(got, " ") != "intent rebase review test document lint push pr ci" ||
		delivery == nil || delivery[0] > strings.Index(screen, "○ intent") {
		t.Fatalf("the screen lacks the whole pending gate pipeline below Delivery:\n%s", screen)
	}
}
