//go:build unix

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// watchPatienceForTest is the patience the test's atm is built with, in place of 5 minutes: long enough for a
// fake's first call while the whole suite loads the machine.
const watchPatienceForTest = 10 * time.Second

// watchNM answers like no-mistakes axi status: $FAKE/status-<n> on its nth call, else $FAKE/status, else it fails.
// With the fifo $FAKE/hang it hangs until killed. Each call goes to $FAKE/calls with the directory it ran in.
const watchNM = `#!/bin/sh
echo "$(pwd -P) no-mistakes $*" >>"$FAKE/calls"
n=$(($(cat "$FAKE/n" 2>/dev/null) + 1))
echo "$n" >"$FAKE/n"
[ ! -p "$FAKE/hang" ] || exec cat "$FAKE/hang"
[ ! -f "$FAKE/status-$n" ] || exec cat "$FAKE/status-$n"
[ -f "$FAKE/status" ] || { echo "no-mistakes is down" >&2; exit 3; }
cat "$FAKE/status"
`

// watchGH answers gh pr view with $FAKE/pr, or fails without it.
const watchGH = `#!/bin/sh
echo "$(pwd -P) gh $*" >>"$FAKE/calls"
[ -f "$FAKE/pr" ] || { echo "HTTP 502: Bad Gateway" >&2; exit 1; }
cat "$FAKE/pr"
`

const gateID = "01M5D0SWRMZ44NM79H2NNC3YZ8"

// gateStatus is no-mistakes' status of gate run gateID: its status, its steps as step,status rows, and its active
// steps as step,last_activity rows.
func gateStatus(status string, steps []string, active ...string) string {
	s := "run:\n  id: \"" + gateID + "\"\n  branch: atm/the-unit\n  status: " + status + "\n" +
		"  steps[" + strconv.Itoa(len(steps)) + "]{step,status,findings,duration_ms}:\n"
	for _, step := range steps {
		s += "    " + step + ",0,0\n"
	}
	if len(active) > 0 {
		s += "  active_steps[" + strconv.Itoa(len(active)) +
			"]{step,status,active_for,round_active_for,last_activity,agent_pid,round}:\n"
		for _, a := range active {
			step, activity, _ := strings.Cut(a, ",")
			s += "    " + step + ",running,32m10s,32m10s," + strconv.Quote(activity) + ",\"\",starting\n"
		}
	}
	return s
}

const allPassed = "3m54s ago: log: all CI checks passed - still monitoring until merged or closed"

// watchRepo is a repository with recorded runs, and the fake no-mistakes and gh the built atm finds on PATH.
type watchRepo struct {
	t                     *testing.T
	bin, root, fake, path string
	runs                  []string
}

