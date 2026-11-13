//go:build unix

package run

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPonytailOffByConfig(t *testing.T) {
	bin := builtATM(t)
	for _, harness := range []string{"claude", "codex", "opencode", "pi", "cursor", "hermes"} {
		t.Run(harness, func(t *testing.T) {
			for _, setting := range []string{"false", "absent", "true"} {
				t.Run(setting, func(t *testing.T) { ponytailConfigRun(t, bin, harness, setting) })
			}
		})
	}
}

func ponytailConfigRun(t *testing.T, bin, harness, setting string) {
	t.Helper()
	cfg := atmSet(atmYAML, "delivery", "printf delivered > delivered")
	if setting != "absent" {
		cfg += "ponytail: " + setting + "\n"
	}
	// serve binds at git's toplevel; t.TempDir can be another spelling of it (Darwin's /var).
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	call := filepath.Join(t.TempDir(), "call")
	t.Setenv("FAKE_AGENT_CALL", call)
	t.Setenv("FAKE_ACP_CALL", call)
	if harness == "hermes" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.Symlink(exe, filepath.Join(dir, "hermes")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	t.Setenv("FAKE_AGENT_WORK", `for dir in .claude/skills .agents/skills .opencode/skills .atm/skills .cursor/skills; do
test ! -e "$dir/ponytail" && test ! -e "$dir/ponytail-review" || exit 1
done; `+fixWork)
	if harness != "hermes" {
		t.Setenv("FAKE_PONYTAIL_WORK", "test -f "+skillDirs[harness]+"/ponytail-review/SKILL.md && "+
			"git check-ignore -q "+skillDirs[harness]+"/ponytail-review/SKILL.md")
	}
	var wait, release func()
	if harness == "claude" {
		wait, release = holdFakeAgents(t, 1)
		defer release()
	}
	restart := ponytailConfigServer(t, bin, top)
	f := startFollow(t, bin, top, false, "run", "--follow", "--harness", harness, issueFile(t, issue))
	if wait != nil {
		waitPonytailConfigAgent(t, f)
		wait()
		assertPonytailConfigLiveScreen(t, bin, top, setting == "false")
		release()
	}
	f.exit(0)
	assertWorkerWithoutPonytail(t, harness, call)
	assertPonytailConfigReport(t, top, call, setting == "false")
	if harness == "claude" {
		assertPonytailConfigScreen(t, bin, top, setting == "false", restart)
	}
}

func waitPonytailConfigAgent(t *testing.T, f *followed) {
	t.Helper()
	timeout := time.NewTimer(time.Minute)
	defer timeout.Stop()
	for started := false; !started; {
		select {
		case line, open := <-f.lines:
			if !open {
				t.Fatalf("atm run ended before starting its agent: %s", f.errors())
			}
			var ev struct{ Step, State string }
			_ = json.Unmarshal([]byte(line), &ev)
			started = ev.Step == "agent" && ev.State == "started"
		case <-timeout.C:
			t.Fatalf("atm run did not start its agent: %s", f.errors())
		}
	}
}

func ponytailConfigServer(t *testing.T, bin, top string) func() {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := startVersionServe(t, bin, top)
	finish := func() {
		if server != nil {
			endDeliveryServer(t, server, top)
			server = nil
		}
	}
	t.Cleanup(finish)
	return func() {
		finish()
		server = startVersionServe(t, bin, top)
		t.Cleanup(finish)
	}
}

func assertWorkerWithoutPonytail(t *testing.T, harness, call string) {
	t.Helper()
	var args []string
	var prompt string
	if harness == "hermes" {
		c := hermesCall(t, call)
		_, prompt = promptText(c["session/prompt"])
		for _, arg := range c["args"].([]any) {
			args = append(args, arg.(string))
		}
	} else {
		c := agentCall(t, call)
		args, prompt = c.Args, sentPrompt(harness, c)
	}
	if strings.Contains(prompt, "ponytail") || strings.Contains(strings.Join(args, " "), "ponytail") ||
		slices.Contains(args, "--skill") {
		t.Fatalf("worker received ponytail: argv=%v, prompt=%s", args, prompt)
	}
}

func assertPonytailConfigReport(t *testing.T, top, call string, disabled bool) {
	t.Helper()
	r := e2eReport(t, top, "issue-1")
	n := r.node("ponytail")
	if r.FailedNode != "" || r.node("checks").Result != "passed" || r.node("delivery").Result != "passed" {
		t.Fatalf("run did not deliver: %+v", r)
	}
	if disabled {
		if n.Result != "skipped" || n.Reason == nil || *n.Reason != "skipped by configuration: ponytail: false" ||
			n.Kept != nil || n.StartedAt != "" {
			t.Fatalf("report does not distinguish disabled ponytail: %+v", n)
		}
		for _, path := range []string{call + ".ponytail", filepath.Join(runDir(top, "issue-1"), "ponytail.jsonl"),
			filepath.Join(runDir(top, "issue-1"), "brief-ponytail.md")} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("disabled ponytail left %s: %v", path, err)
			}
		}
	} else {
		assertPonytailEnabled(t, n, call)
	}
	assertPonytailConfigEvents(t, top, disabled)
}

