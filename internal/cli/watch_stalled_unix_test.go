//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stallFor is the --stall the tests watch with, and movingCalls how many gate reads a run moves for: 20 ms apart
// at least, so more than stallFor.
const (
	stallFor    = time.Second
	movingCalls = 75
)

// stallNM answers like no-mistakes axi status: what $FAKE/each, sourced with the call's number n, prints, else
// $FAKE/status-<n>, else $FAKE/status. Each call goes to $FAKE/calls.
const stallNM = `#!/bin/sh
echo "$(pwd -P) no-mistakes $*" >>"$FAKE/calls"
n=$(($(cat "$FAKE/n" 2>/dev/null) + 1))
echo "$n" >"$FAKE/n"
[ ! -f "$FAKE/each" ] || . "$FAKE/each"
[ ! -f "$FAKE/status-$n" ] || exec cat "$FAKE/status-$n"
cat "$FAKE/status"
`

var stalledLine = regexp.MustCompile(`^stalled: (\S+) for (\S+)\n$`)

// newStallRepo is a watchRepo whose no-mistakes is stallNM.
func newStallRepo(t *testing.T, bin string) *watchRepo {
	t.Helper()
	w := newWatchRepo(t, bin)
	tools := t.TempDir()
	w.write(filepath.Join(tools, "no-mistakes"), stallNM)
	if err := os.Chmod(filepath.Join(tools, "no-mistakes"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.path = tools + string(os.PathListSeparator) + w.path
	return w
}

// step records step as the step of every recorded run, and returns runs.jsonl.
func (w *watchRepo) step(step string) string {
	w.t.Helper()
	for i, line := range w.runs {
		o := map[string]any{}
		if err := json.Unmarshal([]byte(line), &o); err != nil {
			w.t.Fatal(err)
		}
		o["step"] = step
		b, _ := json.Marshal(o)
		w.runs[i] = string(b)
	}
	runs := strings.Join(w.runs, "\n") + "\n"
	w.write(filepath.Join(w.root, ".atm", "runs.jsonl"), runs)
	return runs
}

// moving has the gate's first movingCalls reads run body, and every later one show a completed gate.
func (w *watchRepo) moving(body string) {
	w.fakeFile("each", `if [ "$n" -le `+strconv.Itoa(movingCalls)+` ]; then `+body+`; fi`+"\n")
	w.fakeFile("status", gateStatus("completed", []string{"review,completed"}))
}

// nmCalls is how many times watch asked no-mistakes.
func (w *watchRepo) nmCalls() int { return strings.Count(w.calls(), " no-mistakes ") }

// stalled runs atm axi watch args, which must print stalled at step after stallFor at least, write nothing and
// exit 0; it returns how long watch said nothing moved.
func (w *watchRepo) stalled(step string, args ...string) time.Duration {
	w.t.Helper()
	before, start := w.tree(), time.Now()
	out, errOut, code := w.watched(args...)
	took := time.Since(start)
	m := stalledLine.FindStringSubmatch(out)
	if code != 0 || m == nil || m[1] != step {
		w.t.Fatalf("atm axi watch %v: exit %d, stdout %q, stderr %q; want stalled: %s\ncalls:\n%s",
			args, code, out, errOut, step, w.calls())
	}
	lasted, err := time.ParseDuration(m[2])
	if err != nil || lasted < stallFor || took < stallFor {
		w.t.Fatalf("watch said stalled for %q after %s, before --stall %s: %v", m[2], took, stallFor, err)
	}
	after := w.tree()
	for path, was := range before {
		if after[path] != was {
			w.t.Fatalf("atm axi watch changed %s", path)
		}
	}
	if len(after) != len(before) {
		w.t.Fatal("atm axi watch wrote in the repository")
	}
	return lasted
}

// TestWatchStalled drives the built atm axi watch against recorded runs that stop moving, or move: their step,
// their agent's streams in the run's folder, and the gate's steps, status and last activity.
func TestWatchStalled(t *testing.T) {
	pkg := "github.com/thellmwhisperer/agentic-team-member/internal/cli."
	bin := buildATM(t, "-X", pkg+"watchEvery=20ms", "-X", pkg+"watchPatience="+watchPatienceForTest.String())
	run := func(name string, f func(t *testing.T, w *watchRepo)) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f(t, newStallRepo(t, bin))
		})
	}
	watchStill(run)
	watchMoving(run)
	watchAfterStalled(run)
}

