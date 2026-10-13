package run

import (
	"bufio"
	"bytes"
	"context"
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
	defer func(b, k time.Duration) { beat, tick = b, k }(beat, tick)
	beat, tick = 10*time.Millisecond, 10*time.Millisecond
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
	if l := nextLine(t, gone); !regexp.MustCompile(`^WATCHER GONE at \d\d:\d\d:\d\d: no one is watching this run$`).
		MatchString(l) {
		t.Fatalf("screen line %q", l)
	}
	newID, lines, _ := watching(t, top, o.Run)
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
	var screen bytes.Buffer
	if _, err := Attach(top, o.Run, &screen); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(screen.String(), "WATCHER GONE"); n != 1 {
		t.Fatalf("WATCHER GONE %d times on the screen:\n%s", n, &screen)
	}
	if runs, err := Runs(top); err != nil || runs[0].Watcher != newID {
		t.Fatalf("Runs = %+v, %v; want the run bound to %s", runs, err, newID)
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

// The gate's lines come from no-mistakes axi status, polled: each once, a wait again every remind while it waits.
func TestGateLinesAndAWaitPrintedAgainEveryInterval(t *testing.T) {
	steps := func(rows ...string) string {
		return "current_branch: atm/x\nstatus: running\n  steps[3]{step,status,findings,duration_ms}:\n    " +
			strings.Join(rows, "\n    ") + "\noutcome: running\n"
	}
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	var g gate
	for _, c := range []struct {
		after  time.Duration
		status string
		want   []string
		done   bool
	}{
		{0, steps("review,running,0,0", "pr,pending,0,0", "ci,pending,0,0"), nil, false},
		{time.Minute, steps("review,awaiting_approval,2,10", "pr,pending,0,0", "ci,pending,0,0"),
			[]string{"gate waiting: review"}, false},
		{2 * time.Minute, steps("review,awaiting_approval,2,10", "pr,pending,0,0", "ci,pending,0,0"), nil, false},
		{11 * time.Minute, steps("review,awaiting_approval,2,10", "pr,pending,0,0", "ci,pending,0,0"),
			[]string{"still waiting 10m: review"}, false},
		{15 * time.Minute, steps("review,awaiting_approval,2,10", "pr,pending,0,0", "ci,pending,0,0"), nil, false},
		{21 * time.Minute, steps("review,awaiting_approval,2,10", "pr,pending,0,0", "ci,pending,0,0"),
			[]string{"still waiting 20m: review"}, false},
		{22 * time.Minute, `pr: "https://github.com/o/r/pull/7"` + "\n" +
			steps("review,completed,0,10", "pr,completed,0,10", "ci,fixing,1,0"),
			[]string{"pr https://github.com/o/r/pull/7", "gate fixing CI"}, false},
		{23 * time.Minute, `pr: "https://github.com/o/r/pull/7"` + "\n" +
			steps("review,completed,0,10", "pr,completed,0,10", "ci,completed,0,10"), []string{"ci green"}, false},
		{24 * time.Minute, `pr: "https://github.com/o/r/pull/7"` + "\npr_state: merged\n" +
			steps("review,completed,0,10", "pr,completed,0,10", "ci,completed,0,10"), []string{"merged"}, true},
	} {
		got, done := g.events(c.status, at.Add(c.after))
		if !reflect.DeepEqual(got, c.want) || done != c.done {
			t.Fatalf("at +%s: %q, done %v; want %q, done %v", c.after, got, done, c.want, c.done)
		}
	}
	var failed gate
	if got, done := failed.events(steps("review,completed,0,1", "lint,failed,1,1", "ci,pending,0,0"), at); !done ||
		!reflect.DeepEqual(got, []string{"gate failed: lint"}) {
		t.Fatalf("failed gate: %q, done %v", got, done)
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
