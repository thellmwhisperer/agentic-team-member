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
	if l := nextLine(t, gone); !regexp.MustCompile(`^WATCHER GONE at \d\d:\d\d:\d\d: no one is watching this run$`).
		MatchString(l) {
		t.Fatalf("screen line %q", l)
	}
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
	var report verdict
	b, err := os.ReadFile(filepath.Join(runDir(top, o.Run), "report.json"))
	if err != nil || json.Unmarshal(b, &report) != nil || report.Watcher != newID {
		t.Fatalf("report watcher = %q, %v; want %s", report.Watcher, err, newID)
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

func TestWatchGateRetriesFirstStatusErrorAfterRunEnds(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	bin := t.TempDir()
	count := filepath.Join(bin, "count")
	cmd := "#!/bin/sh\nn=0\ntest ! -f \"$ATM_STATUS_COUNT\" || n=$(cat \"$ATM_STATUS_COUNT\")\n" +
		"n=$((n + 1))\nprintf '%s' \"$n\" > \"$ATM_STATUS_COUNT\"\n" +
		"test \"$n\" -ne 1\n"
	if err := os.WriteFile(filepath.Join(bin, "no-mistakes"), []byte(cmd), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATM_STATUS_COUNT", count)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ended := make(chan struct{})
	close(ended)
	watchGate(context.Background(), t.TempDir(), t.TempDir(), nil, "run", ended, func(string, string) {})
	b, err := os.ReadFile(count)
	if err != nil || string(b) != "2" {
		t.Fatalf("status calls = %q, %v; want failed call followed by successful no-gate status", b, err)
	}
}

func TestWatchGateReadsPRStateAfterCloneRelease(t *testing.T) {
	defer func(b time.Duration) { beat = b }(beat)
	beat = time.Millisecond
	top := repo(t, "https://github.com/o/r.git", atmYAML)
	bin := t.TempDir()
	gh := "#!/bin/sh\nprintf '%s' \"$*\" > \"$ATM_GH_ARGS\"\nprintf MERGED\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(bin, "args")
	t.Setenv("ATM_GH_ARGS", args)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	defer func(b, r time.Duration) { beat, remind = b, r }(beat, remind)
	beat, remind = 2*time.Millisecond, 15*time.Millisecond
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery", "true")),
		"rev-parse", "--show-toplevel")
	stop := background(t, top)
	t.Cleanup(stop)
	bin := t.TempDir()
	waiting, merged, counter, release := filepath.Join(bin, "waiting"), filepath.Join(bin, "merged"),
		filepath.Join(bin, "count"), filepath.Join(bin, "release")
	const liveStatus = `run:
  id: "01M4T2F20FPSNC61KDJ6FYHBN8"
  branch: atm/go-port-no-watcher-no-run-atm-watch-neve-20261013-075250
  status: running
  head: aefb7d33
  head_sha: aefb7d3322800a162036bb5f5bfa36b4e27c1009
  verification_plan: none
  findings: "3 awaiting, 4 auto-fix"
  steps[9]{step,status,findings,duration_ms}:
    intent,completed,0,149
    rebase,completed,0,44095
    review,fixing,7,277544
    test,pending,0,0
    document,pending,0,0
    lint,pending,0,0
    push,pending,0,0
    pr,pending,0,0
    ci,pending,0,0
  active_steps[1]{step,status,active_for,round_active_for,last_activity,agent_pid,round}:
    review,fixing,9m29s,4m36s,"2s ago: codex producing output","90314",fix 1
`
	steps := func(rows ...string) string {
		return "steps[3]{step,status,findings,duration_ms}:\n    " + strings.Join(rows, "\n    ") + "\n"
	}
	if err := os.WriteFile(waiting, []byte(steps("review,awaiting_approval,1,1", "pr,pending,0,0", "ci,pending,0,0")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(merged, []byte("pr: \"https://github.com/o/r/pull/7\"\npr_state: merged\n"+
		steps("review,completed,0,1", "pr,completed,0,1", "ci,completed,0,1")), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := "#!/bin/sh\nn=0\ntest ! -f \"$ATM_STATUS_COUNT\" || n=$(cat \"$ATM_STATUS_COUNT\")\nn=$((n + 1))\nprintf '%s' \"$n\" > \"$ATM_STATUS_COUNT\"\ncase $n in\n1) cat \"$ATM_STATUS_LIVE\" ;;\n2) exit 1 ;;\n*) if test -f \"$ATM_STATUS_RELEASE\"; then cat \"$ATM_STATUS_MERGED\"; else cat \"$ATM_STATUS_WAITING\"; fi ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "no-mistakes"), []byte(cmd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "live"), []byte(liveStatus), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATM_STATUS_COUNT", counter)
	t.Setenv("ATM_STATUS_LIVE", filepath.Join(bin, "live"))
	t.Setenv("ATM_STATUS_WAITING", waiting)
	t.Setenv("ATM_STATUS_MERGED", merged)
	t.Setenv("ATM_STATUS_RELEASE", release)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	id, lines, _ := watching(t, top, "")
	if _, err := startWithWatcher(t, top, []string{"--watcher", id, issueFile(t, issue)}); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readGateEvent(); got != "merged" {
		t.Fatalf("merged gate event %q", got)
	}
	if err := call(top, request{Cmd: "attach", Run: "issue-1"}, nil, func(reply) {}); err != nil {
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
