//go:build unix

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

type followTabRun struct {
	out, err, dir string
	code          int
	calls         []string
	closedEarly   bool
}

func TestFollowTabClosesAfterMerge(t *testing.T) {
	got := runFollowTab(t, followTabATM("\"\"", "\"true\""), "ATM", "1", 0, "chore.md")
	if got.code != 0 || lastEvent(t, got.out) != "merged" {
		t.Fatalf("exit %d, last %q, want merged; stderr:\n%s\nstdout:\n%s", got.code, lastEvent(t, got.out),
			got.err, got.out)
	}
	assertTabLife(t, got, "chore.md", "chore-1")
}

func TestFollowTabClosesAfterRunFailed(t *testing.T) {
	got := runFollowTab(t, followTabATM("exit 1", "\"\""), "ATM", "1", 0, "chore.md")
	if got.code != 1 || lastEvent(t, got.out) != "run_failed" {
		t.Fatalf("exit %d, last %q, want run_failed; stderr:\n%s\nstdout:\n%s", got.code, lastEvent(t, got.out),
			got.err, got.out)
	}
	assertTabLife(t, got, "chore.md", "chore-1")
}

func TestFollowTabDeliveryUsesItsRootPane(t *testing.T) {
	for _, existingServer := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_server_%t", existingServer), func(t *testing.T) {
			delivered := filepath.Join(t.TempDir(), "delivery-panes")
			command := "printf '%s\\n' \"$HERDR_PANE_ID\" >> " + strconvQuote(delivered)
			dir := followTabRepo(t, followTabATM("\"\"", fmt.Sprintf("%q", command)))
			calls := followTabFakes(t, 0, "ATM")
			writeWorkspace(t, "ATM")
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("HERDR_PANE_ID", "wA:p1")
			t.Setenv("HERDR_BIN_PATH", "")
			if existingServer {
				if _, err := run.Runs(dir); err != nil {
					t.Fatal(err)
				}
				t.Setenv("HERDR_PANE_ID", "wB:p3")
			}
			out, err := atm(t, "run", "--follow", "chore.md")
			if err != nil || lastEvent(t, out) != "merged" {
				t.Fatalf("follow: %v\n%s", err, out)
			}
			if got := read(t, delivered); got != "wM:p9\n" {
				t.Fatalf("delivery pane = %q, want tab root wM:p9", got)
			}
			assertTabLife(t, followTabRun{dir: dir, calls: followTabCalls(t, calls)}, "chore.md", "chore-1")
			writeWorkspace(t, "")
			out, err = atm(t, "run", "--follow", "chore.md")
			if err != nil || lastEvent(t, out) != "merged" {
				t.Fatalf("follow without tab: %v\n%s", err, out)
			}
			if got := read(t, delivered); got != "wM:p9\nwA:p1\n" {
				t.Fatalf("delivery panes = %q, tab pane must belong only to its run", got)
			}
		})
	}
}

