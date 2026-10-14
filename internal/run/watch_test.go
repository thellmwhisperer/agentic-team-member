package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// watching runs Watch on root, adopting adopt if set, until the end of the test: it returns the watcher's id, its
// lines after the first, as it prints them, and its end.
func watching(t *testing.T, root, adopt string) (id string, lines <-chan string, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, w := io.Pipe()
	ended := make(chan error, 1)
	go func() { ended <- Watch(ctx, root, adopt, w); _ = w.Close() }()
	t.Cleanup(func() { cancel(); <-ended })
	out := make(chan string, 100)
	sc := bufio.NewScanner(r)
	go func() {
		for sc.Scan() {
			out <- sc.Text()
		}
	}()
	first := nextLine(t, out)
	id, ok := strings.CutPrefix(first, "watching as ")
	if !ok || id == "" {
		t.Fatalf("first line %q, want watching as <id>", first)
	}
	return id, out, ended
}

func nextLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l := <-lines:
		return l
	case <-time.After(20 * time.Second):
		t.Fatal("no line within 20 s")
	}
	return ""
}

// watchEvent is a watcher's line for run label without its time.
func watchEvent(t *testing.T, line, label string) string {
	t.Helper()
	m := regexp.MustCompile(`^\d\d:\d\d:\d\d ` + regexp.QuoteMeta(label) + ` (.+)$`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("line %q is not <time> %s <event>", line, label)
	}
	return m[1]
}

func TestWatchPrintsEachStepAndKeepsWatchingAfterTheRun(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = 10 * time.Millisecond
	top := backgroundRepo(t)
	id, lines, done := watching(t, top, "")
	o, err := Start(top, []string{"--watcher", id, issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "finished") {
		got = append(got, watchEvent(t, nextLine(t, lines), o.Run))
	}
	want := []string{"started", "clone", "contract", "agent", "checks", "ponytail", "finished: passed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("watcher printed %q, want %q", got, want)
	}
	select {
	case err := <-done:
		t.Fatalf("watch ended after the run: %v", err)
	case l := <-lines:
		t.Fatalf("watch printed %q after the run ended", l)
	case <-time.After(50 * beat):
	}
	runs, err := Runs(top)
	if err != nil || len(runs) != 1 || runs[0].Watcher != id {
		t.Fatalf("Runs = %+v, %v; want the run bound to %s", runs, err, id)
	}
	if b, err := os.ReadFile(o.Report); err != nil || !bytes.Contains(b, []byte(`"watcher": "`+id+`"`)) {
		t.Fatalf("report.json does not record watcher %s: %v\n%s", id, err, b)
	}
}

// A killed watcher leaves WATCHER GONE on the screen of each run bound to it, once; another adopts the run.
func TestAKilledWatcherLeavesItsRunsUnwatchedUntilAnotherAdoptsThem(t *testing.T) {
	t.Run("watcher death and adoption", testKilledWatcherAdoption)
}

func testKilledWatcherAdoption(t *testing.T) {
	defer func(b, k time.Duration) { beat, tick = b, k }(beat, tick)
	beat, tick = 10*time.Millisecond, time.Hour
	top := backgroundRepo(t)
	wait, release := holdFakeAgents(t, 1)
	id, kill := watcherProcess(t, top)
	o, err := Start(top, []string{issueFile(t, issue), "--watcher", id})
	if err != nil {
		t.Fatal(err)
	}
	wait()
	kill()
	gone := make(chan string, 10)
	go func() {
		_, _ = Follow(top, o.Run, nil, nil, func(f Frame) {
			if f.Line != nil && strings.Contains(*f.Line, "WATCHER GONE") {
				gone <- *f.Line
			}
		})
	}()
	newID, lines, _ := watching(t, top, o.Run)
	assertWatcherGoneLine(t, nextLine(t, gone))
	if l := watchEvent(t, nextLine(t, lines), o.Run); l != "adopted: running at agent" {
		t.Fatalf("adopting watcher printed %q first", l)
	}
	release()
	var got []string
	for len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "finished") {
		got = append(got, watchEvent(t, nextLine(t, lines), o.Run))
	}
	if want := []string{"checks", "ponytail", "finished: passed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("adopting watcher printed %q, want %q", got, want)
	}
	assertAdoptedWatcherRecorded(t, top, o.Run, newID)
}

