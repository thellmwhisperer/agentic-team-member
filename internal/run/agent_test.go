package run

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type obj = map[string]any

// fakeReport is the fake agent's final message: a draft object, then the report, the last top-level object.
const fakeReport = "Done.\n{\"draft\": true}\n" + `{"test_file": "a_test.sh", "changed_files": ["a.txt"], ` +
	`"summary": "s", "commands_run": [], "follow_ups": [{"title": "t", "red_test": "b_test.sh"}]}`

// fixWork fixes a.txt, which repo's base commit has broken, and tests it.
const fixWork = "echo fixed > a.txt; echo 'grep -q fixed a.txt' > a_test.sh"

// work is what the fake agent does in its clone for each task type, a shell line.
var work = map[string]string{"fix": fixWork, "feature": fixWork, "greenfield": fixWork, "refactor": "echo b > b.txt",
	"tests": "echo 'grep -q broken a.txt' > a_test.sh", "docs": "echo doc > README.md", "chore": "echo d > deps.txt"}

// fakeAgent plays the agent CLI name in its working directory, as $FAKE_AGENT says. Its call goes to
// $FAKE_AGENT_CALL. The default plays a run that, when the ponytail skill is where name finds it and outside
// git, uses it, does $FAKE_AGENT_WORK or else the work of the brief's task type, and reports what that work
// left in .atm/fake-report.json, or else fakeReport; fail
// exits 3; no-report, no-test-file and no-skill each break the run one way; hang starts a grandchild, whose pid
// goes to $FAKE_AGENT_PID, and never ends. The ponytail pass plays the same with the ponytail-review skill,
// $FAKE_PONYTAIL as its mode, $FAKE_PONYTAIL_WORK as its work and $FAKE_PONYTAIL_REPORT, or no findings, as
// its report; its call goes to $FAKE_AGENT_CALL.ponytail. $FAKE_AGENT_STREAM goes to its stdout after its first line.
func fakeAgent(name string) int {
	mode := os.Getenv("FAKE_AGENT")
	if mode == "grandchild" {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt) // blocks until killed: ATM sends nothing but SIGKILL
		_ = os.WriteFile(os.Getenv("FAKE_AGENT_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		<-c
		return 0
	}
	args := os.Args[1:]
	stdin, _ := io.ReadAll(os.Stdin)
	brief := map[bool]string{true: string(stdin), false: args[len(args)-1]}[name == "claude"]
	_, typ, _ := strings.Cut(brief, "Task type: ")
	typ, _, _ = strings.Cut(typ, ".")
	skill, call, todo, text := "ponytail", "", cmp.Or(os.Getenv("FAKE_AGENT_WORK"), work[typ]), fakeReport
	if strings.Contains(brief, "`ponytail-review`") {
		mode, skill, call, todo = os.Getenv("FAKE_PONYTAIL"), "ponytail-review", ".ponytail", os.Getenv("FAKE_PONYTAIL_WORK")
		text = cmp.Or(os.Getenv("FAKE_PONYTAIL_REPORT"), `{"findings": [], "summary": "Lean already."}`)
	}
	recordFakeAgentCall(args, stdin, call)
	waitForFakeAgentRelease()
	fmt.Print(`{"type": "system", "subtype": "init"}`+"\n", os.Getenv("FAKE_AGENT_STREAM")) // the test's lines too
	fmt.Fprintln(os.Stderr, "fake stderr")
	switch mode {
	case "fail":
		return 3
	case "hang":
		grandchild := exec.Command(os.Args[0])
		grandchild.Env, grandchild.Stdout = append(os.Environ(), "FAKE_AGENT=grandchild"), os.Stdout
		_ = grandchild.Run()
		return 0
	}
	path := map[string]string{"claude": ".claude/skills/" + skill, "codex": ".agents/skills/" + skill,
		"opencode": ".opencode/skills/" + skill, "pi": after(args, "--skill")}[name] + "/SKILL.md"
	b, _ := os.ReadFile(path)
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	if bytes.Contains(b, []byte("name: "+skill+"\n")) && mode != "no-skill" &&
		exec.Command("git", "check-ignore", "-q", path).Run() == nil {
		emit(map[string]obj{
			"claude": {"type": "assistant", "message": obj{"content": []obj{{"type": "tool_use", "name": "Skill",
				"input": obj{"skill": skill}}}}},
			"codex":    {"type": "item.completed", "item": obj{"type": "command_execution", "command": "cat " + path}},
			"opencode": {"type": "tool_use", "part": obj{"tool": "skill", "state": obj{"input": obj{"name": skill}}}},
			"pi":       {"type": "tool_execution_start", "toolName": "read", "args": obj{"path": path}},
		}[name])
	}
	_ = exec.Command("sh", "-c", todo).Run()
	text = cmp.Or(map[string]string{"no-report": "Done, nothing to report.", "no-test-file": `{"summary": "s"}`}[mode],
		workReport(call), text)
	if name == "codex" {
		_ = os.WriteFile(after(args, "-o"), []byte(text), 0o600)
	}
	emit(map[string]obj{
		"claude":   {"type": "result", "result": text},
		"codex":    {"type": "item.completed", "item": obj{"type": "agent_message", "text": "not the -o file"}},
		"opencode": {"type": "text", "part": obj{"text": text}},
		"pi": {"type": "message_end", "message": obj{"role": "assistant",
			"content": []obj{{"type": "text", "text": text}}}},
	}[name])
	return 0
}

func recordFakeAgentCall(args []string, stdin []byte, call string) {
	if path := os.Getenv("FAKE_AGENT_CALL"); path != "" {
		b, _ := json.Marshal(obj{"args": args, "stdin": string(stdin), "env": os.Environ()})
		_ = os.WriteFile(path+call, b, 0o600)
	}
}

func waitForFakeAgentRelease() {
	if ready := os.Getenv("FAKE_AGENT_BARRIER_READY"); ready != "" {
		_ = os.WriteFile(filepath.Join(ready, strconv.Itoa(os.Getpid())), nil, 0o600)
		for {
			if _, err := os.Stat(os.Getenv("FAKE_AGENT_BARRIER_RELEASE")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// workReport is the report the worker's work left in .atm/fake-report.json, "" for none or for the ponytail
// pass, whose call is ".ponytail".
func workReport(call string) string {
	b, _ := os.ReadFile(".atm/fake-report.json")
	return map[bool]string{true: string(b)}[call == ""]
}

func emit(ev obj) {
	b, _ := json.Marshal(ev)
	fmt.Println(string(b))
}

func after(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// agentStep is the agent step's end event, after checking the clone step passed, and the clone and log paths.
func agentStep(t *testing.T, out *bytes.Buffer) (end obj, clone, log string) {
	t.Helper()
	evs := events(t, out)
	if len(evs) < 8 || evs[7]["state"] == "failed" && len(evs) != 8 || evs[3]["step"] != "clone" ||
		evs[3]["state"] != "passed" || evs[6]["step"] != "agent" ||
		evs[6]["state"] != "started" || evs[7]["step"] != "agent" {
		t.Fatalf("want the clone step passed, then the agent step, got %v", evs)
	}
	clone = evs[3]["clone"].(string)
	logs, _ := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(clone)), "runs", "t", "worker*.jsonl"))
	if len(logs) != 1 || filepath.Base(logs[0]) != "worker.jsonl" {
		t.Fatalf("want one worker.jsonl in the run's directory, got %v", logs)
	}
	return evs[7], clone, logs[0]
}

func TestRunDrivesTheAgent(t *testing.T) {
	cases := map[string]string{
		"claude": "-p --output-format stream-json --verbose --setting-sources project --strict-mcp-config " +
			"--permission-mode acceptEdits --tools Read,Edit,Write,Bash,Glob,Grep,Skill " +
			"--allowedTools Read,Edit,Write,Bash,Glob,Grep,Skill --model m --effort e --x y",
		"codex": `exec --json -C <clone> --sandbox workspace-write -o <log>.final.md -c model="m" ` +
			`-c model_reasoning_effort="e" --x y <brief>`,
		"opencode": "run --pure --format json --dir <clone> -m m --variant e --x y <brief>",
		"pi": "-p --mode json --no-extensions --no-skills --no-prompt-templates --no-context-files --no-session " +
			"--skill <clone>/.atm/skills/ponytail --model m --thinking e --x y <brief>",
	}
	for harness, want := range cases {
		t.Run(harness, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			call := filepath.Join(t.TempDir(), "call.json")
			t.Setenv("FAKE_AGENT_CALL", call)
			t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
			var out bytes.Buffer
			err := Run("t", []string{"--harness", harness, "--model", "m", "--effort", "e", "--harness-arg", "--x",
				"--harness-arg", "y", "--env", "ATM_FAKE=a=b", issueFile(t, issue)}, &out, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			end, clone, log := agentStep(t, &out)
			report, _ := end["report"].(map[string]any)
			if end["state"] != "passed" || end["log"] != log || report["test_file"] != "a_test.sh" {
				t.Fatalf("end event: %v", end)
			}
			brief, err := os.ReadFile(filepath.Join(root, ".atm", "runs", "t", "brief.md"))
			if err != nil {
				t.Fatal(err)
			}
			got := agentCall(t, call)
			args := strings.Join(got.Args, " ")
			args = strings.ReplaceAll(args, string(brief), "<brief>")
			args = strings.ReplaceAll(args, strings.TrimSuffix(log, ".jsonl"), "<log>")
			if args = filepath.ToSlash(strings.ReplaceAll(args, clone, "<clone>")); args != want {
				t.Fatalf("args:\n%s\nwant\n%s", args, want)
			}
			if wantStdin := map[bool]string{true: string(brief)}[harness == "claude"]; got.Stdin != wantStdin {
				t.Fatalf("stdin: %q, want %q", got.Stdin, wantStdin)
			}
			if !slices.Contains(got.Env, "ATM_FAKE=a=b") || slices.ContainsFunc(got.Env, func(kv string) bool {
				return strings.HasPrefix(kv, "CLAUDE_CODE_CHILD_SESSION=")
			}) {
				t.Fatalf("env: want ATM_FAKE and no CLAUDE_CODE_CHILD_SESSION, got %v", got.Env)
			}
			lines := logLines(t, log)
			if len(lines) != 4 || lines[1] != `"fake stderr"` {
				t.Fatalf("want every line of the agent in the log, got %q", lines)
			}
			if left := clones(t, root); len(left) != 0 {
				t.Fatalf("the run's clone outlived it: %v", left)
			}
		})
	}
}

// agentCall is what the fake agent recorded at path of how it was called.
func agentCall(t *testing.T, path string) (call struct {
	Args  []string
	Stdin string
	Env   []string
}) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &call)
	}
	if err != nil {
		t.Fatal("the agent was not called: ", err)
	}
	return call
}

