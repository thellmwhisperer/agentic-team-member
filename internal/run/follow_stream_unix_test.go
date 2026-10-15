//go:build unix

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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// streamClock is the follow stream's clock in a test: it moves only when the test advances it, which also wakes
// the stream's wait, whatever that asked for.
type streamClock struct {
	mu   sync.Mutex
	now  time.Time
	tick chan time.Time
}

func (c *streamClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *streamClock) After(time.Duration) <-chan time.Time { return c.tick }

// streamed is a follow stream under test: its run, the repository, the clone and gate fixtures, and its lines.
type streamed struct {
	t                  *testing.T
	top, status, count string
	o                  Outcome
	clock              *streamClock
	lines              chan string
	result             chan int
	done               chan struct{} // closed when the stream ended
	cancel             context.CancelFunc
	clone, ghArg       string
}

// advance moves the clock d on and wakes the stream, then wakes it again at the same time, once it waits again:
// what it did at the first wake is done.
func (s *streamed) advance(d time.Duration) {
	s.t.Helper()
	s.clock.mu.Lock()
	s.clock.now = s.clock.now.Add(d)
	s.clock.mu.Unlock()
	for range 2 {
		select {
		case s.clock.tick <- s.clock.Now():
		case <-s.done:
			return
		case <-time.After(20 * time.Second):
			s.t.Fatal("the stream does not wait for its clock")
		}
	}
}

// gate sets what `no-mistakes axi status` says: a testdata capture with each pair of replacements.
func (s *streamed) gate(capture string, replace ...string) {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(streamTestdata, "atm_watch_status_"+capture+".txt"))
	if err != nil {
		s.t.Fatal(err)
	}
	writeFixture(s.t, s.status, strings.NewReplacer(replace...).Replace(string(b)))
}

// pr sets what gh says of the PR: its state and commits, a worker's then a CI repair commit per subject.
func (s *streamed) pr(state string, ci ...string) {
	commits := []map[string]string{{"oid": "51891fa02dd40930ba96208f54caeb863af2b4cb",
		"messageHeadline": "feat: Read gate findings for the follow stream"}}
	for i, subject := range ci {
		commits = append(commits, map[string]string{"oid": fmt.Sprintf("%040d", i+1), "messageHeadline": subject})
	}
	b, _ := json.Marshal(map[string]any{"url": recordedPR, "state": state, "commits": commits})
	s.t.Setenv("FAKE_GH_OUT", string(b))
	s.t.Setenv("FAKE_GH_FAIL", "")
}

// next is the stream's next line, which has to be one JSON object of run s.o with event and the fields want.
func (s *streamed) next(event string, want map[string]any) map[string]any {
	s.t.Helper()
	var line string
	select {
	case line = <-s.lines:
	case <-time.After(30 * time.Second):
		s.t.Fatalf("no %s line within 30 s", event)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		s.t.Fatalf("line %q is not JSON: %v", line, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, fmt.Sprint(got["time"])); err != nil || got["run"] != s.o.Run ||
		got["issue"] != s.o.Issue || got["event"] != event {
		s.t.Fatalf("line %s, want a %s event of run %s, issue %s, at a time", line, event, s.o.Run, s.o.Issue)
	}
	for k, v := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			s.t.Fatalf("%s %s = %v, want %v\nline %s", event, k, got[k], v, line)
		}
	}
	return got
}

// ended wants the stream ended with result, line last.
func (s *streamed) ended(result int) {
	s.t.Helper()
	select {
	case got := <-s.result:
		if got != result {
			s.t.Fatalf("stream result %d, want %d", got, result)
		}
	case <-time.After(30 * time.Second):
		s.t.Fatal("the stream did not end")
	}
	if l, ok := <-s.lines; ok {
		s.t.Fatalf("line %s after the last", l)
	}
}