// watchStill: a run nothing of which moves is stalled at its step
func watchStill(run watchSubtest) {
	run("an agent that writes nothing is stalled at its step", func(t *testing.T, w *watchRepo) {
		dir := w.recorded("7-1", "running", "not run")
		w.step("agent")
		w.write(filepath.Join(dir, "worker.jsonl"), `{"type":"system"}`+"\n")
		w.stalled("agent", "--stall", stallFor.String(), "7-1")
		if w.calls() != "" {
			t.Fatalf("a run before its delivery asked the gate:\n%s", w.calls())
		}
	})
	run("a check that runs longer than --stall is stalled at checks", func(t *testing.T, w *watchRepo) {
		dir := w.recorded("7-1", "running", "not run")
		w.step("checks")
		w.write(filepath.Join(dir, "worker.jsonl"), `{"type":"result"}`+"\n")
		w.write(filepath.Join(dir, "worker.stderr.txt"), "")
		w.stalled("checks", "--stall", stallFor.String())
	})
	watchStillGate(run)
}

// watchStillGate: stalled as JSON, a gate that stops moving, and --stall
func watchStillGate(run watchSubtest) {
	run("stalled as JSON", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "running", "not run")
		w.step("agent")
		out, errOut, code := w.watched("--json", "--stall", stallFor.String(), "7-1")
		var got map[string]string
		if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 {
			t.Fatalf("exit %d, stdout %q, stderr %q: %v", code, out, errOut, err)
		}
		lasted, err := time.ParseDuration(got["for"])
		if got["event"] != "stalled" || got["run"] != "7-1" || got["step"] != "agent" || got["outcome"] != "" ||
			err != nil || lasted < stallFor || len(got) != 5 {
			t.Fatalf("stalled as JSON: %s", out)
		}
	})
	run("a gate whose step runs with no activity is stalled at that step", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.step("delivery")
		w.fakeFile("status", gateStatus("running", []string{"review,completed", "test,running", "ci,pending"},
			"test,2m ago: log: running go test"))
		w.stalled("test", "--stall", stallFor.String(), "7-1")
	})
	run("a last activity that only grows older is no move", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "running", "running")
		w.step("delivery")
		w.fakeFile("aged", gateStatus("running", []string{"review,running"}, "review,AGEs ago: log: thinking"))
		w.fakeFile("each", `sed "s/AGE/$n/" "$FAKE/aged"; exit 0`+"\n")
		w.stalled("review", "--stall", stallFor.String(), "7-1")
	})
	run("--stall is 60 minutes by default, and a second at least", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "running", "not run")
		help, _, _ := w.watched("--help")
		if !strings.Contains(help, "--stall duration") || !strings.Contains(help, "(default 1h0m0s)") {
			t.Fatalf("watch's help does not show --stall at 60 minutes:\n%s", help)
		}
		for _, stall := range []string{"500ms", "0s", "-1m", "soon"} {
			if _, errOut, code := w.watched("--stall", stall, "7-1"); code != 2 || !strings.Contains(errOut, "--stall") {
				t.Fatalf("--stall %s: exit %d, %q", stall, code, errOut)
			}
		}
	})
}

