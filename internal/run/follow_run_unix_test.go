//go:build unix

package run

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

type followed struct {
	t      *testing.T
	cmd    *exec.Cmd
	lines  chan string
	stderr string
}

// startFollow starts the real binary with stdout on a pipe or a terminal, stdin and stdout.
func startFollow(t *testing.T, bin, top string, tty bool, args ...string) *followed {
	t.Helper()
	f := &followed{t: t, cmd: exec.Command(bin, args...), lines: make(chan string, 1000),
		stderr: filepath.Join(t.TempDir(), "stderr")}
	f.cmd.Dir = top
	errout, err := os.Create(f.stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = errout.Close() }()
	f.cmd.Stderr = errout
	var out *os.File
	if tty {
		p, tt, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tt.Close() }()
		t.Cleanup(func() { _ = p.Close() })
		if err := pty.Setsize(tt, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
			t.Fatal(err)
		}
		f.cmd.Stdin, f.cmd.Stdout, out = tt, tt, p
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

// A real run streams each event before its agent is released and ends with the run.
func TestRunFollowStreamsEventsUntilRunEnds(t *testing.T) {
	bin := builtATM(t)
	for _, c := range []struct {
		name, delivery, lint string
		code                 int
		tty                  bool
	}{
		{"no delivery", "", "", 0, false},
		{"delivery", "echo delivered", "", 0, true},
		{"checks fail", "", "exit 1", 1, false},
		{"delivery fails", "echo rejected; exit 1", "", 4, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := atmSet(atmSet(atmYAML, "delivery", c.delivery), "lint", c.lint)
			top := gitT(t, repo(t, "https://example.com/o/r.git", cfg), "rev-parse", "--show-toplevel")
			stopServing(t, top)
			wait, release := holdFakeAgents(t, 1)
			f := startFollow(t, bin, top, c.tty, "run", issueFile(t, issue), "--follow")
			starts, ends := map[string]int{}, map[string]int{}
			for {
				select {
				case line, open := <-f.lines:
					if !open {
						f.assertEnd(c.code, starts, ends)
						return
					}
					var ev map[string]any
					if err := json.Unmarshal([]byte(line), &ev); err != nil || ev["step"] == nil || ev["ts"] == nil {
						t.Fatalf("invalid event %q: %v", line, err)
					}
					step, _ := ev["step"].(string)
					if ev["check"] != nil {
						continue
					}
					switch ev["state"] {
					case "started":
						starts[step]++
						if step == "agent" {
							wait()
							release()
						}
					case "passed", "failed":
						ends[step]++
					}
				case <-time.After(60 * time.Second):
					t.Fatalf("follow did not finish: %s", f.errors())
				}
			}
		})
	}
}

func (f *followed) assertEnd(code int, starts, ends map[string]int) {
	f.t.Helper()
	_ = f.cmd.Wait()
	if f.cmd.ProcessState.ExitCode() != code {
		f.t.Fatalf("exit = %d: %s", f.cmd.ProcessState.ExitCode(), f.errors())
	}
	for step, n := range starts {
		if ends[step] != n {
			f.t.Fatalf("%s: starts %d, ends %d", step, n, ends[step])
		}
	}
	if starts["agent"] != 1 || starts["checks"] != 1 {
		f.t.Fatalf("missing steps: %v", starts)
	}
	list := exec.Command(f.cmd.Path, "runs")
	list.Dir = f.cmd.Dir
	out, err := list.CombinedOutput()
	if err != nil || !strings.Contains(string(out), filepath.Join(f.cmd.Dir, ".atm", "runs")) {
		f.t.Fatalf("atm runs did not list the completed run's report: %v\n%s", err, out)
	}
}

func TestRunFollowInterruptedLeavesRunGoing(t *testing.T) {
	top, f, release := heldFollow(t)
	if err := f.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	f.exit(1)
	runs, err := Runs(top)
	if err != nil || len(runs) != 1 || runs[0].Outcome != "running" {
		t.Fatalf("interruption stopped the run: %+v, %v", runs, err)
	}
	release()
	if end, err := Attach(top, runs[0].Run, io.Discard); err != nil || end.Err() != nil {
		t.Fatalf("run after interruption: %+v, %v", end, err)
	}
}

func TestRunFollowReportsLaunchFailure(t *testing.T) {
	bin := builtATM(t)
	top := atmRepo(t)
	f := startFollow(t, bin, top, false, "run", "--follow", "--resume", "missing")
	f.exit(2)
	if !strings.Contains(f.errors(), "missing") {
		t.Fatalf("launch error: %s", f.errors())
	}
	if runs, err := RecordedRuns(top); err != nil || len(runs) != 0 {
		t.Fatalf("launched a run: %+v, %v", runs, err)
	}
}

func TestRunFollowReportsLostServer(t *testing.T) {
	top, f, release := heldFollow(t)
	killServeProcesses(t, top)
	release()
	f.exit(2)
	if !strings.Contains(f.errors(), "background process") {
		t.Fatalf("lost server: %s", f.errors())
	}
	// The released fake outlives its killed server: let it end before cleanup removes the clone it works in.
	held, _ := os.ReadDir(os.Getenv("FAKE_AGENT_BARRIER_READY"))
	for _, e := range held {
		pid, _ := strconv.Atoi(e.Name())
		until(t, "the released fake agent to end", func() bool { return !alive(pid) })
	}
}

func TestRunFollowReportsUnwritableOutput(t *testing.T) {
	bin := builtATM(t)
	top := atmRepo(t)
	stopServing(t, top)
	_, release := holdFakeAgents(t, 1)
	path := filepath.Join(t.TempDir(), "read-only-output")
	writeFixture(t, path, "")
	out, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "--follow", issueFile(t, issue))
	var stderr strings.Builder
	cmd.Dir, cmd.Stdout, cmd.Stderr = top, out, &stderr
	if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 2 || !strings.Contains(stderr.String(), "write") {
		t.Fatalf("unwritable output: %v, %s", err, &stderr)
	}
	release()
	if end, err := Attach(top, "issue-1", io.Discard); err != nil || end.Err() != nil {
		t.Fatalf("run after output failure: %+v, %v", end, err)
	}
}

func (f *followed) exit(code int) {
	f.t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, open := <-f.lines:
			if open {
				continue
			}
			_ = f.cmd.Wait()
			if f.cmd.ProcessState.ExitCode() != code {
				f.t.Fatalf("exit = %d, want %d: %s", f.cmd.ProcessState.ExitCode(), code, f.errors())
			}
			return
		case <-timer.C:
			f.t.Fatalf("follow did not exit: %s", f.errors())
		}
	}
}

func heldFollow(t *testing.T) (string, *followed, func()) {
	t.Helper()
	bin := builtATM(t)
	top := atmRepo(t)
	stopServing(t, top)
	wait, release := holdFakeAgents(t, 1)
	f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
	wait()
	return top, f, release
}