func newWatchRepo(t *testing.T, bin string) *watchRepo {
	t.Helper()
	w := &watchRepo{t: t, bin: bin, root: t.TempDir(), fake: t.TempDir()}
	tools := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", w.root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	w.write(filepath.Join(tools, "no-mistakes"), watchNM)
	w.write(filepath.Join(tools, "gh"), watchGH)
	if err := os.Chmod(filepath.Join(tools, "no-mistakes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(tools, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.path = tools + string(os.PathListSeparator) + os.Getenv("PATH")
	return w
}

func (w *watchRepo) write(path, content string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// recorded records run label, its outcome, and a report whose delivery has result delivery; it returns the run's
// directory. A run whose delivery passed or failed gets a delivery-output.txt naming gateID.
func (w *watchRepo) recorded(label, outcome, delivery string) string {
	w.t.Helper()
	dir := filepath.Join(w.root, ".atm", "runs", label)
	clone := w.clone(label)
	if err := os.MkdirAll(clone, 0o755); err != nil {
		w.t.Fatal(err)
	}
	report, _ := json.Marshal(map[string]any{"nodes": []map[string]string{
		{"name": "clone", "result": "passed", "clone": clone}, {"name": "delivery", "result": delivery}}})
	w.write(filepath.Join(dir, "report.json"), string(report))
	if delivery == "passed" || delivery == "failed" {
		w.write(filepath.Join(dir, "delivery-output.txt"),
			"no-mistakes axi run\nrun:\n  id: \""+gateID+"\"\n  status: running\n")
	}
	o := map[string]any{"outcome": outcome, "run": label, "issue": "7", "report": filepath.Join(dir, "report.json"),
		"started": time.Now().Add(-time.Hour)}
	if outcome != "running" {
		o["ended"] = time.Now()
	}
	line, _ := json.Marshal(o)
	w.runs = append(w.runs, string(line))
	w.write(filepath.Join(w.root, ".atm", "runs.jsonl"), strings.Join(w.runs, "\n")+"\n")
	return dir
}

func (w *watchRepo) clone(label string) string { return filepath.Join(w.root, ".atm", "clones", label) }

// fakeFile writes the fake's file name.
func (w *watchRepo) fakeFile(name, content string) { w.write(filepath.Join(w.fake, name), content) }

// watched is the built atm axi watch args in the repository: its stdout, stderr and exit code.
func (w *watchRepo) watched(args ...string) (string, string, int) {
	w.t.Helper()
	cmd := exec.Command(w.bin, append([]string{"axi", "watch"}, args...)...)
	cmd.Dir = w.root
	cmd.Env = append(os.Environ(), "PATH="+w.path, "FAKE="+w.fake, "HOME="+w.fake)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		w.t.Fatal(err)
	}
	return stdout.String(), stderr.String(), cmd.ProcessState.ExitCode()
}

// calls is every no-mistakes and gh call the fakes saw, a line each: the directory it ran in, then the call.
func (w *watchRepo) calls() string {
	b, _ := os.ReadFile(filepath.Join(w.fake, "calls"))
	return string(b)
}

// tree is every file and directory under the repository, with its content's hash and its modification time.
func (w *watchRepo) tree() map[string]string {
	w.t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files[path] = info.ModTime().String() + " " + info.Mode().String()
		if !d.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] += fmt.Sprintf(" %x", sha256.Sum256(b))
		}
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return files
}

// expect runs atm axi watch args, checks its output, exit code and that it wrote nothing, and returns its stderr.
func (w *watchRepo) expect(want string, code int, args ...string) string {
	w.t.Helper()
	before := w.tree()
	out, errOut, exit := w.watched(args...)
	if out != want || exit != code {
		w.t.Fatalf("atm axi watch %v: exit %d, stdout %q, stderr %q; want exit %d, stdout %q\ncalls:\n%s",
			args, exit, out, errOut, code, want, w.calls())
	}
	after := w.tree()
	for path, was := range before {
		if after[path] != was {
			w.t.Fatalf("atm axi watch changed %s", path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			w.t.Fatalf("atm axi watch wrote %s", path)
		}
	}
	return errOut
}

// unknown checks that watch prints no event and, after its patience, exits 1 with an error that has want.
func (w *watchRepo) unknown(want string, args ...string) {
	w.t.Helper()
	start := time.Now()
	errOut := w.expect("", 1, args...)
	if !strings.Contains(errOut, want) {
		w.t.Fatalf("atm axi watch's error %q does not say %q", errOut, want)
	}
	if took := time.Since(start); took < watchPatienceForTest {
		w.t.Fatalf("atm axi watch gave up after %s, before its patience", took)
	}
}

// buildATM builds atm, with ldflags, for end-to-end tests that count toward coverage when GOCOVERDIR is set.
func buildATM(t *testing.T, ldflags ...string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "atm")
	args := []string{"build", "-o", bin, "-ldflags", strings.Join(ldflags, " ")}
	if os.Getenv("GOCOVERDIR") != "" {
		args = append(args, "-cover", "-coverpkg=./...")
	}
	build := exec.Command("go", append(args, "./cmd/atm")...)
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build atm: %v\n%s", err, out)
	}
	return bin
}