// watchMoving: a run that moves for longer than --stall is never stalled
func watchMoving(run watchSubtest) {
	running := gateStatus("running", []string{"review,running"}, "review,1s ago: log: thinking")
	for _, c := range []struct{ name, body string }{
		{"the agent's stream grows", `printf x >>"$RUN/worker.jsonl"; cat "$FAKE/running"; exit 0`},
		{"the agent's stderr grows", `printf x >>"$RUN/worker.stderr.txt"; cat "$FAKE/running"; exit 0`},
		{"a new agent stream starts", `: >"$RUN/worker-fix-$n.jsonl"; cat "$FAKE/running"; exit 0`},
		{"the run's step changes", `cp "$FAKE/runs-$((n % 2))" "$FAKE/runs.tmp"; mv "$FAKE/runs.tmp" "$RUNS"; ` +
			`cat "$FAKE/running"; exit 0`},
		{"the gate's step changes", `cat "$FAKE/running-$((n % 2))"; exit 0`},
		{"a gate step's status changes", `cat "$FAKE/fixing-$((n % 2))"; exit 0`},
		{"the gate's status changes", `cat "$FAKE/gate-$((n % 2))"; exit 0`},
		{"a gate step's last activity changes", `sed "s/WHAT/$n/" "$FAKE/activity"; exit 0`},
		{"a gate step's last activity is new again", `sed "s/AGE/$((n % 5))/" "$FAKE/aged"; exit 0`},
	} {
		run(c.name, func(t *testing.T, w *watchRepo) {
			dir := w.recorded("7-1", "running", "running")
			w.write(filepath.Join(dir, "worker.jsonl"), "")
			w.write(filepath.Join(dir, "worker.stderr.txt"), "")
			w.fakeFile("runs-0", w.step("agent"))
			w.fakeFile("runs-1", w.step("delivery"))
			w.fakeFile("running", running)
			w.fakeFile("running-0", running)
			w.fakeFile("running-1", gateStatus("running", []string{"review,completed", "test,running"},
				"review,1s ago: log: thinking"))
			w.fakeFile("fixing-0", running)
			w.fakeFile("fixing-1", gateStatus("running", []string{"review,fixing"}, "review,1s ago: log: thinking"))
			w.fakeFile("gate-0", running)
			w.fakeFile("gate-1", strings.Replace(running, "status: running", "status: paused", 1))
			w.fakeFile("activity", gateStatus("running", []string{"review,running"}, "review,1s ago: log: WHAT"))
			w.fakeFile("aged", gateStatus("running", []string{"review,running"}, "review,AGEs ago: log: thinking"))
			w.moving(`RUN='` + dir + `'; RUNS='` + filepath.Join(w.root, ".atm", "runs.jsonl") + `'; ` + c.body)
			start := time.Now()
			out, errOut, code := w.watched("--stall", stallFor.String(), "7-1")
			if out != "done: passed\n" || code != 0 {
				t.Fatalf("a run that moved: exit %d, stdout %q, stderr %q\ncalls:\n%s", code, out, errOut, w.calls())
			}
			if took := time.Since(start); took < stallFor || w.nmCalls() != movingCalls+1 {
				t.Fatalf("watch read %d times in %s, want %d reads longer than --stall", w.nmCalls(), took, movingCalls+1)
			}
		})
	}
}

// watchAfterStalled: --after stalled waits for a move and a new stall, and hides no other event
func watchAfterStalled(run watchSubtest) {
	run("--after stalled prints stalled once the run moved and stopped again", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "running", "running")
		w.step("delivery")
		w.fakeFile("each", `if [ "$n" -le `+strconv.Itoa(movingCalls)+` ]; then cat "$FAKE/still"; exit 0; fi`+"\n")
		w.fakeFile("still", gateStatus("running", []string{"review,running"}, "review,1s ago: log: thinking"))
		w.fakeFile("status", gateStatus("running", []string{"review,completed", "test,running"},
			"test,1s ago: log: go test ./..."))
		w.stalled("test", "--after", "stalled", "--stall", stallFor.String(), "7-1")
		if n := w.nmCalls(); n <= movingCalls+1 {
			t.Fatalf("watch printed stalled again after %d reads, before the run moved and stopped again", n)
		}
	})
	run("--after stalled prints a gate question", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.fakeFile("status", gateStatus("running", []string{"review,awaiting_approval"}))
		w.expect("gate_question: review\n", 0, "--after", "stalled", "--stall", stallFor.String(), "7-1")
		if n := w.nmCalls(); n != 1 {
			t.Fatalf("--after stalled hid the question for %d reads", n)
		}
	})
	run("--after stalled prints done", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "skipped")
		w.expect("done: passed\n", 0, "--after", "stalled", "7-1")
	})
	run("a question nobody answers is stalled after --after gate_question", func(t *testing.T, w *watchRepo) {
		w.recorded("7-1", "passed", "passed")
		w.fakeFile("status", gateStatus("running", []string{"review,awaiting_approval"}))
		w.stalled("review", "--after", "gate_question", "--stall", stallFor.String(), "7-1")
	})
}