func TestFollowTabLabelUsesParsedIssue(t *testing.T) {
	for _, tc := range []struct {
		issue string
		args  []string
	}{
		{"42", []string{"--follow", "-model", "fast", "42"}},
		{"-chore.md", []string{"--follow", "--", "-chore.md"}},
	} {
		t.Run(tc.issue, func(t *testing.T) {
			dir := followTabRepo(t, followTabATM("exit 1", "\"\""))
			if err := os.WriteFile(
				filepath.Join(dir, "-chore.md"), []byte("# Chore\nType: chore\nDo it.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			calls := followTabFakes(t, 0, "ATM")
			writeWorkspace(t, "ATM")
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("HERDR_BIN_PATH", "")
			out, err := atm(t, append([]string{"run"}, tc.args...)...)
			if run.ExitCode(err) != 1 || lastEvent(t, out) != "run_failed" {
				t.Fatalf("follow: %v\n%s", err, out)
			}
			create := callWith(followTabCalls(t, calls), "tab|create|")
			if !strings.Contains(create, "--label|atm-"+tc.issue+"|") {
				t.Fatalf("tab create = %q, want label atm-%s", create, tc.issue)
			}
		})
	}
}

// TestFollowTabCreatesMissingWorkspace: herdr lists no workspace with the configured label, so --follow
// creates one with that label and the run's tab is its root pane's tab, labelled atm-<issue>.
func TestFollowTabCreatesMissingWorkspace(t *testing.T) {
	dir := followTabRepo(t, followTabATM("exit 1", "\"\""))
	calls := followTabFakes(t, 0, "")
	writeWorkspace(t, "ATM")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", "")
	stop := captureStderr(t)
	out, err := atm(t, "run", "--follow", "chore.md")
	stderr := stop()
	if run.ExitCode(err) != 1 || lastEvent(t, out) != "run_failed" {
		t.Fatalf("follow: %v\n%s\nstderr:\n%s", err, out, stderr)
	}
	if stderr != "" {
		t.Fatalf("creating the workspace warned: %q", stderr)
	}
	all := followTabCalls(t, calls)
	create := callWith(all, "workspace|create|")
	if !strings.Contains(create, "--label|ATM|") || !strings.Contains(create, "--cwd|"+dir+"|") ||
		!strings.Contains(create, "--no-focus|") {
		t.Fatalf("workspace create: %q", create)
	}
	if label := callWith(all, "tab|label|"); label != "tab|label|wN:t1|atm-chore.md|" {
		t.Fatalf("the created workspace's root tab is not the run's tab: %q", all)
	}
	if pane := callWith(all, "pane|run|"); !strings.Contains(pane, "wN:p1|atm attach chore-1") {
		t.Fatalf("the screen did not run in the created workspace's root pane: %q", all)
	}
	if close := callWith(all, "tab|close|"); close != "tab|close|wN:t1|" {
		t.Fatalf("tab close: %q", all)
	}
	if strings.Contains(strings.Join(all, "\n"), "tab|create|") {
		t.Fatalf("a tab was created instead of the root tab relabelled: %q", all)
	}
}

// TestFollowTabWorkspaceCreateRefusedLeavesTheRun: herdr refuses workspace creation, so the run starts with the
// warning naming herdr's error and no tab, as with any other herdr failure.
func TestFollowTabWorkspaceCreateRefusedLeavesTheRun(t *testing.T) {
	base := runFollowTab(t, followTabATM("exit 1", "\"\""), "", "1", 0, "chore.md")
	if base.code != 1 || lastEvent(t, base.out) != "run_failed" {
		t.Fatalf("baseline exit %d, last %q; stderr:\n%s", base.code, lastEvent(t, base.out), base.err)
	}
	followTabRepo(t, followTabATM("exit 1", "\"\""))
	calls := followTabFakes(t, 0, "")
	writeWorkspace(t, "ATM")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_BIN_PATH", "")
	t.Setenv("REFUSE_CREATE", "1")
	stop := captureStderr(t)
	out, err := atm(t, "run", "--follow", "chore.md")
	stderr := stop()
	if run.ExitCode(err) != base.code || !slices.Equal(eventsOf(t, out), eventsOf(t, base.out)) {
		t.Fatalf("follow changed: %v\n%s\nwant events %v\nstderr:\n%s\nbaseline stderr:\n%s",
			err, out, eventsOf(t, base.out), stderr, base.err)
	}
	if warn := herdrStderr(stderr, base.err); warn == "" || !strings.Contains(warn, "refused") {
		t.Fatalf("want the warning to name herdr's error, got %q (baseline %q)", stderr, base.err)
	}
	got := followTabCalls(t, calls)
	if callWith(got, "workspace|create|") == "" {
		t.Fatalf("workspace create was not tried: %q", got)
	}
	if strings.Contains(strings.Join(got, "\n"), "tab|close|") {
		t.Fatalf("a refused create closed a tab: %q", got)
	}
}

func TestFollowTabSkipsHerdrUnlessConfigured(t *testing.T) {
	atm := followTabATM("exit 1", "\"\"")
	for _, c := range []struct{ name, env, workspace string }{
		{"empty workspace", "1", ""},
		{"HERDR_ENV unset", "", "ATM"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := runFollowTab(t, atm, c.workspace, c.env, 0, "chore.md")
			if got.code != 1 || lastEvent(t, got.out) != "run_failed" {
				t.Fatalf("exit %d, last %q; stderr:\n%s", got.code, lastEvent(t, got.out), got.err)
			}
			if len(got.calls) != 0 {
				t.Fatalf("herdr was called: %q", got.calls)
			}
		})
	}
}

func TestFollowTabHerdrFailureLeavesTheRun(t *testing.T) {
	atm := followTabATM("exit 1", "\"\"")
	base := runFollowTab(t, atm, "", "1", 0, "chore.md")
	if base.code != 1 || lastEvent(t, base.out) != "run_failed" {
		t.Fatalf("baseline exit %d, last %q; stderr:\n%s", base.code, lastEvent(t, base.out), base.err)
	}
	for _, c := range []struct {
		name, workspace string
		code            int
	}{
		{"herdr exits 1", "ATM", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := runFollowTab(t, atm, c.workspace, "1", c.code, "chore.md")
			if got.code != base.code || !slices.Equal(eventsOf(t, got.out), eventsOf(t, base.out)) {
				t.Fatalf("follow changed: exit %d want %d\nevents %v\nwant %v\nstdout:\n%s\nstderr:\n%s",
					got.code, base.code, eventsOf(t, got.out), eventsOf(t, base.out), got.out, got.err)
			}
			if extra := herdrStderr(got.err, base.err); extra == "" {
				t.Fatalf("want one extra herdr stderr line, got %q (baseline %q)", got.err, base.err)
			}
			if strings.Contains(strings.Join(got.calls, "\n"), "tab|close|") {
				t.Fatalf("a failed herdr closed a tab: %q", got.calls)
			}
		})
	}
}

