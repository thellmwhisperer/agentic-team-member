// Command fakeagent plays the agent CLIs in internal/run's tests, under each one's name. The tests build it
// once per process and link it as claude, codex, opencode, pi and cursor-agent first on PATH.
package main

import (
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
	"time"
)

func main() {
	ownSession()
	os.Exit(fakeAgent(filepath.Base(os.Args[0])))
}

type obj = map[string]any

// fakeReport is the fake agent's final message: a draft object, then the report, the last top-level object.
const fakeReport = "Done.\n{\"draft\": true}\n" + `{"test_file": "a_test.sh", "changed_files": ["a.txt"], ` +
	`"summary": "s", "commands_run": [], "follow_ups": [{"title": "t", "red_test": "b_test.sh"}]}`

// fixTest writes a test that a.txt, which repo's base commit has broken, is fixed and broken no longer.
const fixTest = "; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh"

// fixWork fixes a.txt and tests it, as the tests' fixWork does.
const fixWork = "echo fixed > a.txt" + fixTest

// work is what the fake agent does in its clone for each task type, a shell line.
var work = map[string]string{"fix": fixWork, "feature": fixWork, "greenfield": fixWork, "refactor": "echo b > b.txt",
	"tests": "echo 'grep -q broken a.txt' > a_test.sh", "docs": "echo doc > README.md", "chore": "echo d > deps.txt"}

// fakeAgent plays the agent CLI name in its working directory, as $FAKE_AGENT says. Its call goes to
// $FAKE_AGENT_CALL. The default plays a run that, when the ponytail skill is where name finds it and outside
// git, uses it, does $FAKE_AGENT_WORK or else the work of the brief's task type, and reports what that work
// left in .atm/fake-report.json, or else fakeReport; fail
// writes boom to stderr and exits 3; silent writes its pid to $FAKE_AGENT_PID and says nothing more;
// killed
// SIGKILLs itself; no-report and no-test-file each break the run one way; hang
// starts a grandchild, whose pid goes to $FAKE_AGENT_PID, and never ends; hang-session does the same with the
// grandchild in its own session; hang-orphan starts it in its own session through a child that exits at once,
// so init adopts it. The ponytail pass plays the same with the ponytail-review skill,
// $FAKE_PONYTAIL as its mode, $FAKE_PONYTAIL_WORK as its work and $FAKE_PONYTAIL_REPORT, or no findings, as
// its report; its call goes to $FAKE_AGENT_CALL.ponytail. With FAKE_WORKER_TMPDIR set, the worker writes
// test.log under its $TMPDIR, by a tool call naming its absolute path. $FAKE_AGENT_ORDER, when
// set, receives one line per call. $FAKE_AGENT_STREAM goes to its stdout after its first
// line, <clone> in it its working directory.
func fakeAgent(name string) int {
	go watchFakeAgentOwner()
	if script := os.Getenv("REJECTION_SCRIPT"); script != "" {
		cmd := exec.Command("sh", script)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return exit.ExitCode()
			}
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	return fakeAgentCLI(name)
}

// The server can outlive the test, so watch the test's PID, never the immediate parent.
// Stat the directory it started in: macOS getcwd still succeeds after that directory is removed.
func watchFakeAgentOwner() {
	owner, _ := strconv.Atoi(os.Getenv("ATM_FAKE_OWNER_PID"))
	wd, wdErr := os.Getwd()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		_, err := os.Stat(wd)
		if owner > 0 && !pidAlive(owner) || wdErr != nil || err != nil {
			os.Exit(0)
		}
	}
}

func fakeAgentCLI(name string) int {
	mode := os.Getenv("FAKE_AGENT")
	if fakeAgentSetup(mode) {
		return 0
	}
	args := os.Args[1:]
	call, skill, todo, text := fakeAgentTask(name, args, &mode)
	if flag := codexResumeRejected(name, args); flag != "" {
		fmt.Fprintf(os.Stderr, "unexpected argument '%s'\n", flag)
		return 2
	}
	waitForFakeAgentRelease()
	wd, _ := os.Getwd()
	stream := strings.ReplaceAll(os.Getenv("FAKE_AGENT_STREAM"), "<clone>", filepath.ToSlash(wd))
	if mode != "empty-fail" {
		fmt.Print(`{"type": "system", "subtype": "init"}`+"\n", stream) // the test's lines too
		fmt.Fprintln(os.Stderr, "fake stderr")
	}
	if exit, done := fakeAgentMode(mode); done {
		return exit
	}
	fakeAgentSkill(name, args, skill)
	if skill == "ponytail" && os.Getenv("FAKE_WORKER_TMPDIR") != "" { // the worker logs to its $TMPDIR
		log := filepath.ToSlash(filepath.Join(os.Getenv("TMPDIR"), "test.log"))
		fmt.Print(bashEvent(name, `echo log > "`+log+`"`))
		todo += `; echo log > "` + log + `"`
	}
	if mode == "tail-sequence" {
		for i := 1; i <= 22; i++ {
			line := fmt.Sprintf("line-%02d", i)
			if i%2 == 0 {
				fmt.Fprintln(os.Stderr, line)
			} else {
				fmt.Println(line)
			}
			time.Sleep(20 * time.Millisecond)
		}
		return 3
	}
	if err := exec.Command("sh", "-c", todo).Run(); err != nil {
		return 1
	}
	text = cmp.Or(map[string]string{"no-report": "Done, nothing to report.", "no-test-file": `{"summary": "s"}`}[mode],
		workReport(call), text)
	if name == "codex" {
		_ = os.WriteFile(after(args, "-o"), []byte(text), 0o600)
	}
	for _, ev := range map[string][]obj{
		"claude":   {{"type": "result", "result": text}},
		"codex":    {{"type": "item.completed", "item": obj{"type": "agent_message", "text": "not the -o file"}}},
		"opencode": {{"type": "text", "part": obj{"text": text}}},
		"pi": {{"type": "message_end", "message": obj{"role": "assistant",
			"content": []obj{{"type": "text", "text": text}}}}},
		"cursor-agent": {{"type": "assistant", "message": obj{"role": "assistant",
			"content": []obj{{"type": "text", "text": text}}}}, {"type": "result", "result": "not the report"}},
	}[name] {
		emit(ev)
	}
	return 0
}

