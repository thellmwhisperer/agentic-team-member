package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// background serves the repository at root, its git toplevel, in this test's process until stop or the end of
// the test.
func background(t *testing.T, root string) (stop func()) {
	t.Helper()
	sock := socket(root)
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", short(sock))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = serve(l.(*net.UnixListener), root, sock); close(done) }()
	stop = func() { _ = l.Close(); <-done }
	t.Cleanup(stop)
	return stop
}

// backgroundRepo is repo with atmYAML, served in the background; it returns its git toplevel.
func backgroundRepo(t *testing.T) string {
	t.Helper()
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmYAML), "rev-parse", "--show-toplevel")
	background(t, top)
	return top
}

func holdFakeAgents(t *testing.T, count int) (wait, release func()) {
	t.Helper()
	dir := t.TempDir()
	ready, unblock := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	if err := os.Mkdir(ready, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_AGENT_BARRIER_READY", ready)
	t.Setenv("FAKE_AGENT_BARRIER_RELEASE", unblock)
	release = func() { _ = os.WriteFile(unblock, nil, 0o600) }
	t.Cleanup(release)
	wait = func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			entries, err := os.ReadDir(ready)
			if err == nil && len(entries) >= count {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		entries, err := os.ReadDir(ready)
		t.Fatalf("only %d of %d fake agents reached the barrier: %v", len(entries), count, err)
	}
	return wait, release
}

func TestBackgroundRunsAndTellsHowEachEnded(t *testing.T) {
	for _, c := range []struct {
		name, agent, outcome, failed string
		code                         int
	}{
		{name: "passed", outcome: "passed"},
		{name: "agent fails", agent: "fail", outcome: "failed", failed: "agent", code: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			top := backgroundRepo(t)
			var wait, release func()
			if c.agent == "" {
				wait, release = holdFakeAgents(t, 1)
			}
			t.Setenv("FAKE_AGENT", c.agent)
			path := issueFile(t, issue)
			o, err := Start(top, []string{path})
			if err != nil || o.Outcome != "running" || o.Run != "issue-1" || o.Issue != path ||
				o.Report != filepath.Join(top, ".atm", "runs", o.Run, "report.json") {
				t.Fatalf("Start = %+v, %v", o, err)
			}
			if wait != nil {
				wait()
				runs, err := Runs(top)
				if err != nil || len(runs) != 1 || runs[0].Outcome != "running" || runs[0].Report != o.Report {
					t.Fatalf("running Runs = %+v, %v", runs, err)
				}
				release()
			}
			checkBackgroundRunEnd(t, top, o, c.outcome, c.failed, c.code)
		})
	}
}