// logLines is every line of the log at path, after checking each is JSON.
func logLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	for s := bufio.NewScanner(f); s.Scan(); {
		if !json.Valid(s.Bytes()) {
			t.Fatalf("not JSON in the log: %q", s.Text())
		}
		lines = append(lines, s.Text())
	}
	return lines
}

func TestRunDiesWhenTheAgentFails(t *testing.T) {
	cases := []struct {
		harness, mode, why string
		args               []string
	}{
		{harness: "claude", mode: "fail", why: "exit status 3"},
		{harness: "pi", mode: "no-report", why: "no report"},
		{harness: "codex", mode: "no-report", why: "no report"},
		{harness: "opencode", mode: "no-test-file", why: "test_file"},
		{harness: "claude", mode: "no-skill", why: "ponytail"},
		{harness: "codex", mode: "no-skill", why: "ponytail"},
		{harness: "opencode", mode: "no-skill", why: "ponytail"},
		{harness: "pi", mode: "no-skill", why: "ponytail"},
		{harness: "claude", why: "KEY=VALUE", args: []string{"--env", "NOVALUE"}},
	}
	for _, c := range cases {
		t.Run(c.harness+" "+c.mode+c.why, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			t.Setenv("FAKE_AGENT", c.mode)
			var out bytes.Buffer
			err := Run("t", append(c.args, "--harness", c.harness, issueFile(t, issue)), &out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.why) {
				t.Fatalf("want an error naming %q, got %v", c.why, err)
			}
			if c.mode == "" {
				return // a bad flag: no step ran
			}
			if end, _, _ := agentStep(t, &out); end["state"] != "failed" || end["error"] != err.Error() {
				t.Fatalf("end event: %v", end)
			}
			if left := clones(t, root); len(left) != 0 {
				t.Fatalf("a failed run left its clone: %v", left)
			}
		})
	}
}