// delivered reads the run's steps up to its delivery and its clone, then waits for the run to end.
func (s *streamed) delivered() {
	s.t.Helper()
	for _, step := range []string{"issue", "clone", "contract", "agent", "checks", "ponytail", "delivery"} {
		s.next("step", map[string]any{"step": step})
	}
	s.clone = fmt.Sprint(s.next("delivery", nil)["clone"])
	if !strings.HasPrefix(s.clone, filepath.Join(s.top, ".atm", "clones")) {
		s.t.Fatalf("delivery clone %q", s.clone)
	}
	s.runEnded()
}

// waitsAgain has the gate's wait at review clear, then come back: a new wait.
func (s *streamed) waitsAgain() {
	s.t.Helper()
	s.gate("running")
	s.advance(10 * time.Second)
	s.gate("parked")
	s.advance(10 * time.Second)
	s.next("gate_waiting", map[string]any{"step": "review"})
}

func (s *streamed) runEnded() {
	s.t.Helper()
	end, err := Attach(s.top, s.o.Run, io.Discard)
	if err != nil {
		s.t.Fatal(err)
	}
	s.o.FailedNode, s.o.Reason = end.FailedNode, end.Reason
}

func (s *streamed) calls() string {
	b, _ := os.ReadFile(s.count)
	return string(b)
}

var streamTestdata, _ = filepath.Abs("testdata")

// followStream starts a run of a repository on GitHub whose delivery is line, and the follow stream of it, by
// reflection, on a test clock: gh fails and the gate has no step until the test says otherwise.
func followStream(t *testing.T, atm string) *streamed {
	t.Helper()
	s := &streamed{t: t, clock: &streamClock{now: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC),
		tick: make(chan time.Time)}, lines: make(chan string, 100), result: make(chan int, 1),
		done: make(chan struct{})}
	s.count = fakeStatus(t)
	s.status = filepath.Join(t.TempDir(), "status.txt")
	writeFixture(t, s.status, "")
	for _, env := range []string{"ATM_STATUS_LIVE", "ATM_STATUS_WAITING", "ATM_STATUS_MERGED"} {
		t.Setenv(env, s.status) // the fake's second call fails; the others read status
	}
	s.ghArg = fakeGH(t, "", true)
	s.top = gitT(t, repo(t, "https://github.com/o/r.git", atm), "rev-parse", "--show-toplevel")
	background(t, s.top)
	o, err := startWithWatcher(t, s.top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	s.o = o
	stream := reflect.ValueOf(o).MethodByName("Stream")
	if !stream.IsValid() {
		t.Fatal("Outcome has no Stream: nothing follows one run as JSON")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	r, w := io.Pipe()
	go func() {
		out := stream.Call([]reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(s.top), reflect.ValueOf(w),
			reflect.ValueOf(s.clock.Now), reflect.ValueOf(s.clock.After)})
		s.result <- int(out[0].Int())
		close(s.done)
		_ = w.Close()
	}()
	go func() {
		defer close(s.lines)
		for sc := bufio.NewScanner(r); sc.Scan(); {
			s.lines <- sc.Text()
		}
	}()
	t.Cleanup(cancel)
	return s
}

var streamFixingCI = []string{"ci,completed,", "ci,fixing,"}

// A run passes, its gate waits twice, fixes CI, goes green, and its PR merges after its clone is gone: one line per
// event, each read before the next fixture, none for a snapshot seen already, merged last with 0.
func TestFollowStreamPassingTrace(t *testing.T) {
	s := followStream(t, atmSet(atmYAML, "delivery", "true"))
	s.delivered()
	s.gate("parked")
	s.advance(10 * time.Second) // the first read
	w := s.next("gate_waiting", map[string]any{"step": "review", "clone": s.clone})
	assertStreamFindings(t, w["findings"])
	s.advance(10 * time.Second) // the fake's second call fails: no event
	s.advance(10 * time.Second) // the same wait: no line
	s.waitsAgain()
	s.pr("OPEN", `no-mistakes(ci): Fix lint, "unused" import`)
	s.gate("completed", streamFixingCI...)
	s.advance(10 * time.Second)
	s.next("pr_opened", map[string]any{"url": recordedPR})
	s.next("gate_fixing_ci", map[string]any{"commit": map[string]any{"sha": fmt.Sprintf("%040d", 1),
		"subject": `no-mistakes(ci): Fix lint, "unused" import`}})
	s.advance(10 * time.Second) // the same commit: no line
	s.gate("completed")
	s.advance(10 * time.Second)
	s.next("ci_green", map[string]any{"url": recordedPR})
	if err := os.RemoveAll(s.clone); err != nil {
		t.Fatal(err)
	}
	s.advance(10 * time.Second) // no clone, the same PR: no line
	s.pr("MERGED", `no-mistakes(ci): Fix lint, "unused" import`, "no-mistakes(ci): Retry the lease read on Windows")
	s.advance(10 * time.Second) // a CI commit and the merge between two polls
	s.next("gate_fixing_ci", map[string]any{"commit": map[string]any{"sha": fmt.Sprintf("%040d", 2),
		"subject": "no-mistakes(ci): Retry the lease read on Windows"}})
	s.next("merged", map[string]any{"url": recordedPR})
	s.ended(0)
}