func assertPonytailEnabled(t *testing.T, n *node, call string) {
	t.Helper()
	if n.Result != "passed" || n.Kept == nil || n.StartedAt == "" {
		t.Fatalf("default ponytail did not run: %+v", n)
	}
	b, err := os.ReadFile(call + ".ponytail")
	if err != nil || !strings.Contains(string(b), "ponytail-review") {
		t.Fatalf("ponytail did not receive its skill: %s, %v", b, err)
	}
}

func assertPonytailConfigEvents(t *testing.T, top string, disabled bool) {
	t.Helper()
	_, err := Follow(top, "issue-1", nil, func(f Frame) {
		var ev struct{ Step string }
		if json.Unmarshal(f.Event, &ev) == nil && disabled && ev.Step == "ponytail" {
			t.Errorf("disabled ponytail emitted an event: %s", f.Event)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertPonytailConfigScreen(t *testing.T, bin, top string, disabled bool, restart func()) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	for _, reload := range []bool{false, true} {
		if reload {
			restart()
		} else {
			assertPonytailConfigPlainScreen(t, bin, top, disabled)
		}
		a := &attached{t: t, chunks: make(chan string, 64), rows: 50}
		a.attach(bin, top, 120, 50, "issue-1")
		s := a.drawn(func(s string) bool { return strings.Contains(s, "q close") })
		a.key("q")
		a.exits()
		if !strings.Contains(s, "Checks") || !strings.Contains(s, "Delivery") ||
			strings.Contains(s, "Slop detector") == disabled {
			t.Fatalf("wrong ponytail row, reload=%v, disabled=%v:\n%s", reload, disabled, s)
		}
		if disabled {
			t.Logf("ponytail disabled, reload=%v:\n%s", reload, s)
		}
	}
}

func assertPonytailConfigPlainScreen(t *testing.T, bin, top string, disabled bool) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	defer t.Setenv("NO_COLOR", "")
	cmd := exec.Command(bin, "attach", "issue-1")
	cmd.Dir = top
	b, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(b), "slop detector") == disabled {
		t.Fatalf("wrong ponytail row on plain screen: %s, %v", b, err)
	}
}

func assertPonytailConfigLiveScreen(t *testing.T, bin, top string, disabled bool) {
	t.Helper()
	a := &attached{t: t, chunks: make(chan string, 64), rows: 50}
	a.attach(bin, top, 120, 50, "issue-1")
	s := a.sees(func(s string) bool { return strings.Contains(s, "╭─ Agent") && strings.Contains(s, "q detach") })
	if strings.Contains(s, "Slop detector") == disabled {
		t.Fatalf("wrong ponytail row while worker runs, disabled=%v:\n%s", disabled, s)
	}
	a.key("q")
	a.exits()
}