func assertTabLife(t *testing.T, got followTabRun, issue, runID string) {
	t.Helper()
	if got.closedEarly {
		t.Fatal("tab close ran while a follow line was still being written")
	}
	create := callWith(got.calls, "tab|create|")
	if !strings.Contains(create, "--label|atm-"+issue+"|") || !strings.Contains(create, "--workspace|wM|") ||
		!strings.Contains(create, "--no-focus|") || !strings.Contains(create, "--cwd|"+got.dir+"|") {
		t.Fatalf("tab create: %q", create)
	}
	if !strings.Contains(strings.Join(got.calls, "\n"), "workspace|list|") {
		t.Fatalf("workspace list was not called: %q", got.calls)
	}
	if pane := callWith(got.calls, "pane|run|"); !strings.Contains(pane, "wM:p9|atm attach "+runID) {
		t.Fatalf("pane run: %q", got.calls)
	}
	if close := callWith(got.calls, "tab|close|"); close != "tab|close|wM:t9|" {
		t.Fatalf("tab close: %q", close)
	}
	if slices.IndexFunc(got.calls, func(l string) bool { return strings.Contains(l, "tab|create|") }) >
		slices.IndexFunc(got.calls, func(l string) bool { return strings.Contains(l, "tab|close|") }) {
		t.Fatalf("tab close before tab create: %q", got.calls)
	}
}

func callWith(calls []string, part string) string {
	for _, l := range calls {
		if strings.Contains(l, part) {
			return l
		}
	}
	return ""
}

func followTabATM(lint, delivery string) string {
	return "harness: codex\ninstall: \"\"\ntest: \"\"\ntypecheck: \"\"\nlint: " + lint + "\n" +
		"test_file: \"\"\ntest_patterns: []\ndocs_patterns: []\ndelivery: " + delivery + "\n"
}

func runFollowTab(t *testing.T, atm, workspace, herdrEnv string, herdrCode int, issue string) followTabRun {
	t.Helper()
	dir := followTabRepo(t, atm)
	calls := followTabFakes(t, herdrCode, "ATM")
	if workspace != "" {
		writeWorkspace(t, workspace)
	}
	t.Setenv("HERDR_ENV", herdrEnv)
	t.Setenv("HERDR_BIN_PATH", "")
	out := &followTabWatch{calls: calls}
	stop := captureStderr(t)
	c := root()
	c.SetArgs([]string{"run", "--follow", issue})
	c.SetOut(out)
	err := c.Execute()
	stderr := stop()
	return followTabRun{out: out.String(), err: stderr, dir: dir, code: run.ExitCode(err),
		calls: followTabCalls(t, calls), closedEarly: out.closedEarly}
}