// TestWatchGate drives the built atm axi watch against recorded runs and the gate statuses a fake no-mistakes
// and a fake gh show: each event, what watch cannot read, --after, and the run it watches.
func TestWatchGate(t *testing.T) {
	pkg := "github.com/thellmwhisperer/agentic-team-member/internal/cli."
	// watch's patience cut to watchPatienceForTest, its reads 20 ms apart
	bin := buildATM(t, "-X", pkg+"watchEvery=20ms", "-X", pkg+"watchPatience="+watchPatienceForTest.String())
	run := func(name string, f func(t *testing.T, w *watchRepo)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f(t, newWatchRepo(t, bin))
		})
	}
	watchGateEvents(run)
	watchRuns(run)
	watchLookup(run)
	watchUnknown(run)
	watchAfter(run)
	t.Run("atm skill names atm axi watch", func(t *testing.T) {
		out, err := exec.Command(bin, "skill").Output()
		if err != nil || !strings.Contains(string(out), "atm axi watch") {
			t.Fatalf("atm skill: %v\n%s", err, out)
		}
	})
}

// watchSubtest runs f as a parallel subtest name, on a new watchRepo.
type watchSubtest func(name string, f func(t *testing.T, w *watchRepo))

// pr is a gate whose pull request is open and whose ci step runs.
var pr = []string{"review,completed", "push,completed", "pr,completed", "ci,running"}

// watchGateEvents: each event, from the gate a passed delivery started or from the run itself.
func watchGateEvents(run watchSubtest) {
	for _, c := range []struct {
		name, status, pr, want string
	}{
		{"a step waits for approval", gateStatus("running", []string{"review,awaiting_approval", "ci,pending"}), "",
			"gate_question: review\n"},
		{"a step's fixes wait for review", gateStatus("running", []string{"review,fix_review"}), "",
			"gate_question: review\n"},
		{"a step failed", gateStatus("running", []string{"review,completed", "test,failed"}), "",
			"gate_failed: test\n"},
		{"ci runs", gateStatus("running", pr, `ci,1m ago: log: waiting for "build", 3 checks`), `{"state":"OPEN"}`,
			"ci_running: ci\n"},
		{"ci is green and the pull request waits", gateStatus("running", pr, "ci,"+allPassed), `{"state":"OPEN"}`,
			"ci_green_awaiting_merge: ci\n"},
		{"the pull request merged", gateStatus("running", pr, "ci,"+allPassed), `{"state":"MERGED"}`,
			"done: merged\n"},
		{"the pull request closed", gateStatus("running", pr), `{"state":"CLOSED"}`, "done: closed\n"},
		{"the gate completed, its pull request unread", gateStatus("completed", pr[:3]), "", "done: passed\n"},
		{"the gate failed", gateStatus("failed", []string{"review,failed"}), "", "done: failed\n"},
		{"the gate was cancelled", gateStatus("cancelled", []string{"review,running"}), "", "done: failed\n"},
	} {
		run(c.name, func(t *testing.T, w *watchRepo) {
			dir := w.recorded("7-1", "passed", "passed")
			w.fakeFile("status", c.status)
			if c.pr != "" {
				w.fakeFile("pr", c.pr)
			}
			w.expect(c.want, 0, "7-1")
			real, _ := filepath.EvalSymlinks(dir)
			if !strings.HasPrefix(w.calls(), real+" no-mistakes axi status --run "+gateID+"\n") {
				t.Fatalf("watch did not ask the gate attach asks, from the run's directory:\n%s", w.calls())
			}
		})
	}
	run("ci green, read with gh in the same pass", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.fakeFile("status", gateStatus("running", pr, "ci,"+allPassed))
		w.fakeFile("pr", `{"state":"OPEN"}`)
		w.expect(`{"event":"ci_green_awaiting_merge","run":"7-1","step":"ci","outcome":""}`+"\n", 0, "--json", "7-1")
		root, _ := filepath.EvalSymlinks(w.root)
		want := " no-mistakes axi status --run " + gateID + "\n" + root + " gh pr view atm/the-unit --json state\n"
		if calls := w.calls(); !strings.HasSuffix(calls, want) || strings.Count(calls, "\n") != 2 {
			t.Fatalf("calls:\n%s\nwant one status and its gh read:\n%s", calls, want)
		}
	})
	run("the run failed", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "failed", "not run")
		w.expect(`{"event":"done","run":"7-1","step":"","outcome":"failed"}`+"\n", 0, "--json", "7-1")
		if w.calls() != "" {
			t.Fatalf("a failed run asked the gate:\n%s", w.calls())
		}
	})
	run("the run passed with no delivery", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "skipped")
		w.expect("done: passed\n", 0, "7-1")
	})
}

