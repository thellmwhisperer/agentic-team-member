//go:build unix

package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// Each test's background process is its own atm serve, which ends cleanly when the test does, so a covered build
// leaves the counters of what it ran.

// heldPIDs is a FIFO and a directory where each process held on it, a shell a worker or a check command starts,
// writes "<its pid> <its parent's pid>" to a file named after its clone, then waits for a line on the FIFO before
// it goes on: hold is the shell line that does it.
type heldPIDs struct{ dir, fifo, hold string }

func holdOnFIFO(t *testing.T) heldPIDs {
	t.Helper()
	dir := t.TempDir()
	h := heldPIDs{dir: filepath.Join(dir, "pids"), fifo: filepath.Join(dir, "go")}
	if err := os.Mkdir(h.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(h.fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STOP_PIDS", h.dir)
	t.Setenv("STOP_GO", h.fifo)
	h.hold = `n=$(basename "$(pwd)"); echo "$$ $PPID" > "$STOP_PIDS/.$n"; mv "$STOP_PIDS/.$n" "$STOP_PIDS/$n"; ` +
		`read _ < "$STOP_GO"`
	t.Cleanup(func() { // a held process left by a failure goes on; one killed leaves no reader, and nothing to write
		if f, err := os.OpenFile(h.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_, _ = f.WriteString("go\ngo\n")
			_ = f.Close()
		}
	})
	return h
}

// held is each held process's pid and its parent's, by the base name of its clone.
func (h heldPIDs) held(t *testing.T) map[string][2]int {
	t.Helper()
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	held := map[string][2]int{}
	for _, e := range entries {
		var pids [2]int
		if b, err := os.ReadFile(filepath.Join(h.dir, e.Name())); err == nil && !strings.HasPrefix(e.Name(), ".") {
			if _, err := fmt.Sscan(string(b), &pids[0], &pids[1]); err == nil {
				held[e.Name()] = pids
			}
		}
	}
	return held
}

// release lets one held process go on.
func (h heldPIDs) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(h.fifo, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// press types key k on sc's terminal and reads it until what it drew since shows want, 2 min at most. The footer
// is the screen's last line, which ends in no newline: a new size every quarter second has the screen draw itself
// again, which ends it.
func (sc *screenRead) press(k, want string) {
	sc.f.t.Helper()
	from := sc.shown.Len()
	if _, err := sc.f.term.Write([]byte(k)); err != nil {
		sc.f.t.Fatal(err)
	}
	deadline, redraw := time.After(2*time.Minute), time.NewTicker(250*time.Millisecond)
	defer redraw.Stop()
	for cols := uint16(100); !strings.Contains(ansi.Strip(sc.shown.String()[from:]), want); {
		select {
		case l, open := <-sc.f.lines:
			if !open {
				sc.f.t.Fatalf("the screen ended before it showed %q:\n%s", want, ansi.Strip(sc.shown.String()[from:]))
			}
			sc.shown.WriteString(l + "\n")
		case <-redraw.C:
			cols = 201 - cols // 100, then 101
			if err := pty.Setsize(sc.f.term, &pty.Winsize{Rows: 36, Cols: cols}); err != nil {
				sc.f.t.Fatal(err)
			}
			_ = sc.f.cmd.Process.Signal(syscall.SIGWINCH)
		case <-deadline:
			sc.f.t.Fatalf("the screen never showed %q after %s:\n%s", want, k, ansi.Strip(sc.shown.String()[from:]))
		}
	}
}

// stopOf is the real atm stop on top and what it printed.
func stopOf(bin, top, label string) (string, error) {
	cmd := exec.Command(bin, "stop", label)
	cmd.Dir = top
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runOf is run label of top as the background process tells it.
func runOf(t *testing.T, top, label string) Outcome {
	t.Helper()
	runs, err := Runs(top)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Run == label {
			return r
		}
	}
	t.Fatalf("no run %s in %+v", label, runs)
	return Outcome{}
}

// stoppedReport checks run label's report.json says it failed at node, stopped by the operator, asked nothing,
// and returns the base name of its clone.
func stoppedReport(t *testing.T, top, label, node string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir(top, label), "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		FailedNode string `json:"failed_node"`
		Reason     string
		Decisions  []Decision
		Nodes      []struct{ Name, Result, Clone string }
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	clone := ""
	for _, n := range r.Nodes {
		if n.Name == "clone" {
			clone = filepath.Base(n.Clone)
		}
		if n.Name == "delivery" && n.Result != "not run" {
			t.Fatalf("a stopped run went on to its delivery: %s", b)
		}
	}
	if r.FailedNode != node || r.Reason != "stopped by operator" || len(r.Decisions) != 0 || clone == "" {
		t.Fatalf("report of %s: want failed at %s, stopped by operator, no decision:\n%s", label, node, b)
	}
	return clone
}

// The real binary: on the screen of atm run, x only warns, any other key cancels it, and x twice kills the run's
// working agent and the shell it started, and ends the run failed, stopped by operator. The other run of the same
// atm serve goes on; on its atm attach, q after the first x leaves the screen and the run goes, then it passes.
func TestStopRunOnTheScreen(t *testing.T) {
	top := atmRepo(t)
	h := holdOnFIFO(t)
	t.Setenv("FAKE_AGENT_WORK", h.hold+"; "+fixWork)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	bin := followUpsServer(t, top) // after the fake's environment, which the background process passes on
	other := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
	waitFor(t, "the first run's worker to start its work", func() bool { return len(h.held(t)) == 1 })
	sc := &screenRead{f: startFollow(t, bin, top, true, "run", issueFile(t, issue))}
	waitFor(t, "the second run's worker to start its work", func() bool { return len(h.held(t)) == 2 })
	const warning = "x again stops the run and kills its agent; any other key cancels"
	sc.until(func(s string) bool { return strings.Contains(s, "x stop") && strings.Contains(s, "Agent") })
	sc.press("x", warning)
	sc.press("i", "x stop") // cancels the warning, and unfolds the title as i does
	// The screen redraws only the lines that change: a new size draws it whole, without the warning.
	sc.resized(120, func(s string) bool { return strings.Contains(s, "x stop") && !strings.Contains(s, warning) })
	if r := runOf(t, top, "issue-2"); r.Outcome != "running" {
		t.Fatalf("a cancelled x stopped the run: %+v", r)
	}
	sc.press("x", warning)
	if _, err := sc.f.term.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if shown := decisionTerminal(t, sc.f, 1); !strings.Contains(shown, "stopped by operator") {
		t.Fatalf("the screen's last view does not say the run was stopped:\n%s", shown)
	}
	if r := runOf(t, top, "issue-2"); r.Outcome != "failed" || r.FailedNode != "agent" ||
		r.Reason != "stopped by operator" {
		t.Fatalf("the stopped run: %+v", r)
	}
	h.killedOnly(t, stoppedReport(t, top, "issue-2", "agent"))
	if r := runOf(t, top, "issue-1"); r.Outcome != "running" {
		t.Fatalf("stopping issue-2 stopped issue-1: %+v", r)
	}
	leaveAfterX(t, bin, top, "issue-1", warning)
	h.release(t)
	other.exit(0)
	if r := runOf(t, top, "issue-1"); r.Outcome != "passed" {
		t.Fatalf("the other run: %+v", r)
	}
}

// killedOnly checks the worker held in clone, the stopped run's, and the shell it started end, and every other
// worker held and its shell still work.
func (h heldPIDs) killedOnly(t *testing.T, clone string) {
	t.Helper()
	held := h.held(t)
	if _, ok := held[clone]; !ok {
		t.Fatalf("no held worker in the stopped run's clone %s: %v", clone, held)
	}
	for in, pids := range held {
		if in == clone {
			until(t, "the stopped run's agent and its shell to end", func() bool { return !alive(pids[0]) && !alive(pids[1]) })
		} else if !alive(pids[0]) || !alive(pids[1]) {
			t.Fatalf("the stop killed another run's agent %d or its shell %d", pids[1], pids[0])
		}
	}
}

// leaveAfterX opens the real atm attach of run label on a terminal, presses x, which warns, then q, which leaves the
// screen with the run going.
func leaveAfterX(t *testing.T, bin, top, label, warning string) {
	t.Helper()
	at := &screenRead{f: startFollow(t, bin, top, true, "attach", label)}
	at.until(func(s string) bool { return strings.Contains(s, "x stop") && strings.Contains(s, "Agent") })
	at.press("x", warning)
	if _, err := at.f.term.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if shown := decisionTerminal(t, at.f, 0); !strings.Contains(shown, "the run goes on: atm attach "+label) {
		t.Fatalf("q after x did not leave the screen with the run going:\n%s", shown)
	}
	if r := runOf(t, top, label); r.Outcome != "running" {
		t.Fatalf("q after x stopped %s: %+v", label, r)
	}
}

// The real binary: atm stop, for scripts, stops a run whose check command works: it kills the command, ends the
// run failed at Checks, stopped by operator, without asking, and returns once it ended. A run that ended and one
// that does not exist each fail it, saying why.
func TestStopRunFromAScript(t *testing.T) {
	h := holdOnFIFO(t)
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "lint", h.hold)),
		"rev-parse", "--show-toplevel")
	bin := followUpsServer(t, top)
	f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
	waitFor(t, "the lint command to start", func() bool { return len(h.held(t)) == 1 })
	if out, err := stopOf(bin, top, "issue-1"); err != nil || out != "stopped issue-1\n" {
		t.Fatalf("atm stop: %v\n%s", err, out)
	}
	if r := runOf(t, top, "issue-1"); r.Outcome != "failed" || r.FailedNode != "checks" ||
		r.Reason != "stopped by operator" || r.Ended.IsZero() {
		t.Fatalf("atm stop returned before the run ended as stopped: %+v", r)
	}
	stopped := stoppedReport(t, top, "issue-1", "checks")
	lint := h.held(t)[stopped]
	until(t, "the lint command to end", func() bool { return !alive(lint[0]) })
	f.exit(1)
	for _, c := range []struct{ run, want string }{
		{"issue-1", "run issue-1 is failed at checks: only a run going before its delivery stops"},
		{"no-such-run", "no run no-such-run"},
	} {
		var exit *exec.ExitError
		if out, err := stopOf(bin, top, c.run); !errors.As(err, &exit) || !strings.Contains(out, c.want) {
			t.Fatalf("atm stop %s: want %q, got %v\n%s", c.run, c.want, err, out)
		}
	}
}