func checkBackgroundRunEnd(t *testing.T, top string, o Outcome, outcome, failed string, code int) {
	t.Helper()
	var screen bytes.Buffer
	end, err := Attach(top, o.Run, &screen)
	if err != nil {
		t.Fatal(err)
	}
	got := []any{end.Outcome, end.Run, end.FailedNode, end.Report, ExitCode(end.Err()), end.NextStep != ""}
	want := []any{outcome, o.Run, failed, filepath.Join(top, ".atm", "runs", o.Run, "report.json"), code, true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outcome: %+v", end)
	}
	if s := screen.String(); !strings.Contains(s, "clone     passed") || !strings.Contains(s, "RESULT  ") {
		t.Fatalf("screen:\n%s", s)
	}
	runs, err := Runs(top)
	if err != nil || len(runs) != 1 || runs[0].Outcome != outcome || runs[0].Duration() == "" {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
}

// Two runs at once in one repository each write under .atm/runs/<label>/, sharing only what the background
// process owns. The same work passes as a fix and fails as docs.
func TestBackgroundRunsAtOnceEachInItsOwnDirectory(t *testing.T) {
	top := backgroundRepo(t)
	wait, release := holdFakeAgents(t, 2)
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	var started []Outcome
	for _, typ := range []string{"fix", "docs"} {
		o, err := Start(top, []string{issueFile(t, "# Retry\nType: "+typ+"\nRetry once.\n")})
		if err != nil {
			t.Fatal(err)
		}
		started = append(started, o)
	}
	wait()
	runs, err := Runs(top)
	if err != nil || len(runs) != len(started) {
		t.Fatalf("running Runs = %+v, %v", runs, err)
	}
	for i, o := range started {
		if runs[i].Outcome != "running" || runs[i].Run != o.Run ||
			runs[i].Report != filepath.Join(top, ".atm", "runs", o.Run, "report.json") {
			t.Fatalf("running run %d = %+v", i, runs[i])
		}
	}
	release()
	checkConcurrentBackgroundReports(t, top, started)
	entries, err := os.ReadDir(filepath.Join(top, ".atm"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !map[string]bool{"atm.sock": true, "runs.jsonl": true, "runs": true, "clones": true}[e.Name()] {
			t.Errorf("the runs share .atm/%s", e.Name())
		}
	}
}

func checkConcurrentBackgroundReports(t *testing.T, top string, started []Outcome) {
	t.Helper()
	for i, want := range [][]string{{"fix", "", "passed"}, {"docs", "checks", "failed"}} {
		end, err := Attach(top, started[i].Run, &bytes.Buffer{})
		dir := filepath.Join(top, ".atm", "runs", started[i].Run)
		if err != nil || end.Outcome != want[2] || end.Report != filepath.Join(dir, "report.json") {
			t.Fatalf("Attach = %+v, %v", end, err)
		}
		rep := readReport(t, end.Report)
		if got := []string{rep["type"].(string), rep["failed_node"].(string), end.Outcome}; !reflect.DeepEqual(got,
			want) {
			t.Fatalf("%s: report %v", end.Run, got)
		}
		if _, err := os.Stat(filepath.Join(dir, "brief.md")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBackgroundRunThatNeverStartedHasNoReport(t *testing.T) {
	root := repo(t, "https://example.com/owner/repo.git", "test: go test ./...\n")
	top := gitT(t, root, "rev-parse", "--show-toplevel")
	background(t, top)
	o, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	end, err := Attach(top, "", &bytes.Buffer{})
	if err != nil || end.Run != o.Run || end.Outcome != "failed" || end.FailedNode != "" || end.Report != "" ||
		!strings.Contains(end.Reason, "config incomplete") || ExitCode(end.Err()) != 2 {
		t.Fatalf("Attach = %+v, %v", end, err)
	}
}

// A start that finds .atm/atm.lock held serves nothing: it waits for the one that holds it to answer, here
// for idle, none. Once its holder dies, the lock blocks no one.
func TestServeLeavesTheRepositoryToTheStartThatHoldsTheLock(t *testing.T) {
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmYAML), "rev-parse", "--show-toplevel")
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := lock(filepath.Join(top, ".atm", "atm.lock"))
	if err != nil || held == nil {
		t.Fatalf("lock = %v, %v", held, err)
	}
	defer func(i, k time.Duration) { idle, tick = i, k }(idle, tick)
	idle, tick = 0, time.Millisecond // a start that serves ends at its first tick
	if err := Serve(top); err == nil {
		t.Fatal("Serve served while another start held the lock")
	}
	if err := held.Close(); err != nil { // as its holder's death does
		t.Fatal(err)
	}
	if err := Serve(top); err != nil {
		t.Fatalf("Serve after the holder died = %v", err)
	}
}

func TestStartRejectsABadCommandLineBeforeTheBackground(t *testing.T) {
	top := backgroundRepo(t)
	for _, args := range [][]string{{"--nope", "7"}, {}, {"7", "8"}} {
		if _, err := Start(top, args); err == nil {
			t.Fatalf("Start(%q) = nil, want an error", args)
		}
	}
	if runs, err := Runs(top); err != nil || len(runs) != 0 {
		t.Fatalf("Runs = %+v, %v; want none", runs, err)
	}
}

// A later start sweeps a clone after its inherited process lock has ended.
func TestBackgroundSweepsTheCloneOfARunThatDied(t *testing.T) {
	top := backgroundRepo(t)
	old := oldClone(t, top, false, false, false)
	o, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	if end, err := Attach(top, o.Run, &bytes.Buffer{}); err != nil || end.Outcome != "passed" {
		t.Fatalf("Attach = %+v, %v", end, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the dead run's clone outlived the sweep: %v", clones(t, top))
	}
}

func TestSweepKeepsOnlyClonesWithLiveLeases(t *testing.T) {
	root := filepath.Join(t.TempDir(), "clones")
	clone := func(name string, live bool) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if live {
			if err := os.WriteFile(dir+".lock", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := startTestCloneLease(dir); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	held, dropped := clone("held", true), clone("dropped", false)
	sweep(root, "")
	for dir, want := range map[string]bool{held: true, dropped: false} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Fatalf("%s kept = %v, want %v", filepath.Base(dir), err == nil, want)
		}
	}
	closeCloneLease(held)
	sweep(root, "")
	if _, err := os.Stat(held); !os.IsNotExist(err) {
		t.Fatalf("clone remained after its lease closed: %v", err)
	}
}

func TestBackgroundRemembersRunsAcrossRestarts(t *testing.T) {
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmYAML), "rev-parse", "--show-toplevel")
	stop := background(t, top)
	o, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Attach(top, o.Run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	stop()
	// A run the process before died in the middle of.
	f, err := os.OpenFile(filepath.Join(top, ".atm", "runs.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"outcome":"running","run":"7-2","issue":"7","step":"agent",` +
		`"started":"2026-10-07T05:00:00Z","ended":"0001-01-01T00:00:00Z"}` + "\n")
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	background(t, top)
	runs, err := Runs(top)
	var got [][]any
	for _, o := range runs {
		got = append(got, []any{o.Run, o.Outcome, o.FailedNode, o.Reason != "", o.Ended.IsZero()})
	}
	if want := [][]any{{"issue-1", "passed", "", false, false}, {"7-2", "failed", "agent", true, false}}; err != nil ||
		!reflect.DeepEqual(got, want) {
		t.Fatalf("Runs = %+v, %v", runs, err)
	}
	if o, err := Start(top, []string{"--harness", "pi", issueFile(t, issue)}); err != nil || o.Run != "issue-3" {
		t.Fatalf("Start = %+v, %v; want the next label", o, err)
	}
	if _, err := Attach(top, "", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

// The history keeps the last 200 runs: a new one drops the oldest that ended, never one still running.
func TestBackgroundKeepsTheLastRunsAndEveryRunning(t *testing.T) {
	top := backgroundRepo(t)
	wait, release := holdFakeAgents(t, 1)
	held, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	wait()
	missing := filepath.Join(t.TempDir(), "missing.md") // each run fails at its issue
	for range 201 {
		o, err := Start(top, []string{missing})
		if err == nil {
			_, err = Attach(top, o.Run, &bytes.Buffer{})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	runs, err := Runs(top)
	if err != nil || len(runs) != 200 || runs[0].Run != held.Run || runs[0].Outcome != "running" ||
		runs[1].Run != "missing-4" || runs[len(runs)-1].Run != "missing-202" {
		t.Fatalf("Runs = %d runs, first %+v, %v", len(runs), runs[:min(len(runs), 2)], err)
	}
	release()
	if end, err := Attach(top, held.Run, &bytes.Buffer{}); err != nil || end.Outcome != "passed" {
		t.Fatalf("Attach = %+v, %v", end, err)
	}
	if saved, err := history(top); err != nil || len(saved) != 200 {
		t.Fatalf("runs.jsonl holds %d runs, %v; want 200", len(saved), err)
	}
}

func TestHistoryReadsLargeRecordsAndReturnsScannerErrors(t *testing.T) {
	top := t.TempDir()
	atmDir := filepath.Join(top, ".atm")
	if err := os.Mkdir(atmDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(atmDir, "runs.jsonl")
	first, err := json.Marshal(Outcome{
		Outcome: "failed", Run: "large", Reason: strings.Repeat("x", 70*1024), Ended: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(Outcome{Outcome: "passed", Run: "later"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append(first, '\n'), append(second, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, err := history(top)
	if err != nil || len(runs) != 2 || len(runs[0].Reason) != 70*1024 || runs[1].Run != "later" {
		t.Fatalf("history = %d records, %v", len(runs), err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := history(top); err == nil {
		t.Fatal("history did not return the scanner's read error")
	}
}