func followTabRepo(t *testing.T, atm string) string {
	t.Helper()
	dir, _ := resumeRepo(t, "", "")
	// Match git's physical root, including macOS's /var -> /private/var symlink.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte(atm), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "remote", "set-url", "origin", "https://github.com/o/r.git")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote: %v\n%s", err, out)
	}
	return dir
}

func writeWorkspace(t *testing.T, label string) {
	t.Helper()
	path := filepath.Join(os.Getenv("HOME"), ".config", "atm", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("herdr_workspace: "+label+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func followTabFakes(t *testing.T, code int, listed string) string {
	t.Helper()
	bin := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	status := "steps[1]{step,status,findings,duration_ms}:\n  ci,completed,0,1\npr: \"https://github.com/o/r/pull/1\"\n"
	gh := `{"url":"https://github.com/o/r/pull/1","state":"MERGED","commits":[]}`
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s|' \"$@\" >> %s\necho >> %s\n"+
		"if [ \"$1\" = workspace ] && [ \"$2\" = list ]; then\n"+
		"  echo '{\"id\":\"cli:workspace:list\",\"result\":{\"type\":\"workspace_list\",\"workspaces\":"+
		"[{\"workspace_id\":\"wM\",\"label\":\"%s\",\"number\":1}]}}'\nfi\n"+
		"if [ \"$1\" = tab ] && [ \"$2\" = create ]; then\n"+
		"  echo '{\"id\":\"cli:tab:create\",\"result\":{\"type\":\"tab_created\",\"tab\":{\"tab_id\":\"wM:t9\"},"+
		"\"root_pane\":{\"pane_id\":\"wM:p9\"}}}'\nfi\n"+
		"if [ \"$1\" = workspace ] && [ \"$2\" = create ]; then\n"+
		"  if [ -n \"$REFUSE_CREATE\" ]; then echo 'workspace create: refused' >&2; exit 7; fi\n"+
		"  echo '{\"id\":\"cli:workspace:create\",\"result\":{\"type\":\"workspace_created\","+
		"\"tab\":{\"tab_id\":\"wN:t1\"},\"root_pane\":{\"pane_id\":\"wN:p1\"}}}'\nfi\nexit %d\n",
		strconvQuote(calls), strconvQuote(calls), listed, code)
	writeFake(t, filepath.Join(bin, "herdr"), script)
	writeFake(t, filepath.Join(bin, "gh"), "#!/bin/sh\nprintf '%s\\n' '"+gh+"'\n")
	writeFake(t, filepath.Join(bin, "no-mistakes"), "#!/bin/sh\nprintf '%s\\n' '"+status+"'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func strconvQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func writeFake(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func followTabCalls(t *testing.T, calls string) []string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			got = append(got, l)
		}
	}
	return got
}

type followTabWatch struct {
	bytes.Buffer
	calls       string
	closedEarly bool
}

func (w *followTabWatch) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"event":`)) && strings.Contains(readCalls(w.calls), "tab|close|") {
		w.closedEarly = true
	}
	return w.Buffer.Write(p)
}

func readCalls(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	return func() string {
		os.Stderr = old
		_ = w.Close()
		<-done
		_ = r.Close()
		return buf.String()
	}
}

func eventsOf(t *testing.T, out string) []string {
	t.Helper()
	var evs []string
	for _, l := range nonEmptyLines(out) {
		var ev struct{ Event string }
		if json.Unmarshal([]byte(l), &ev) != nil || ev.Event == "" {
			t.Fatalf("not a follow line: %q", l)
		}
		evs = append(evs, ev.Event)
	}
	return evs
}

func lastEvent(t *testing.T, out string) string {
	t.Helper()
	evs := eventsOf(t, out)
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1]
}

// herdrStderr is the one extra line in got that names herdr, or "" when stderr is not baseline plus that line.
func herdrStderr(got, base string) string {
	extra := difference(nonEmptyLines(got), nonEmptyLines(base))
	if len(extra) != 1 || !strings.Contains(extra[0], "herdr") {
		return ""
	}
	return extra[0]
}

func difference(got, base []string) []string {
	left := append([]string(nil), base...)
	var extra []string
	for _, l := range got {
		if i := slices.Index(left, l); i >= 0 {
			left = slices.Delete(left, i, i+1)
			continue
		}
		extra = append(extra, l)
	}
	return extra
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