// watchRuns: the gate a delivery that goes on shows, and a run watch waits on
func watchRuns(run watchSubtest) {
	run("a delivery that goes on is asked in the run's clone, for its branch", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "running", "running")
		w.fakeFile("status", gateStatus("running", []string{"review,awaiting_approval"}))
		w.expect("gate_question: review\n", 0, "7-1")
		clone, _ := filepath.EvalSymlinks(w.clone("7-1"))
		if w.calls() != clone+" no-mistakes axi status\n" {
			t.Fatalf("calls:\n%s", w.calls())
		}
	})
	run("blocks until the next event", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "running", "not run")
		w.recorded("8-1", "running", "running")
		w.recorded("9-1", "passed", "skipped")
		w.fakeFile("status-1", gateStatus("running", []string{"review,running"}))
		w.fakeFile("status-2", "current_branch: atm/the-unit\nruns_on_current_branch: 0\n")
		w.fakeFile("status", gateStatus("running", []string{"review,awaiting_approval"}))
		w.expect(`{"event":"gate_question","run":"8-1","step":"review","outcome":""}`+"\n", 0, "--json")
		clone, _ := filepath.EvalSymlinks(w.clone("8-1"))
		if calls := w.calls(); strings.Count(calls, clone+" no-mistakes axi status\n") != 3 {
			t.Fatalf("watch of the newest running run did not wait through two reads without an event:\n%s", calls)
		}
	})
	run("a run before its delivery has no event, and keeps watch on", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "running", "not run")
		ctx, cancel := context.WithTimeout(context.Background(), watchPatienceForTest+2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, w.bin, "axi", "watch", "7-1")
		cmd.Dir, cmd.Env = w.root, append(os.Environ(), "PATH="+w.path, "FAKE="+w.fake)
		out, err := cmd.Output()
		if ctx.Err() == nil || len(out) > 0 {
			t.Fatalf("watch of a run in its agent ended before its patience passed twice: %v %q", err, out)
		}
		if w.calls() != "" {
			t.Fatalf("a run before its delivery asked the gate:\n%s", w.calls())
		}
	})
	run("ci green with its pull request unread has no event", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.fakeFile("status-1", gateStatus("running", []string{"pr,running", "ci,running"}, "ci,"+allPassed))
		w.fakeFile("status", gateStatus("completed", []string{"pr,completed", "ci,completed"}))
		w.expect("done: passed\n", 0, "7-1")
		if calls := w.calls(); strings.Count(calls, "\n") != 2 || strings.Contains(calls, " gh ") {
			t.Fatalf("calls:\n%s", calls)
		}
	})
}

// watchLookup: the run watch watches, and its command line
func watchLookup(run watchSubtest) {
	run("unreadable recorded runs", func(t *testing.T, w *watchRepo) {
		if err := os.MkdirAll(filepath.Join(w.root, ".atm", "runs.jsonl"), 0o755); err != nil {
			t.Fatal(err)
		}
		if errOut := w.expect("", 1); !strings.Contains(errOut, "is a directory") {
			t.Fatalf("stderr %q", errOut)
		}
	})
	run("outside a repository", func(t *testing.T, w *watchRepo) {
		dir := t.TempDir()
		cmd := exec.Command(w.bin, "axi", "watch")
		cmd.Dir, cmd.Env = dir, append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
		out, err := cmd.CombinedOutput()
		if !strings.Contains(string(out), "not in a git repository") || cmd.ProcessState.ExitCode() != 2 {
			t.Fatalf("%v: %s", err, out)
		}
	})
	run("nothing runs", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "skipped")
		if errOut := w.expect("", 1); errOut != "atm: nothing runs\n" {
			t.Fatalf("stderr %q", errOut)
		}
		if errOut := w.expect("", 1, "6-1"); errOut != "atm: no run 6-1\n" {
			t.Fatalf("stderr %q", errOut)
		}
	})
	run("no recorded run", func(t *testing.T, w *watchRepo) {
		if errOut := w.expect("", 1); errOut != "atm: nothing runs\n" {
			t.Fatalf("stderr %q", errOut)
		}
	})
	run("its only flags are --json and --after", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "skipped")
		for _, args := range [][]string{{"--follow", "7-1"}, {"--after", "merged", "7-1"}, {"7-1", "8-1"}} {
			if _, errOut, code := w.watched(args...); code != 2 || errOut == "" {
				t.Fatalf("atm axi watch %v: exit %d, %q", args, code, errOut)
			}
		}
		help, _, _ := w.watched("--help")
		_, flags, _ := strings.Cut(help, "Flags:\n")
		if !strings.Contains(flags, "--after") || !strings.Contains(flags, "--json") || strings.Count(flags, "--") != 3 {
			t.Fatalf("watch's flags are not --after, --json and --help:\n%s", help)
		}
	})
}

