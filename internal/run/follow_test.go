package run

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// following runs FollowRun with args on root until the end of the test: it returns each line it prints, parsed,
// failing the test on a line that is not one JSON object with time, run, issue and event, and its end.
func following(t *testing.T, root string, args []string) (lines <-chan map[string]any, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, w := io.Pipe()
	ended, stopped := make(chan error, 1), make(chan struct{})
	go func() { ended <- FollowRun(ctx, root, args, w); _ = w.Close(); close(stopped) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
		_, _ = Attach(root, "issue-1", io.Discard) // a fake merged PR may precede the background run's end
	})
	out := make(chan map[string]any, 100)
	sc := bufio.NewScanner(r)
	go func() {
		for sc.Scan() {
			var ev map[string]any
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				t.Errorf("line %q is not JSON: %v", sc.Text(), err)
			}
			for _, k := range []string{"time", "run", "issue", "event"} {
				if _, ok := ev[k].(string); !ok {
					t.Errorf("line %q has no %s", sc.Text(), k)
				}
			}
			out <- ev
		}
		close(out)
	}()
	return out, ended
}

func nextEvent(t *testing.T, lines <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case ev, ok := <-lines:
		if !ok {
			t.Fatal("follow ended its output")
		}
		return ev
	case <-time.After(20 * time.Second):
		t.Fatal("no line within 20 s")
	}
	return nil
}

// eventsUntil reads events up to the first named last, and checks follow then ends with code.
func eventsUntil(t *testing.T, lines <-chan map[string]any, done <-chan error, last string, code int) (
	evs []map[string]any) {
	t.Helper()
	for len(evs) == 0 || evs[len(evs)-1]["event"] != last {
		evs = append(evs, nextEvent(t, lines))
	}
	select {
	case err := <-done:
		if ExitCode(err) != code {
			t.Fatalf("follow ended with %v, exit %d; want %d", err, ExitCode(err), code)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("follow did not end after %s", last)
	}
	if ev, ok := <-lines; ok {
		t.Fatalf("follow printed %v after %s", ev, last)
	}
	return evs
}

// fakeGate puts on PATH a no-mistakes whose axi status replays statuses, one a call, the last again from then
// on, and a gh whose pr view says the PR is state, with a CI fix commit of the gate and a review one.
func fakeGate(t *testing.T, state string, statuses ...string) {
	t.Helper()
	dir := t.TempDir()
	for i, s := range statuses {
		writeFixture(t, filepath.Join(dir, strconv.Itoa(i+1)), s)
	}
	t.Setenv("FAKE_GATE", dir)
	installFakeNoMistakes(t)
	fakeGH(t, `{"state":"`+state+`","commits":[{"oid":"c1","messageHeadline":"no-mistakes(ci): fix lint"},`+
		`{"oid":"c2","messageHeadline":"no-mistakes(review): tidy"}]}`, false)
}

// gateRows is a gate's steps, a TOON table.
func gateRows(rows ...string) string {
	return "  steps[3]{step,status,findings,duration_ms}:\n    " + strings.Join(rows, "\n    ") + "\n"
}

// waitingOn is a gate waiting on review with findings, rows of a TOON findings table.
func waitingOn(findings ...string) string {
	return "run:\n  branch: atm/fix\n" + gateRows("review,awaiting_approval,2,1", "pr,pending,0,0", "ci,pending,0,0") +
		fmt.Sprintf("  findings[%d]{id,severity,file,line,description}:\n    ", len(findings)) +
		strings.Join(findings, "\n    ") + "\n"
}

// greenPR is a gate whose PR 7 is open and green.
var greenPR = "run:\n  pr: \"https://github.com/o/r/pull/7\"\n" +
	gateRows("review,completed,0,1", "pr,completed,0,1", "ci,completed,0,1")

// mergingGate is a fake gate that waits on review twice, with two findings, then fixes CI, opens PR 7, goes
// green, and merges.
func mergingGate(t *testing.T) {
	t.Helper()
	waiting := waitingOn(`r1,error,a.txt,1,"broken, still: fix it"`, "r2,warning,a_test.sh,1,weak test")
	fixing := "run:\n" + gateRows("review,completed,0,1", "pr,pending,0,0", "ci,fixing,1,1")
	fakeGate(t, "MERGED", waiting, waiting, fixing, greenPR)
}

// deliveringRepo is backgroundRepo with a delivery, its gate read every 2 ms.
func deliveringRepo(t *testing.T) string {
	t.Helper()
	b := beat
	beat = 2 * time.Millisecond
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery", "true")),
		"rev-parse", "--show-toplevel")
	t.Cleanup(func() { beat = b })
	background(t, top)
	return top
}