func assertStreamFindings(t *testing.T, findings any) {
	t.Helper()
	got, _ := json.Marshal(findings)
	var want []map[string]string
	b, err := os.ReadFile(filepath.Join(streamTestdata, "atm_watch_status_parked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range parseGate(string(b)).findings {
		want = append(want, map[string]string{"id": f.id, "severity": f.severity, "file": f.file,
			"description": f.description})
	}
	if w, _ := json.Marshal(want); len(want) != 2 || string(got) != string(w) {
		t.Fatalf("findings %s, want %s", got, w)
	}
}

// A run that fails at its checks ends the stream with its step and reason.
func TestFollowStreamRunFailedAtChecks(t *testing.T) {
	s := followStream(t, atmSet(atmYAML, "lint", "exit 1"))
	for _, step := range []string{"issue", "clone", "contract", "agent", "checks"} {
		s.next("step", map[string]any{"step": step})
	}
	s.runEnded()
	if s.o.FailedNode != "checks" || s.o.Reason == "" {
		t.Fatalf("run failed at %q: %q; want checks", s.o.FailedNode, s.o.Reason)
	}
	s.next("run_failed", map[string]any{"step": "checks", "reason": s.o.Reason})
	s.ended(1)
}

// A gate that fails while its PR is open fails the stream.
func TestFollowStreamGateFailedWithOpenPR(t *testing.T) {
	s := followStream(t, atmSet(atmYAML, "delivery", "true"))
	s.delivered()
	s.pr("OPEN")
	s.gate("completed", "ci,completed,", "ci,failed,")
	s.advance(10 * time.Second)
	s.next("pr_opened", map[string]any{"url": recordedPR})
	s.next("run_failed", map[string]any{"step": "ci"})
	s.ended(1)
}

func TestFollowStreamPublicGateReadRemainsAtomic(t *testing.T) {
	for _, commandFails := range []bool{true, false} {
		t.Run(fmt.Sprint(commandFails), func(t *testing.T) {
			fakeStatus(t)
			status := filepath.Join(t.TempDir(), "status.txt")
			writeFixture(t, status, recorded(t, "atm_watch_status_completed.txt"))
			t.Setenv("ATM_STATUS_LIVE", status)
			fakeGH(t, "invalid JSON", commandFails)
			before := gateState{pr: "https://github.com/o/r/pull/1", waiting: "review", seen: true}
			got := before
			if err := got.Read(t.TempDir()); err == nil {
				t.Fatal("Read succeeded despite the failed commit read")
			}
			if !reflect.DeepEqual(got, before) {
				t.Fatalf("failed Read changed the snapshot: got %+v, want %+v", got, before)
			}
		})
	}
}

func TestFollowStreamGateFailureSurvivesCommitReadError(t *testing.T) {
	for _, broken := range []string{"exit 1", "printf 'invalid JSON'"} {
		for _, state := range []string{"MERGED", "CLOSED", "OPEN", "unreadable"} {
			t.Run(broken+"/"+state, func(t *testing.T) {
				s := followStream(t, atmSet(atmYAML, "delivery", "true"))
				s.delivered()
				s.gate("completed", "ci,completed,", "ci,failed,")
				s.pr(state, "no-mistakes(ci): Repair CI")
				if state == "unreadable" {
					t.Setenv("FAKE_GH_FAIL", "1")
				}
				bin := t.TempDir()
				writeFixture(t, filepath.Join(bin, "gh"), `#!/bin/sh
case "$*" in
  *"--json commits") `+broken+` ;;
  *) test -z "$FAKE_GH_FAIL" || exit 1
     printf '%s\n' "$FAKE_GH_OUT" ;;
esac
`, 0o755)
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				s.advance(10 * time.Second)
				s.next("pr_opened", map[string]any{"url": recordedPR})
				if state != "unreadable" {
					s.next("gate_fixing_ci", map[string]any{"commit": map[string]any{
						"sha": "0000000000000000000000000000000000000001", "subject": "no-mistakes(ci): Repair CI"}})
				}
				s.next("run_failed", map[string]any{"step": "ci", "reason": "no-mistakes step ci failed"})
				s.ended(1)
			})
		}
	}
}

func TestFollowStreamGateFailureBeforePRTerminal(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			s := followStream(t, atmSet(atmYAML, "delivery", "true"))
			s.delivered()
			s.gate("completed", "ci,completed,", "ci,failed,")
			s.pr(state, "no-mistakes(ci): Repair CI")
			bin := t.TempDir()
			writeFixture(t, filepath.Join(bin, "gh"), `#!/bin/sh
case "$*" in
  *"--json commits") printf '%s\n' '{"state":"OPEN","commits":[]}' ;;
  *) printf '%s\n' "$FAKE_GH_OUT" ;;
esac
`, 0o755)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			s.advance(10 * time.Second)
			s.next("pr_opened", map[string]any{"url": recordedPR})
			s.next("gate_fixing_ci", map[string]any{"commit": map[string]any{
				"sha": "0000000000000000000000000000000000000001", "subject": "no-mistakes(ci): Repair CI"}})
			s.next("run_failed", map[string]any{"step": "ci", "reason": "no-mistakes step ci failed"})
			s.ended(1)
		})
	}
}

