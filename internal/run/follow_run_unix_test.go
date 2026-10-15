//go:build unix

package run

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// followRepo builds atm from this source, then a repository on GitHub with .atm.yaml atm, whose background process
// is that atm's, and whose gh and no-mistakes print the files gh and nm name, failing while one is missing.
func followRepo(t *testing.T, atm string) (bin, top, gh, nm string) {
	t.Helper()
	bin = builtATM(t)
	top = gitT(t, repo(t, "https://github.com/o/r.git", atm), "rev-parse", "--show-toplevel")
	stopServing(t, top)
	fakeBin := t.TempDir()
	gh, nm = filepath.Join(fakeBin, "gh.out"), filepath.Join(fakeBin, "nm.out")
	writeFixture(t, filepath.Join(fakeBin, "gh"), "#!/bin/sh\ncat '"+gh+"'\n", 0o755)
	writeFixture(t, filepath.Join(fakeBin, "no-mistakes"), "#!/bin/sh\ncat '"+nm+"'\n", 0o755)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin, top, gh, nm
}

// builtATM is atm built from this source, before a repository's new home, under the warm build cache.
func builtATM(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "atm")
	buildATM(t, bin, "follow-test")
	return bin
}

// stopServing kills, at the end of the test, the background processes atm started for the repository at top.
func stopServing(t *testing.T, top string) {
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(top, ".atm", "serve.log")); err == nil {
			killServeProcesses(t, top)
		}
	})
}

// followed is atm run --follow, a process of its own: its stdout's lines, read as they come, the stream's
// lines of run o, and its stderr.
type followed struct {
	*streamed
	cmd    *exec.Cmd
	stderr string
}

// startFollow starts bin with args in top, its stdout a pipe or, with tty, a terminal.
func startFollow(t *testing.T, bin, top string, tty bool, args ...string) *followed {
	t.Helper()
	f := &followed{streamed: &streamed{t: t, lines: make(chan string, 100)}, cmd: exec.Command(bin, args...),
		stderr: filepath.Join(t.TempDir(), "stderr")}
	f.cmd.Dir = top
	stderr, err := os.Create(f.stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	f.cmd.Stderr = stderr
	var out *os.File
	if tty {
		p, tt, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tt.Close() }() // the child holds it: its end ends the reads
		t.Cleanup(func() { _ = p.Close() })
		f.cmd.Stdout, out = tt, p
	} else {
		r, err := f.cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		out = r.(*os.File)
	}
	if err := f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.cmd.Process.Kill(); _ = f.cmd.Wait() })
	go func() {
		defer close(f.lines)
		for sc := bufio.NewScanner(out); sc.Scan(); {
			f.lines <- strings.TrimSuffix(sc.Text(), "\r")
		}
	}()
	return f
}

func (f *followed) errors() string {
	b, _ := os.ReadFile(f.stderr)
	return string(b)
}

// runFollow starts atm run with the issue between flags before and after, the first run of the repository at top.
func runFollow(t *testing.T, bin, top string, tty bool, before []string, after ...string) *followed {
	t.Helper()
	path := issueFile(t, issue)
	f := startFollow(t, bin, top, tty, append(append(append([]string{"run"}, before...), path), after...)...)
	f.o = Outcome{Run: "issue-1", Issue: path}
	return f
}

// ended wants atm ended with code, and nothing more on its stdout.
func (f *followed) ended(code int) {
	f.t.Helper()
	for l := range f.lines {
		f.t.Fatalf("stdout line %q after the last", l)
	}
	_ = f.cmd.Wait()
	if got := f.cmd.ProcessState.ExitCode(); got != code {
		f.t.Fatalf("atm exit %d, want %d; stderr:\n%s", got, code, f.errors())
	}
}

func (f *followed) steps(steps ...string) {
	f.t.Helper()
	for _, step := range steps {
		f.next("step", map[string]any{"step": step})
	}
}