func TestFollowPrintsEachEventUntilMerged(t *testing.T) {
	top := deliveringRepo(t)
	mergingGate(t)
	lines, done := following(t, top, []string{issueFile(t, issue)})
	evs := eventsUntil(t, lines, done, "merged", 0)
	var got []string
	for _, ev := range evs {
		if ev["run"] != "issue-1" || !strings.HasSuffix(ev["issue"].(string), "issue.md") {
			t.Fatalf("event %v is not of run issue-1", ev)
		}
		name := ev["event"].(string)
		if name == "step" {
			name += " " + ev["step"].(string)
		}
		got = append(got, name)
	}
	want := []string{"step issue", "step clone", "step contract", "step agent", "step checks", "step ponytail",
		"delivery", "gate_waiting", "gate_fixing_ci", "pr_opened", "ci_green", "merged"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("follow printed %q, want %q", got, want)
	}
	clone := evs[6]["clone"].(string)
	if !strings.Contains(clone, filepath.Join(".atm", "clones", "atm-run-")) {
		t.Fatalf("delivery clone %q", clone)
	}
	wantWait := map[string]any{"step": "review", "clone": clone, "findings": []any{
		map[string]any{"id": "r1", "severity": "error", "file": "a.txt", "description": "broken, still: fix it"},
		map[string]any{"id": "r2", "severity": "warning", "file": "a_test.sh", "description": "weak test"},
	}}
	for k, v := range wantWait {
		if !reflect.DeepEqual(evs[7][k], v) {
			t.Fatalf("gate_waiting %s = %v, want %v", k, evs[7][k], v)
		}
	}
	if want := map[string]any{"sha": "c1", "subject": "no-mistakes(ci): fix lint"}; !reflect.DeepEqual(
		evs[8]["commit"], want) {
		t.Fatalf("gate_fixing_ci commit = %v, want %v", evs[8]["commit"], want)
	}
	for _, ev := range evs[9:] {
		if ev["url"] != "https://github.com/o/r/pull/7" {
			t.Fatalf("%s url = %v", ev["event"], ev["url"])
		}
	}
}

func TestFollowPrintsClosedAndExits1(t *testing.T) {
	top := deliveringRepo(t)
	fakeGate(t, "CLOSED", "run:\n  pr: \"https://github.com/o/r/pull/8\"\n"+
		gateRows("review,completed,0,1", "pr,completed,0,1", "ci,running,0,1"))
	lines, done := following(t, top, []string{issueFile(t, issue)})
	evs := eventsUntil(t, lines, done, "closed", 1)
	if last := evs[len(evs)-1]; last["url"] != "https://github.com/o/r/pull/8" ||
		evs[len(evs)-2]["event"] != "pr_opened" {
		t.Fatalf("follow ended with %v", evs[len(evs)-2:])
	}
}

func TestFollowFindsClosedPRWhenDeliveryRemovedTheClone(t *testing.T) {
	top := repo(t, "https://github.com/o/r.git", atmYAML)
	fakeGH(t, `{"state":"CLOSED","url":"https://github.com/o/r/pull/8"}`, false)
	g := follower{clone: filepath.Join(top, "removed-clone"), branch: "atm/fix"}
	evs := g.poll(top)
	want := []followEvent{{"event": "pr_opened", "url": "https://github.com/o/r/pull/8"},
		{"event": "closed", "url": "https://github.com/o/r/pull/8"}}
	if !reflect.DeepEqual(evs, want) || ExitCode(over(evs[len(evs)-1])) != 1 {
		t.Fatalf("events after clone removal = %v, want %v and exit 1", evs, want)
	}
}

func TestFollowPrintsRunFailedAtChecksAndExits1(t *testing.T) {
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "typecheck", "false")),
		"rev-parse", "--show-toplevel")
	background(t, top)
	lines, done := following(t, top, []string{issueFile(t, issue)})
	evs := eventsUntil(t, lines, done, "run_failed", 1)
	last := evs[len(evs)-1]
	if last["step"] != "checks" || !strings.Contains(last["reason"].(string), "typecheck") {
		t.Fatalf("run_failed = %v, want step checks and typecheck's reason", last)
	}
}

func TestFollowPrintsStalledWhileNothingChanges(t *testing.T) {
	defer func(b, s time.Duration) { beat, stall = b, s }(beat, stall)
	beat, stall = 2*time.Millisecond, 30*time.Millisecond
	top := backgroundRepo(t)
	wait, release := holdFakeAgents(t, 1)
	lines, done := following(t, top, []string{issueFile(t, issue)})
	t.Cleanup(release)
	wait()
	var stalls []map[string]any
	for len(stalls) < 2 { // two in a row, once the agent holds the run
		if ev := nextEvent(t, lines); ev["event"] == "stalled" {
			stalls = append(stalls, ev)
		} else {
			stalls = nil
		}
	}
	for _, ev := range stalls {
		if _, ok := ev["minutes"].(float64); !ok {
			t.Fatalf("stalled without minutes: %v", ev)
		}
	}
	release()
	for range lines { // the run passes without a delivery: nothing more to follow
	}
	if err := <-done; err != nil {
		t.Fatalf("follow ended with %v after the run passed", err)
	}
}