// watchUnknown: what watch cannot read prints no event, until watch gives up
func watchUnknown(run watchSubtest) {
	run("an unreadable report is unknown", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.write(filepath.Join(w.root, ".atm", "runs", "7-1", "report.json"), "not json")
		w.unknown("report of 7-1: invalid character", "7-1")
	})
	run("a passed delivery without its output is unknown", func(t *testing.T, w *watchRepo) {
		dir := w.recorded("7-1", "passed", "passed")
		if err := os.Remove(filepath.Join(dir, "delivery-output.txt")); err != nil {
			t.Fatal(err)
		}
		w.unknown("delivery-output.txt: no such file", "7-1")
	})
	run("a delivery output that names no gate run is unknown", func(t *testing.T, w *watchRepo) {
		dir := w.recorded("7-1", "passed", "passed")
		w.write(filepath.Join(dir, "delivery-output.txt"), "error: repo not initialized\n")
		w.unknown("delivery-output.txt names no gate run", "7-1")
	})
	run("a gate status that fails is unknown", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.unknown("no-mistakes axi status --run "+gateID+": exit status 3: no-mistakes is down", "7-1")
	})
	run("a gate status with no run status is unknown", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.fakeFile("status", "current_branch: atm/the-unit\nruns_on_current_branch: 0\n")
		w.unknown("shows no gate run", "7-1")
	})
	run("a gh call that fails is unknown", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.fakeFile("status", gateStatus("running", pr, "ci,"+allPassed))
		w.unknown("pull request: gh pr view atm/the-unit --json state: exit status 1: HTTP 502", "7-1")
	})
	run("a call that hangs is killed at its patience", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		if err := syscall.Mkfifo(filepath.Join(w.fake, "hang"), 0o600); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		w.unknown("signal: killed", "7-1")
		if took := time.Since(start); took > watchPatienceForTest+5*time.Second {
			t.Fatalf("the hung call was not killed at watch's patience: %s", took)
		}
	})
	run("a good read after a failed one", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.fakeFile("status", gateStatus("running", []string{"review,awaiting_approval"}))
		w.fakeFile("status-1", "")
		w.expect("gate_question: review\n", 0, "7-1")
	})
}

// watchAfter: --after skips only its event, and only while the state is the one first read
func watchAfter(run watchSubtest) {
	run("--after skips its event while the state is the first read", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		waiting := gateStatus("running", []string{"review,awaiting_approval"})
		w.fakeFile("status-1", waiting)
		w.fakeFile("status-2", waiting)
		w.fakeFile("status-3", gateStatus("running", []string{"review,fixing"}))
		w.fakeFile("status", waiting)
		w.expect("gate_question: review\n", 0, "--after", "gate_question", "7-1")
		if n := strings.Count(w.calls(), "\n"); n != 4 {
			t.Fatalf("watch printed the question after %d reads, want after the fix that changed the state:\n%s",
				n, w.calls())
		}
	})
	run("--after prints every other event", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.fakeFile("status", gateStatus("running", []string{"review,awaiting_approval"}))
		w.expect("gate_question: review\n", 0, "--after", "ci_running", "7-1")
		if n := strings.Count(w.calls(), "\n"); n != 1 {
			t.Fatalf("watch skipped an event --after does not name:\n%s", w.calls())
		}
	})
}