// The real binary: a run that waits for its launcher's answer is not stopped: x twice on its screen says why
// there, as atm stop does, and atm answer still ends it.
func TestStopRunRefusedWhileItWaits(t *testing.T) {
	top := atmRepo(t)
	t.Setenv("FAKE_AGENT", "fail")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	bin := followUpsServer(t, top)
	_, label := question(t, bin, top, "", 1, "run", "--follow", issueFile(t, issue))
	const why = "run issue-1 is waiting at agent: only a run going before its delivery stops"
	sc := &screenRead{f: startFollow(t, bin, top, true, "attach", label)}
	sc.resized(200, func(s string) bool { return strings.Contains(s, "⏸ waiting") })
	sc.press("x", "x again stops the run") // one key at a time, as typed
	sc.press("x", why)
	if out, err := stopOf(bin, top, label); err == nil || !strings.Contains(out, why) {
		t.Fatalf("atm stop on a waiting run: %v\n%s", err, out)
	}
	if err := Answer(top, label, "stop", "test"); err != nil {
		t.Fatal(err)
	}
	decisionTerminal(t, sc.f, 1)
	if end, err := Attach(top, label, io.Discard); err != nil || end.Outcome != "failed" ||
		end.Reason == "stopped by operator" {
		t.Fatalf("the answered run: %+v, %v", end, err)
	}
}