func assertWatcherGoneLine(t *testing.T, line string) {
	t.Helper()
	if !regexp.MustCompile(`^WATCHER GONE at \d\d:\d\d:\d\d: no one is watching this run$`).MatchString(line) {
		t.Fatalf("screen line %q", line)
	}
}

func assertAdoptedWatcherRecorded(t *testing.T, top, run, watcher string) {
	t.Helper()
	var screen bytes.Buffer
	if _, err := Attach(top, run, &screen); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(screen.String(), "WATCHER GONE"); n != 1 {
		t.Fatalf("WATCHER GONE %d times on the screen:\n%s", n, &screen)
	}
	if runs, err := Runs(top); err != nil || runs[0].Watcher != watcher {
		t.Fatalf("Runs = %+v, %v; want the run bound to %s", runs, err, watcher)
	}
	var report verdict
	b, err := os.ReadFile(filepath.Join(runDir(top, run), "report.json"))
	if err != nil || json.Unmarshal(b, &report) != nil || report.Watcher != watcher {
		t.Fatalf("report watcher = %q, %v; want %s", report.Watcher, err, watcher)
	}
}

func TestWatcherDeathIsLoggedWhenRunEndsBeforeServerTick(t *testing.T) {
	defer func(k time.Duration) { tick = k }(tick)
	tick = time.Hour
	top := backgroundRepo(t)
	wait, release := holdFakeAgents(t, 1)
	id, kill := watcherProcess(t, top)
	o, err := startWithWatcher(t, top, []string{issueFile(t, issue), "--watcher", id})
	if err != nil {
		t.Fatal(err)
	}
	wait()
	kill()
	release()
	var screen bytes.Buffer
	if _, err := Attach(top, o.Run, &screen); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(screen.String(), "WATCHER GONE"); n != 1 {
		t.Fatalf("WATCHER GONE %d times on the screen:\n%s", n, &screen)
	}
}

func TestWatchLiveness(t *testing.T) {
	top := atmRepo(t)
	if err := os.MkdirAll(filepath.Join(top, ".atm", "watch"), 0o755); err != nil {
		t.Fatal(err)
	}
	dead := exec.Command(os.Args[0], "-test.run=^$")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for id, l := range map[string]lease{
		"live":  {os.Getpid(), now, now},
		"stale": {os.Getpid(), now.Add(-time.Minute), now.Add(-31 * time.Second)},
		"dead":  {dead.Process.Pid, now, now},
	} {
		if err := l.write(top, id); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]bool{"live": true, "stale": false, "dead": false, "none": false,
		"../watch/live": false} {
		if got := Watching(top, id); got != want {
			t.Errorf("Watching(%q) = %v, want %v", id, got, want)
		}
	}
}

// liveLease is a repository with the fresh lease l of a live watcher, "live".
func liveLease(t *testing.T) (top string, l lease) {
	top = atmRepo(t)
	now := time.Now()
	l = lease{os.Getpid(), now, now}
	if err := l.write(top, "live"); err != nil {
		t.Fatal(err)
	}
	return top, l
}

// A live watcher stays live while it rewrites its lease, as it does every beat.
func TestWatchingHoldsWhileItsWatcherRewritesItsLease(t *testing.T) {
	top, l := liveLease(t)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
				_ = l.write(top, "live")
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	for i := range 2000 {
		if !Watching(top, "live") {
			t.Fatalf("Watching = false at check %d while its watcher rewrites its lease", i)
		}
	}
}

// On Windows a lease being renamed over cannot be read, at times for over 100 ms: its watcher stays live.
func TestWatchingHoldsWhileItsLeaseCannotBeReadForAMoment(t *testing.T) {
	top, _ := liveLease(t)
	if err := os.Rename(leasePath(top, "live"), leasePath(top, "live")+".tmp"); err != nil {
		t.Fatal(err)
	}
	back := time.AfterFunc(200*time.Millisecond, func() {
		_ = os.Rename(leasePath(top, "live")+".tmp", leasePath(top, "live"))
	})
	defer back.Stop()
	if !Watching(top, "live") {
		t.Fatal("Watching = false while its lease could not be read for 200 ms")
	}
}

func TestWatchGateRetriesFirstStatusErrorAfterRunEnds(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	count := fakeStatus(t)
	ended := make(chan struct{})
	close(ended)
	watchGate(context.Background(), t.TempDir(), t.TempDir(), nil, "run", ended, func(string, string) {})
	b, err := os.ReadFile(count)
	if err != nil || string(b) != "2" {
		t.Fatalf("status calls = %q, %v; want failed call followed by successful no-gate status", b, err)
	}
}