func TestFollowPrintsGateWaitingAgainForAReviewRoundOnTheSameStep(t *testing.T) {
	top := deliveringRepo(t)
	first, second := waitingOn("r1,error,a.txt,1,broken"), waitingOn("r3,error,a.txt,2,still broken")
	fakeGate(t, "MERGED", first, first, second, second, greenPR)
	lines, done := following(t, top, []string{issueFile(t, issue)})
	var waits []any
	for _, ev := range eventsUntil(t, lines, done, "merged", 0) {
		if ev["event"] == "gate_waiting" {
			waits = append(waits, ev["findings"].([]any)[0].(map[string]any)["id"])
		}
	}
	if !reflect.DeepEqual(waits, []any{"r1", "r3"}) {
		t.Fatalf("gate_waiting printed for %v, want once for r1 and once for r3", waits)
	}
}

func TestFollowReportsParkedReviewAndContinues(t *testing.T) {
	status, err := os.ReadFile("testdata/atm_watch_status_parked.txt")
	if err != nil {
		t.Fatal(err)
	}
	top := deliveringRepo(t)
	fakeGate(t, "MERGED", string(status), string(status), greenPR)
	lines, done := following(t, top, []string{issueFile(t, issue)})
	var waits []map[string]any
	for _, ev := range eventsUntil(t, lines, done, "merged", 0) {
		if ev["event"] == "gate_waiting" {
			waits = append(waits, ev)
		}
	}
	if len(waits) != 1 || waits[0]["step"] != "review" {
		t.Fatalf("parked gate waits = %v, want one review wait", waits)
	}
	findings := waits[0]["findings"].([]any)
	if len(findings) != 2 || findings[0].(map[string]any)["id"] != "doctor-duplicate-watcher" ||
		findings[1].(map[string]any)["id"] != "follow-ci-commit-provenance-unresolved" {
		t.Fatalf("parked gate findings = %v, want both recorded findings", findings)
	}
}

func TestFollowPrintsRunFailedWhenTheGateFailsBeforeAPR(t *testing.T) {
	top := deliveringRepo(t)
	fakeGate(t, "OPEN", "run:\n"+gateRows("review,failed,1,1", "pr,pending,0,0", "ci,pending,0,0"))
	lines, done := following(t, top, []string{issueFile(t, issue)})
	evs := eventsUntil(t, lines, done, "run_failed", 1)
	if last := evs[len(evs)-1]; last["step"] != "delivery" || !strings.Contains(last["reason"].(string), "review") {
		t.Fatalf("run_failed = %v, want step delivery and the gate's failed step", last)
	}
}

func TestFollowPrintsRunFailedWhenTheGateFailsWithAnOpenPR(t *testing.T) {
	top := deliveringRepo(t)
	fakeGate(t, "OPEN", "run:\n  pr: \"https://github.com/o/r/pull/7\"\n"+
		gateRows("review,completed,0,1", "pr,completed,0,1", "ci,failed,1,1"))
	lines, done := following(t, top, []string{issueFile(t, issue)})
	evs := eventsUntil(t, lines, done, "run_failed", 1)
	last := evs[len(evs)-1]
	if last["step"] != "delivery" || last["reason"] != "no-mistakes failed at ci" {
		t.Fatalf("run_failed = %v, want gate failure at ci", last)
	}
	if evs[len(evs)-2]["event"] != "pr_opened" {
		t.Fatalf("events before run_failed = %v, want pr_opened", evs)
	}
}

func TestFollowKeepsItsLeaseFreshWhileNoOneReadsIt(t *testing.T) {
	defer func(b, s time.Duration) { beat, stale = b, s }(beat, stale)
	beat, stale = 2*time.Millisecond, 100*time.Millisecond
	top := backgroundRepo(t)
	_, release := holdFakeAgents(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	r, w := io.Pipe() // never read: the follow blocks on its first line
	done := make(chan struct{})
	go func() { _ = FollowRun(ctx, top, []string{issueFile(t, issue)}, w); close(done) }()
	t.Cleanup(func() {
		cancel()
		_ = r.Close()
		<-done
		release()
		_, _ = Attach(top, "issue-1", io.Discard)
	})
	time.Sleep(5 * stale)
	if ids := Watchers(top); len(ids) != 1 {
		t.Fatalf("live watchers %v after %s unread, want the follow's", ids, 5*stale)
	}
}