func fakeAgentTask(name string, args []string, mode *string) (string, string, string, string) {
	stdin, _ := io.ReadAll(os.Stdin)
	brief := map[bool]string{true: string(stdin), false: args[len(args)-1]}[name == "claude"]
	_, typ, _ := strings.Cut(brief, "Task type: ")
	typ, _, _ = strings.Cut(typ, ".")
	skill, call, todo, text, kind := fakeAgentKind(brief, typ, mode)
	recordFakeAgentCall(args, stdin, call)
	recordOrder(kind)
	return call, skill, todo, text
}

// fakeAgentKind is which fake the brief plays, and the call suffix its recording uses.
func fakeAgentKind(brief, typ string, mode *string) (skill, call, todo, text, kind string) {
	if k, ok := continuedKind(brief); ok {
		*mode = k.Mode
		return k.Skill, k.Call + ".return", k.Todo, k.Text, k.Kind
	}
	skill, call, todo, text, kind = "ponytail", "", cmp.Or(os.Getenv("FAKE_AGENT_WORK"), work[typ]), fakeReport, "worker"
	if strings.Contains(brief, "`ponytail-review`") {
		*mode = os.Getenv("FAKE_PONYTAIL")
		skill, call, kind = "ponytail-review", ".ponytail", "ponytail"
		todo = os.Getenv("FAKE_PONYTAIL_WORK")
		text = cmp.Or(os.Getenv("FAKE_PONYTAIL_REPORT"), `{"findings": [], "summary": "Lean already."}`)
	}
	saveKind(fakeKind{skill, call, todo, text, kind, *mode})
	return skill, call, todo, text, kind
}

// fakeKind is the brief's fake, kept outside the clone so a returned report replays a review, not a new worker.
type fakeKind struct{ Skill, Call, Todo, Text, Kind, Mode string }

func saveKind(k fakeKind) {
	b, _ := json.Marshal(k)
	_ = os.WriteFile(kindPath(), b, 0o600)
}

func continuedKind(brief string) (fakeKind, bool) {
	if !strings.Contains(brief, "Your final message was rejected:") { // after the last message when no session resumes
		return fakeKind{}, false
	}
	var k fakeKind
	b, err := os.ReadFile(kindPath())
	if err != nil || json.Unmarshal(b, &k) != nil || k.Kind == "" || k.Kind == "worker" {
		return fakeKind{}, false
	}
	return k, true
}

func kindPath() string {
	wd, _ := os.Getwd()
	return filepath.Join(os.TempDir(), "atm-fake-"+strings.NewReplacer("/", "_", "\\", "_").Replace(wd))
}

func recordOrder(kind string) {
	path := os.Getenv("FAKE_AGENT_ORDER")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, kind)
	_ = f.Close()
}

