//go:build unix

package run

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunRemovesWorkerScratch(t *testing.T) {
	builtATM(t)
	top := gitT(t, repo(t, "local", atmYAML+"e2e: test -f reached\n"), "rev-parse", "--show-toplevel")
	bin := filepath.Join(t.TempDir(), "atm")
	buildATM(t, bin, "follow-test")
	path := issueFile(t, issue)
	runScratch := func(label string) string { return filepath.Join(runDir(top, label), "worker-scratch") }
	t.Cleanup(func() {
		scratches, _ := filepath.Glob(runScratch("*"))
		for _, scratch := range scratches {
			writable(scratch)
		}
	})
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	hold, green, h := workerScratchControls(t)
	server := startVersionServe(t, bin, top)
	defer func() {
		if server != nil {
			endServe(bin, top, server)
		}
	}()
	passed, stopped := passedAndStoppedScratchRuns(t, bin, top, path, hold, green, h)
	if err := os.Remove(green); err != nil {
		t.Fatal(err)
	}
	failed := runWithRetainedScratch(t, bin, top, path)
	recent := runWithRetainedScratch(t, bin, top, path)
	exist(t, runScratch(failed), runScratch(recent))
	if err := os.Symlink("../resume", filepath.Join(runScratch(failed), "kept-work")); err != nil {
		t.Fatal(err)
	}
	saved := savedFiles(t, top, failed)
	endServe(bin, top, server)
	server = nil
	prepareScratchHistory(t, top, failed, recent, passed, stopped)
	server = startVersionServe(t, bin, top)
	write(t, green, "pass")
	followedRun(t, bin, top, 0, "--harness", "claude", path)
	assertSweptScratchRuns(t, top, failed, recent, passed, stopped)
	if !maps.Equal(savedFiles(t, top, failed), saved) {
		t.Fatal("sweeping worker-scratch changed the failed run's report or resume bundle")
	}
	followedRun(t, bin, top, 0, "--resume", failed)
	assertScratchRemoved(t, runScratch(lastRun(t, top, "passed")))
}

func workerScratchControls(t *testing.T) (hold, green string, h heldPIDs) {
	t.Helper()
	dir := t.TempDir()
	hold, green = filepath.Join(dir, "hold"), filepath.Join(dir, "green")
	t.Setenv("SCRATCH_HOLD", hold)
	t.Setenv("SCRATCH_GREEN", green)
	t.Setenv("FAKE_AGENT_STREAM", `{"session_id":"sess-1"}`+"\n")
	h = holdOnFIFO(t)
	t.Setenv("FAKE_AGENT_WORK", `mkdir -p "$TMPDIR/nested/.atm/clones" &&
touch "$TMPDIR/nested/.atm/clones/cache" &&
chmod 000 "$TMPDIR/nested/.atm/clones" && chmod 555 "$TMPDIR/nested/.atm" || exit 1
if test -f "$SCRATCH_HOLD"; then `+h.hold+`; fi
`+fixWork+`
if test -f "$SCRATCH_GREEN"; then touch reached; fi`)
	return hold, green, h
}

func passedAndStoppedScratchRuns(t *testing.T, bin, top, path, hold, green string, h heldPIDs) (string, string) {
	t.Helper()
	write(t, hold, "hold")
	stopping := startFollow(t, bin, top, false, "run", "--follow", "--harness", "claude", path)
	waitFor(t, "the worker to create its restricted scratch before stopping", func() bool { return len(h.held(t)) == 1 })
	if err := os.Remove(hold); err != nil {
		t.Fatal(err)
	}
	write(t, green, "pass")
	followedRun(t, bin, top, 0, "--harness", "claude", path)
	passed := lastRun(t, top, "passed")
	assertScratchRemoved(t, filepath.Join(runDir(top, passed), "worker-scratch"))
	stopped := "issue-1"
	scratch := filepath.Join(runDir(top, stopped), "worker-scratch")
	exist(t, scratch)
	if out, err := stopOf(bin, top, stopped); err != nil {
		t.Fatalf("atm stop %s: %v\n%s", stopped, err, out)
	}
	stopping.exit(1)
	stoppedReport(t, top, stopped, "agent")
	assertScratchRemoved(t, scratch)
	return passed, stopped
}