func TestWatchStopsAtDeliveryFailedBeforeAGate(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = 10 * time.Millisecond
	fakeStatus(t)
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery", "exit 1")),
		"rev-parse", "--show-toplevel")
	background(t, top)
	id, lines, _ := watching(t, top, "")
	o, err := Start(top, []string{"--watcher", id, issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	var event string
	for !strings.HasPrefix(event, "finished") {
		event = watchEvent(t, nextLine(t, lines), o.Run)
	}
	if event != "finished: failed at Delivery" {
		t.Fatalf("watcher printed %q", event)
	}
}

func TestWatchGateMakesNoStatusCallAfterDeliveryFailedBeforeAGate(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	count := fakeStatus(t)
	root := reported(t, `{"failed_node": "delivery"}`)
	ended := make(chan struct{})
	close(ended)
	watchGate(context.Background(), root, t.TempDir(), nil, "run", ended, func(_, event string) {
		t.Errorf("watcher printed %q", event)
	})
	if b, err := os.ReadFile(count); err == nil {
		t.Fatalf("%s status calls after delivery failed before a gate; want none", b)
	}
}

// A delivery command that ran may have started a gate before it failed: its status errors are retried.
func TestWatchGateRetriesAfterDeliveryCommandFailed(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	root := reported(t, `{"failed_node": "delivery", "commands": [{"name": "delivery", "result": "failed"}]}`)
	release := filepath.Join(t.TempDir(), "release")
	writeFixture(t, release, "")
	count := realStatus(t, release)
	none := filepath.Join(t.TempDir(), "none")
	writeFixture(t, none, "")
	t.Setenv("ATM_STATUS_LIVE", none)
	watchesToMergedAfterARetry(t, root, count)
}

// reported is a root whose run "run" has report, its report.json.
func reported(t *testing.T, report string) (root string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(runDir(root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(runDir(root, "run"), "report.json"), report)
	return root
}

// fakeStatus installs the fake no-mistakes and returns the file counting its calls.
func fakeStatus(t *testing.T) (count string) {
	t.Helper()
	installFakeNoMistakes(t)
	count = filepath.Join(t.TempDir(), "count")
	t.Setenv("ATM_STATUS_COUNT", count)
	return count
}

// realStatus has the fake no-mistakes answer with the testdata captures of a real run: running, an error, then
// parked until release exists, then completed. It returns the file counting its calls.
func realStatus(t *testing.T, release string) (count string) {
	t.Helper()
	t.Setenv("ATM_STATUS_RELEASE", release)
	for env, name := range map[string]string{"ATM_STATUS_LIVE": "running", "ATM_STATUS_WAITING": "parked",
		"ATM_STATUS_MERGED": "completed"} {
		path, err := filepath.Abs(filepath.Join("testdata", "atm_watch_status_"+name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(env, path)
	}
	return fakeStatus(t)
}

func TestWatchGatePrintsRealCompletedStatusAndRetriesAnErrorOnceItsGateRuns(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	release := filepath.Join(t.TempDir(), "release")
	writeFixture(t, release, "")
	watchesToMergedAfterARetry(t, t.TempDir(), realStatus(t, release))
}

// watchesToMergedAfterARetry watches the gate of run "run" in root and wants the real completed capture's lines
// after three status calls, counted in count: a first answer, an error retried, completed.
func watchesToMergedAfterARetry(t *testing.T, root, count string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var got []string
	watchGate(ctx, root, t.TempDir(), nil, "run", make(chan struct{}), func(_, event string) { got = append(got, event) })
	want := []string{"pr https://github.com/thellmwhisperer/agentic-team-member/pull/315", "ci green", "merged"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("watcher printed %q, want %q", got, want)
	}
	if b, err := os.ReadFile(count); err != nil || string(b) != "3" {
		t.Fatalf("status calls = %q, %v; want a first answer, the error retried, completed", b, err)
	}
}

// A real run parked for a decision, `awaiting_agent: parked`, is a gate waiting at its step.
func TestWatchGatePrintsRealParkedStatusAsWaiting(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	release := filepath.Join(t.TempDir(), "release")
	realStatus(t, release)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var got []string
	watchGate(ctx, t.TempDir(), t.TempDir(), nil, "run", make(chan struct{}), func(_, event string) {
		got = append(got, event)
		if event == "gate waiting: review" {
			writeFixture(t, release, "")
		}
	})
	want := []string{"gate waiting: review", "pr https://github.com/thellmwhisperer/agentic-team-member/pull/315",
		"ci green", "merged"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("watcher printed %q, want %q", got, want)
	}
}

// A step fixing its review findings on its own, not parked, waits for no one.
func TestParseGateFixReviewNotParkedIsNotWaiting(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "atm_watch_status_parked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if s := parseGate(strings.Replace(string(b), "awaiting_agent: parked", "awaiting_agent: none", 1)); s.waiting != "" {
		t.Fatalf("waiting = %q, want none", s.waiting)
	}
}

func TestWatchGateReadsPRStateAfterCloneRelease(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	top := repo(t, "https://github.com/o/r.git", atmYAML)
	args := fakeGH(t, "MERGED", false)
	branch := make(chan string, 1)
	branch <- "atm/fix-1"
	ended := make(chan struct{})
	close(ended)
	got := make(chan string, 1)
	watchGate(context.Background(), top, filepath.Join(top, "gone-clone"), branch, "run", ended,
		func(_, event string) { got <- event })
	if event := <-got; event != "merged" {
		t.Fatalf("event = %q; want merged", event)
	}
	b, err := os.ReadFile(args)
	if err != nil || string(b) != "pr view atm/fix-1 --repo o/r --json state --jq .state" {
		t.Fatalf("gh args = %q, %v", b, err)
	}
}

func TestWatchPrintsGateEventsFromAxiStatusAndSurvivesTransientError(t *testing.T) {
	t.Run("captured status drives watcher events", testGateStatusWatcherEvents)
}

func testGateStatusWatcherEvents(t *testing.T) {
	defer func(b, r time.Duration) { beat, remind = b, r }(beat, remind)
	beat, remind = 2*time.Millisecond, 15*time.Millisecond
	release := filepath.Join(t.TempDir(), "release")
	realStatus(t, release) // before repo, which leaves the package directory
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery", "true")),
		"rev-parse", "--show-toplevel")
	stop := background(t, top)
	t.Cleanup(stop)
	id, lines, _ := watching(t, top, "")
	if _, err := startWithWatcher(t, top, []string{"--watcher", id, issueFile(t, issue)}); err != nil {
		t.Fatal(err)
	}
	assertGateEventSequence(t, lines, release, top)
}

func assertGateEventSequence(t *testing.T, lines <-chan string, release, top string) {
	t.Helper()
	delivered := false
	readGateEvent := func() string {
		for {
			event := watchEvent(t, nextLine(t, lines), "issue-1")
			if event == "delivery" {
				delivered = true
			}
			if strings.HasPrefix(event, "lost:") {
				t.Fatalf("transient status error became a watcher event: %q", event)
			}
			if strings.HasPrefix(event, "gate ") || strings.HasPrefix(event, "still waiting") || event == "merged" {
				if !delivered {
					t.Fatalf("gate event %q preceded delivery", event)
				}
				return event
			}
		}
	}
	if got := readGateEvent(); got != "gate waiting: review" {
		t.Fatalf("first gate event %q", got)
	}
	if got := readGateEvent(); got != "still waiting 0m: review" {
		t.Fatalf("reminder event %q", got)
	}
	writeFixture(t, release, "")
	if got := readGateEvent(); got != "merged" {
		t.Fatalf("merged gate event %q", got)
	}
	if err := call(top, request{Cmd: "attach", Run: "issue-1"}, nil, func(reply) {}); err != nil {
		t.Fatal(err)
	}
}

func writeFixture(t *testing.T, path, content string, modes ...os.FileMode) {
	t.Helper()
	mode := os.FileMode(0o600)
	if len(modes) > 0 {
		mode = modes[0]
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// watcherProcess starts atm watch on root as a process of its own: it returns its id and what kills it.
func watcherProcess(t *testing.T, root string) (id string, kill func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "watch")
	cmd.Dir = root
	out, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		t.Fatal(err)
	}
	kill = func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }
	t.Cleanup(kill)
	sc := bufio.NewScanner(out)
	sc.Scan()
	return strings.TrimPrefix(sc.Text(), "watching as "), kill
}