// A PR closed unmerged ends the stream with closed, last, and 1.
func TestFollowStreamClosedPR(t *testing.T) {
	s := followStream(t, atmSet(atmYAML, "delivery", "true"))
	s.delivered()
	s.gate("running")
	s.advance(10 * time.Second)
	s.pr("CLOSED")
	s.gate("completed", streamFixingCI...)
	s.advance(10 * time.Second)
	s.next("pr_opened", map[string]any{"url": recordedPR})
	s.next("closed", map[string]any{"url": recordedPR})
	s.ended(1)
}

// The clone goes before the stream read its PR's URL: gh finds the PR by the delivery's branch.
func TestFollowStreamFindsThePRByBranchAfterTheCloneWent(t *testing.T) {
	s := followStream(t, atmSet(atmYAML, "delivery", "true"))
	s.delivered()
	if err := os.RemoveAll(s.clone); err != nil {
		t.Fatal(err)
	}
	s.advance(10 * time.Second) // gh still fails: no PR yet
	s.pr("OPEN")
	s.advance(10 * time.Second)
	s.next("pr_opened", map[string]any{"url": recordedPR})
	b, err := os.ReadFile(s.ghArg)
	if err != nil || !strings.HasPrefix(string(b), "pr view atm/retry-on-timeout-") ||
		!strings.HasSuffix(string(b), " --repo o/r --json url,state,commits") {
		t.Fatalf("gh args %q, %v; want the PR of the delivery's branch", b, err)
	}
	s.pr("MERGED")
	s.advance(10 * time.Second)
	s.next("merged", map[string]any{"url": recordedPR})
	s.ended(0)
}