// leases is the files of the watcher leases of the repository at top.
func leases(t *testing.T, top string) []string {
	t.Helper()
	paths, err := filepath.Glob(leasePath(top, "*"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

var followSteps = []string{"issue", "clone", "contract", "agent", "checks", "ponytail", "delivery"}

// atm run <issue> --follow, with no watcher alive, launches one run, prints its whole trace as JSON lines, and only
// those, on a pipe, and exits 0 once its PR merges, its lease gone.
func TestRunFollowMergesAfterTheWholeTrace(t *testing.T) {
	parked, completed := recorded(t, "atm_watch_status_parked.txt"), recorded(t, "atm_watch_status_completed.txt")
	bin, top, gh, nm := followRepo(t, atmSet(atmYAML, "delivery", "true"))
	writeFixture(t, nm, parked)
	f := runFollow(t, bin, top, false, nil, "--follow")
	f.steps(followSteps...)
	clone := f.next("delivery", nil)["clone"]
	f.next("gate_waiting", map[string]any{"step": "review", "clone": clone})
	ci := fmt.Sprintf("%040d", 1)
	writeFixture(t, gh, `{"url":"`+recordedPR+`","state":"MERGED","commits":[{"oid":"`+ci+
		`","messageHeadline":"no-mistakes(ci): Fix lint"}]}`)
	writeFixture(t, nm, completed)
	f.next("pr_opened", map[string]any{"url": recordedPR})
	f.next("gate_fixing_ci", map[string]any{"commit": map[string]any{"sha": ci, "subject": "no-mistakes(ci): Fix lint"}})
	f.next("ci_green", map[string]any{"url": recordedPR})
	f.next("merged", map[string]any{"url": recordedPR})
	f.ended(0)
	if runs, err := Runs(top); err != nil || len(runs) != 1 || runs[0].Run != f.o.Run || runs[0].Watcher == "" {
		t.Fatalf("runs = %+v, %v; want %s alone, bound to a watcher", runs, err, f.o.Run)
	}
	if l := leases(t, top); len(l) != 0 {
		t.Fatalf("leases left after the follow ended: %v", l)
	}
}

// A PR closed unmerged ends atm run --follow with closed, last, and 1. The gate cannot be read, as when the run
// removed the clone of a closed PR: gh finds the PR by the delivery's branch.
func TestRunFollowClosedPRExits1(t *testing.T) {
	bin, top, gh, _ := followRepo(t, atmSet(atmYAML, "delivery", "true"))
	writeFixture(t, gh, `{"url":"`+recordedPR+`","state":"CLOSED","commits":[]}`)
	f := runFollow(t, bin, top, false, nil, "--follow")
	f.steps(followSteps...)
	f.next("delivery", nil)
	f.next("pr_opened", map[string]any{"url": recordedPR})
	f.next("closed", map[string]any{"url": recordedPR})
	f.ended(1)
}

// Flags before the issue, --model's value --follow among them, and a terminal for stdout: a run that fails at its
// checks prints only JSON lines there, run_failed last, and exits 1.
func TestRunFollowRunFailureOnATerminalExits1(t *testing.T) {
	bin, top, _, _ := followRepo(t, atmSet(atmYAML, "lint", "exit 1"))
	f := runFollow(t, bin, top, true, []string{"--follow", "--model", "--follow"})
	f.steps("issue", "clone", "contract", "agent", "checks")
	if failed := f.next("run_failed", map[string]any{"step": "checks"}); failed["reason"] == "" {
		t.Fatalf("run_failed without a reason: %v", failed)
	}
	f.ended(1)
}

// Without --follow, a value-taking flag's value --follow is that flag's: atm run prints the run's label, as ever.
func TestRunFollowAsAFlagValueLeavesRunAsItWas(t *testing.T) {
	bin := builtATM(t)
	top, _ := liveLease(t)
	stopServing(t, top)
	cmd := exec.Command(bin, "run", "--model", "--follow", issueFile(t, issue), "--watcher", "live")
	cmd.Dir = top
	out, err := cmd.Output()
	if err != nil || string(out) != "issue-1\n" {
		t.Fatalf("atm run --model --follow: %q, %v", out, err)
	}
	if runs, err := Runs(top); err != nil || len(runs) != 1 || runs[0].Watcher != "live" {
		t.Fatalf("runs = %+v, %v; want one bound to live", runs, err)
	}
}

// The follow process holds the run's lease. Killed while the worker waits, it leaves the run going: the run says
// WATCHER GONE, atm watch --adopt takes it over, and it finishes.
func TestRunFollowKilledLeavesTheRunGoing(t *testing.T) {
	bin, top, _, _ := followRepo(t, atmYAML)
	_, release := holdFakeAgents(t, 1)
	f := runFollow(t, bin, top, false, nil, "--follow")
	f.steps("issue", "clone", "contract", "agent")
	f.holdsTheLease(top)
	gone := make(chan string, 1)
	go func() {
		_, _ = Follow(top, f.o.Run, nil, nil, func(fr Frame) {
			if fr.Line != nil && strings.Contains(*fr.Line, "WATCHER GONE") {
				select {
				case gone <- *fr.Line:
				default:
				}
			}
		})
	}()
	if err := f.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = f.cmd.Wait()
	select {
	case line := <-gone:
		assertWatcherGoneLine(t, line)
	case <-time.After(60 * time.Second):
		t.Fatal("no WATCHER GONE on the run's screen after the follow process died")
	}
	adopter := startFollow(t, bin, top, false, "watch", "--adopt", f.o.Run)
	for _, want := range []string{"watching as ", f.o.Run + " adopted: running at agent"} {
		select {
		case line := <-adopter.lines:
			if !strings.Contains(line, want) {
				t.Fatalf("atm watch --adopt printed %q, want %q", line, want)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("atm watch --adopt did not print %q; stderr:\n%s", want, adopter.errors())
		}
	}
	release()
	if end, err := Attach(top, f.o.Run, new(strings.Builder)); err != nil || end.Outcome != "passed" {
		t.Fatalf("the run after its follow process died: %+v, %v", end, err)
	}
}

// holdsTheLease wants the one lease of the repository at top held by the follow process, and its run, alone and
// running, bound to it.
func (f *followed) holdsTheLease(top string) {
	f.t.Helper()
	l := leases(f.t, top)
	if len(l) != 1 {
		f.t.Fatalf("leases %v, want the follow process's", l)
	}
	var held lease
	b, err := os.ReadFile(l[0])
	if err != nil || json.Unmarshal(b, &held) != nil || held.PID != f.cmd.Process.Pid {
		f.t.Fatalf("lease %s: %s, %v; want pid %d", l[0], b, err, f.cmd.Process.Pid)
	}
	id := strings.TrimSuffix(filepath.Base(l[0]), ".json")
	if runs, err := Runs(top); err != nil || len(runs) != 1 || runs[0].Run != f.o.Run || runs[0].Watcher != id ||
		runs[0].Outcome != "running" {
		f.t.Fatalf("runs = %+v, %v; want %s alone, running, bound to %s", runs, err, f.o.Run, id)
	}
}

// --follow with --watcher or --resume, or a lease that cannot be written, starts no run and prints nothing on
// stdout: why goes to stderr.
func TestRunFollowRejectsBeforeLaunch(t *testing.T) {
	bin := builtATM(t)
	top, _ := liveLease(t)
	stopServing(t, top)
	path := issueFile(t, issue)
	for _, c := range []struct {
		args []string
		why  string
	}{
		{[]string{"run", path, "--follow", "--watcher", "live"}, "--watcher"},
		{[]string{"run", "--follow", "--resume", "issue-1"}, "atm watch --adopt"},
		{[]string{"run", "--follow", path}, filepath.Join(".atm", "watch")},
	} {
		if c.why == filepath.Join(".atm", "watch") { // a file where the leases go
			if err := os.RemoveAll(filepath.Join(top, ".atm", "watch")); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(top, ".atm", "watch"), "")
		}
		cmd := exec.Command(bin, c.args...)
		cmd.Dir = top
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil || len(out) != 0 || !strings.Contains(stderr.String(), c.why) {
			t.Fatalf("atm %v: %v, stdout %q, stderr %q; want an error naming %s", c.args, err, out, stderr.String(), c.why)
		}
		for _, p := range []string{"runs", "atm.sock"} {
			if _, err := os.Stat(filepath.Join(top, ".atm", p)); !os.IsNotExist(err) {
				t.Fatalf("atm %v: .atm/%s exists: %v", c.args, p, err)
			}
		}
	}
}
