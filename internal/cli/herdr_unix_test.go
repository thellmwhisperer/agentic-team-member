//go:build unix

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeHerdr puts a herdr first on PATH that appends its arguments, one call a line, to the file it returns, and
// exits with code.
func fakeHerdr(t *testing.T, code string) string {
	t.Helper()
	bin, calls := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\nprintf '%s|' \"$@\" >> " + calls + "\necho >> " + calls + "\nexit " + code + "\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_BIN_PATH", "")
	return calls
}

var seq = regexp.MustCompile(`\|--seq\|\d+`)

// herdrCalls is each call the fake herdr got, without its --seq, which must be there.
func herdrCalls(t *testing.T, calls string) []string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		if !seq.MatchString(l) {
			t.Fatalf("a call without --seq: %q", l)
		}
		got = append(got, seq.ReplaceAllString(l, ""))
	}
	return got
}

func TestEveryRunOnTheScreenShowsInHerdr(t *testing.T) {
	resumeRepo(t, "", "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("HERDR_PANE_ID", "wX:p1")

	t.Setenv("HERDR_ENV", "")
	calls := fakeHerdr(t, "0")
	if _, code := onTerminal(t, "run", "chore.md"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := herdrCalls(t, calls); len(got) != 0 {
		t.Fatalf("outside herdr, ATM called herdr: %q", got)
	}

	t.Setenv("HERDR_ENV", "1")
	const report = "pane|report-agent|wX:p1|--source|atm|--agent|atm|--state|"
	steps := func(label, start string) []string {
		return []string{
			"pane|report-agent-session|wX:p1|--source|atm|--agent|atm|--agent-session-id|" + label +
				"|--session-start-source|" + start + "|",
			report + "working|--message|" + label + "|",
			report + "working|--message|" + label + " Chore|",
			report + "working|--message|" + label + " Clone|",
			report + "working|--message|" + label + " Contract|",
			report + "working|--message|" + label + " Agent · unit 1|",
			report + "working|--message|" + label + " Checks|",
			report + "working|--message|" + label + " Slop detector|",
			report + "idle|--message|" + label + " passed|",
			"pane|release-agent|wX:p1|--source|atm|--agent|atm|",
		}
	}
	if _, code := onTerminal(t, "run", "chore.md"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got, want := herdrCalls(t, calls), steps("chore-2", "startup"); !reflect.DeepEqual(got, want) {
		t.Fatalf("herdr got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	if _, code := onTerminal(t, "attach", "chore-2"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got, want := herdrCalls(t, calls), steps("chore-2", "resume"); !reflect.DeepEqual(got, want) {
		t.Fatalf("herdr got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	for i, env := range []struct{ noColor, term string }{{"1", "xterm-256color"}, {"", "dumb"}} {
		label := fmt.Sprintf("chore-%d", i+3)
		t.Setenv("NO_COLOR", env.noColor)
		t.Setenv("TERM", env.term)
		calls = fakeHerdr(t, "0")
		if _, code := onTerminal(t, "run", "chore.md"); code != 0 {
			t.Fatalf("plain run exit %d", code)
		}
		if got, want := herdrCalls(t, calls), steps(label, "startup"); !reflect.DeepEqual(got, want) {
			t.Fatalf("plain run herdr calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if err := os.Remove(calls); err != nil {
			t.Fatal(err)
		}
		if _, code := onTerminal(t, "attach", label); code != 0 {
			t.Fatalf("plain attach exit %d", code)
		}
		if got, want := herdrCalls(t, calls), steps(label, "resume"); !reflect.DeepEqual(got, want) {
			t.Fatalf("plain attach herdr calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}

	calls = fakeHerdr(t, "1")
	if shown, code := onTerminal(t, "run", "chore.md"); code != 0 || !strings.Contains(shown, "passed") {
		t.Fatalf("a failing herdr changed the run: exit %d, the terminal showed %q", code, shown)
	}
	if got := herdrCalls(t, calls); len(got) != 10 {
		t.Fatalf("herdr got %d calls, want 10: %q", len(got), got)
	}
}

func TestAttachWithoutRunsDoesNotReportHerdr(t *testing.T) {
	dir := repo(t, "https://example.com/o/r.git")
	cleanBackgroundServer(t, dir)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "wX:p1")
	calls := fakeHerdr(t, "0")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	_, _ = onTerminal(t, "attach")
	t.Setenv("NO_COLOR", "1")
	_, _ = onTerminal(t, "attach")
	if got := herdrCalls(t, calls); len(got) != 0 {
		t.Fatalf("attach without a run called herdr: %q", got)
	}
}

func TestDefaultAttachKeepsSelectedRunWhenAnotherStarts(t *testing.T) {
	repoDir, _ := resumeRepo(t, "", "")
	if err := os.WriteFile(filepath.Join(repoDir, "second.md"), []byte("# Second\nType: chore\nDo it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsReady, agentRelease := setupBarrierAgent(t)
	startRun := func(task string) string {
		t.Helper()
		c := root()
		c.SetArgs([]string{"run", task})
		var out strings.Builder
		c.SetOut(&out)
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String())
	}
	firstRun := startRun("chore.md")
	waitForAgents(t, agentsReady, 1)

	dir := t.TempDir()
	herdrReady, herdrRelease, calls := filepath.Join(dir, "ready"), filepath.Join(dir, "unblock"), filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "herdr")
	script := "#!/bin/sh\nif [ \"$2\" = report-agent-session ]; then : > \"$HERDR_READY\"; while [ ! -e \"$HERDR_RELEASE\" ]; do sleep 0.01; done; fi\nprintf '%s|' \"$@\" >> \"$HERDR_LOG\"; echo >> \"$HERDR_LOG\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "wX:p1")
	t.Setenv("HERDR_BIN_PATH", bin)
	t.Setenv("HERDR_READY", herdrReady)
	t.Setenv("HERDR_RELEASE", herdrRelease)
	t.Setenv("HERDR_LOG", calls)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	result := make(chan struct {
		shown string
		code  int
	}, 1)
	go func() {
		shown, code := onTerminal(t, "attach")
		result <- struct {
			shown string
			code  int
		}{shown, code}
	}()
	deadline := time.Now().Add(800 * time.Millisecond)
	for {
		if _, err := os.Stat(herdrReady); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attach did not report its selected session")
		}
		time.Sleep(5 * time.Millisecond)
	}
	secondRun := startRun("second.md")
	waitForAgents(t, agentsReady, 2)
	if err := os.WriteFile(herdrRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	assertBothRunsFinish(t, agentRelease)
	got := <-result
	if got.code != 1 || !strings.Contains(got.shown, "Chore") || strings.Contains(got.shown, "Second") {
		t.Fatalf("attach followed the wrong run: exit %d, shown %q", got.code, got.shown)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--agent-session-id|"+firstRun+"|") || firstRun == secondRun {
		t.Fatalf("herdr reported a different selected run: %q", b)
	}
}

func waitForAgents(t *testing.T, ready string, count int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		entries, err := os.ReadDir(ready)
		if err == nil && len(entries) >= count {
			return
		}
	}
	entries, _ := os.ReadDir(ready)
	t.Fatalf("only %d fake agents reached the barrier, want %d", len(entries), count)
}

func TestRunReportsWorkingBeforeIssueLoadingFails(t *testing.T) {
	dir := repo(t, "https://example.com/o/r.git")
	cleanBackgroundServer(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "retry.md"), []byte("# Retry\nType: fix\nRetry.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "wX:p1")
	calls := fakeHerdr(t, "0")
	if _, code := onTerminal(t, "run", "retry.md"); code != 2 {
		t.Fatalf("exit %d, want config failure", code)
	}
	got := herdrCalls(t, calls)
	if len(got) != 4 || !strings.Contains(got[0], "report-agent-session") ||
		!strings.Contains(got[1], "working|--message|retry-1|") ||
		!strings.Contains(got[2], "idle|--message|retry-1 failed at start|") ||
		!strings.Contains(got[3], "release-agent") {
		t.Fatalf("herdr calls before issue loading failed: %q", got)
	}
}