func fakeAgentSetup(mode string) bool {
	if mode == "grandchild" {
		_ = os.WriteFile(os.Getenv("FAKE_AGENT_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	if ready := os.Getenv("FAKE_AGENT_READY"); ready != "" {
		_ = os.WriteFile(ready, nil, 0o600)
	}
	if os.Getenv("FAKE_AGENT_KILL_PARENT") != "" {
		parent, _ := os.FindProcess(os.Getppid())
		_ = parent.Kill()
		_ = os.Unsetenv("FAKE_AGENT_KILL_PARENT")
	}
	if mode == "orphan-parent" {
		grandchild := exec.Command(os.Args[0])
		grandchild.Env = append(os.Environ(), "FAKE_AGENT=grandchild")
		detach(grandchild)
		_ = grandchild.Start()
		return true
	}
	if mode == "grandchild" {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt) // blocks until killed: ATM sends nothing but SIGKILL
		<-c
		return true
	}
	return false
}

func fakeAgentMode(mode string) (int, bool) {
	switch mode {
	case "fail", "empty-fail":
		return fakeAgentFailure(mode)
	case "silent":
		_ = os.WriteFile(os.Getenv("FAKE_AGENT_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(time.Hour)
		return 0, true
	case "killed":
		self, _ := os.FindProcess(os.Getpid())
		_ = self.Kill()
		time.Sleep(time.Hour)
		return 0, true
	case "hang", "hang-session":
		grandchild := exec.Command(os.Args[0])
		grandchild.Env, grandchild.Stdout = append(os.Environ(), "FAKE_AGENT=grandchild"), os.Stdout
		if mode == "hang-session" {
			detach(grandchild)
		}
		_ = grandchild.Run()
		return 0, true
	case "hang-orphan":
		parent := exec.Command(os.Args[0])
		parent.Env = append(os.Environ(), "FAKE_AGENT=orphan-parent")
		_ = parent.Run()
		time.Sleep(time.Hour)
		return 0, true
	case "orphan":
		return fakeOrphanAgent()
	}
	return 0, false
}

func fakeAgentFailure(mode string) (int, bool) {
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "boom")
	}
	return 3, true
}

func fakeOrphanAgent() (int, bool) {
	grandchild := exec.Command(os.Args[0])
	grandchild.Env = append(os.Environ(), "FAKE_AGENT=grandchild")
	grandchild.ExtraFiles = append(grandchild.ExtraFiles, os.NewFile(3, "clone.lock"))
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return 1, true
	}
	grandchild.Stdout, grandchild.Stderr = null, null
	err = grandchild.Start()
	_ = null.Close()
	if err != nil {
		return 1, true
	}
	return 0, false
}

// fakeAgentSkill loads skill.
func fakeAgentSkill(name string, args []string, skill string) {
	dir := map[string]string{"claude": ".claude/skills/" + skill, "codex": ".agents/skills/" + skill,
		"opencode": ".opencode/skills/" + skill, "pi": after(args, "--skill"),
		"cursor-agent": ".cursor/skills/" + skill}[name]
	path := dir + "/SKILL.md"
	b, _ := os.ReadFile(path)
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	if !bytes.Contains(b, []byte("name: "+skill+"\n")) || exec.Command("git", "check-ignore", "-q", path).Run() != nil {
		return
	}
	failed, exit, output := false, 0, "name: "+skill+"\n"
	for _, ev := range map[string][]obj{
		"claude": {{"type": "assistant", "message": obj{"content": []obj{{"type": "tool_use", "id": "s1", "name": "Skill",
			"input": obj{"skill": skill}}}}},
			{"type": "user", "message": obj{"content": []obj{{"type": "tool_result", "tool_use_id": "s1",
				"is_error": failed}}}}},
		"codex": {{"type": "item.completed", "item": obj{"id": "s1", "type": "command_execution",
			"command": "cat " + path, "exit_code": exit, "aggregated_output": output}}},
		"opencode": {{"type": "tool_use", "part": obj{"callID": "s1", "tool": "skill", "state": obj{
			"status": map[bool]string{true: "error", false: "completed"}[failed], "input": obj{"name": skill}}}}},
		"pi": {{"type": "tool_execution_start", "toolCallId": "s1", "toolName": "read", "args": obj{"path": path}},
			{"type": "tool_execution_end", "toolCallId": "s1", "toolName": "read", "isError": failed}},
		"cursor-agent": {{"type": "tool_call", "subtype": "started", "call_id": "s1",
			"tool_call": obj{"readToolCall": obj{"args": obj{"path": path}}}},
			{"type": "tool_call", "subtype": "completed", "call_id": "s1", "tool_call": obj{"hookAdditionalContexts": []obj{},
				"readToolCall": obj{"args": obj{"path": path}, "result": obj{
					map[bool]string{true: "failure", false: "success"}[failed]: obj{}}}}}},
	}[name] {
		emit(ev)
	}
}

// codexResumeRejected is the flag codex exec resume rejects, as the real CLI does, "" when the call is not one.
func codexResumeRejected(name string, args []string) string {
	if name != "codex" || !slices.Contains(args, "resume") {
		return ""
	}
	for _, flag := range []string{"-C", "--sandbox"} {
		if slices.Contains(args, flag) {
			return flag
		}
	}
	return ""
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

// bashEvent is harness's stream line for a shell tool call running command, as the clone boundary check reads it.
func bashEvent(harness, command string) string {
	q := func(s string) string { // unescaped, so <clone> reaches the check as it is
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(s)
		return strings.TrimSpace(b.String())
	}
	switch harness {
	case "pi":
		return `{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":` + q(command) + `}}` + "\n"
	case "opencode":
		return `{"type":"tool_use","part":{"callID":"c1","tool":"bash","state":{"status":"completed","input":{"command":` +
			q(command) + `}}}}` + "\n"
	default:
		return `{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"c1","name":"Bash","input":{"command":` + q(command) + `}}]}}` + "\n"
	}
}