func assertSweptScratchRuns(t *testing.T, top, failed, recent, passed, stopped string) {
	t.Helper()
	runScratch := func(label string) string { return filepath.Join(runDir(top, label), "worker-scratch") }
	for _, label := range []string{failed, passed, stopped, "archived-failed", "archived-passed", "archived-stopped"} {
		assertScratchRemoved(t, runScratch(label))
		exist(t, filepath.Join(runDir(top, label), "scratch-cleanup.log"))
	}
	exist(t, runScratch(recent), runScratch("archived-unfinished"), runScratch("archived-waiting"),
		runScratch("archived-no-report"), runDir(top, "sidecar"))
	for _, label := range []string{failed, passed, stopped} {
		dir := runDir(top, label)
		exist(t, filepath.Join(dir, "report.json"), filepath.Join(dir, "worker.jsonl"), filepath.Join(dir, "brief.md"))
	}
}

func assertScratchRemoved(t *testing.T, scratch string) {
	t.Helper()
	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Fatalf("want worker-scratch %s removed, stat error: %v", scratch, err)
	}
}

func runWithRetainedScratch(t *testing.T, bin, top, path string) string {
	t.Helper()
	out, err := versionLaunch(bin, top, path)
	if err != nil {
		t.Fatalf("launch failed-run fixture: %v\n%s", err, out)
	}
	label := string(bytes.TrimSpace(out))
	waitFor(t, "failed checks to ask for an answer", func() bool { return runOf(t, top, label).Waits() })
	if end := stopCheckRun(t, top, label); end.FailedNode != "checks" {
		t.Fatalf("want failed checks so the run can resume, got %+v", end)
	}
	return label
}

func prepareScratchHistory(t *testing.T, top, failed, recent, passed, stopped string) {
	t.Helper()
	// The previous binary left scratch even in passed and stopped runs. Simulate the history trimming
	// some older outcomes, with only their report and its timestamp remaining.
	now := time.Now()
	old := now.Add(-169 * time.Hour)
	runs, err := RecordedRuns(top)
	if err != nil {
		t.Fatal(err)
	}
	history := &bytes.Buffer{}
	for _, r := range runs {
		switch r.Run {
		case failed:
			r.Ended = old
		case recent:
			r.Ended = now.Add(-167 * time.Hour)
		}
		if err := json.NewEncoder(history).Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(top, ".atm", "runs.jsonl"), history.String())
	legacyScratch := map[string]string{
		passed: "", stopped: "", "archived-failed": failed, "archived-passed": passed,
		"archived-stopped": "", "archived-unfinished": "", "archived-waiting": "", "archived-no-report": "",
	}
	for label, from := range legacyScratch {
		dir := runDir(top, label)
		scratch := filepath.Join(dir, "worker-scratch")
		if err := os.MkdirAll(scratch, 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(scratch, "cache"), "legacy scratch")
		if from != "" {
			b, err := os.ReadFile(filepath.Join(runDir(top, from), "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(dir, "report.json")
			write(t, report, string(b))
			if err := os.Chtimes(report, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(t, runDir(top, "sidecar"), "keep this file")
	write(t, filepath.Join(runDir(top, "archived-stopped"), "report.json"), `{"reason":"stopped by operator"}`)
	write(t, filepath.Join(runDir(top, "archived-unfinished"), "report.json"),
		`{"nodes":[{"name":"checks","result":"running"}]}`)
	write(t, filepath.Join(runDir(top, "archived-waiting"), "report.json"),
		`{"failed_node":"checks","reason":"stopped by operator","decisions":[{"answer":""}]}`)
}