// A delivery command that fails before the stream read its gate leaves a live gate followed; one that failed
// before the command ran, with no gate to come, ends the stream.
func TestFollowStreamDeliveryErrorBeforeTheGateRead(t *testing.T) {
	t.Run("after its command ran", func(t *testing.T) {
		s := followStream(t, atmSet(atmYAML, "delivery", "exit 1"))
		s.delivered()
		if s.o.FailedNode != "delivery" {
			t.Fatalf("run failed at %q, want delivery", s.o.FailedNode)
		}
		s.gate("running")
		s.advance(10 * time.Second)
		s.advance(10 * time.Second) // the fake's failed call
		s.gate("parked")
		s.advance(10 * time.Second)
		s.next("gate_waiting", map[string]any{"step": "review"})
		s.pr("MERGED")
		s.gate("completed")
		s.advance(10 * time.Second)
		s.next("pr_opened", map[string]any{"url": recordedPR})
		s.next("ci_green", map[string]any{"url": recordedPR})
		s.next("merged", map[string]any{"url": recordedPR})
		s.ended(0)
	})
	t.Run("before its command ran", func(t *testing.T) {
		t.Setenv("FAKE_REVIEW", "fail")
		s := followStream(t, atmSet(atmYAML, "delivery", "true"))
		s.delivered()
		s.next("run_failed", map[string]any{"step": "delivery", "reason": s.o.Reason})
		s.ended(1)
	})
}

// An hour without a line is stalled, not a minute earlier, and so each hour after; a line starts the hour again.
// The gate is read at most every 10 s, however often the stream wakes.
func TestFollowStreamStallsAndPollsNoFasterThanEvery10s(t *testing.T) {
	s := followStream(t, atmSet(atmYAML, "delivery", "true"))
	s.delivered()
	s.gate("parked")
	s.advance(10 * time.Second)
	s.next("gate_waiting", map[string]any{"step": "review"})
	calls := s.calls()
	s.advance(3 * time.Second)
	s.advance(6 * time.Second)
	if got := s.calls(); got != calls {
		t.Fatalf("status calls %s after 9 s, want %s", got, calls)
	}
	s.advance(time.Second)
	if got := s.calls(); got == calls {
		t.Fatalf("no status call 10 s after the last")
	}
	s.advance(59*time.Minute - 10*time.Second)
	s.advance(59 * time.Second)
	s.advance(time.Second)
	s.next("stalled", map[string]any{"minutes": 60})
	s.advance(time.Hour)
	s.next("stalled", map[string]any{"minutes": 120})
	s.waitsAgain()
	s.advance(59 * time.Minute)
	s.advance(time.Minute)
	s.next("stalled", map[string]any{"minutes": 60})
}

// Cancelling the stream while it reads a gate that hangs ends it, its read and its attachment, and only it: the
// run goes on to pass.
func TestFollowStreamCancelStopsOnlyTheObserver(t *testing.T) {
	_, gate := fifos(t)
	s := followStream(t, atmSet(atmYAML, "delivery", `cat "$ATM_TEST_GATE" >/dev/null`))
	hang := filepath.Join(t.TempDir(), "hang")
	if err := syscall.Mkfifo(hang, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	reading := filepath.Join(bin, "reading")
	writeFixture(t, filepath.Join(bin, "no-mistakes"), "#!/bin/sh\n: > '"+reading+"'\nexec cat '"+hang+"'\n", 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, step := range []string{"issue", "clone", "contract", "agent", "checks", "ponytail", "delivery"} {
		s.next("step", map[string]any{"step": step})
	}
	s.next("delivery", nil)
	s.clock.mu.Lock()
	s.clock.now = s.clock.now.Add(10 * time.Second)
	s.clock.mu.Unlock()
	select { // a frame may have woken it first
	case s.clock.tick <- s.clock.Now():
	case <-time.After(time.Second):
	}
	until(t, "the stream reads its gate", func() bool { _, err := os.Stat(reading); return err == nil })
	s.cancel()
	select {
	case <-s.result:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream outlived its cancellation")
	}
	if runs, err := Runs(s.top); err != nil || runs[0].Outcome != "running" {
		t.Fatalf("Runs = %+v, %v; want the run going", runs, err)
	}
	writeFixture(t, gate, "")
	if end, err := Attach(s.top, s.o.Run, io.Discard); err != nil || end.Outcome != "passed" {
		t.Fatalf("run = %+v, %v; want passed", end, err)
	}
}
