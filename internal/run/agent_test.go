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

const fakeSlicing = `{"disposition":"ready","reason":"One fulfilled contract","fields":{` +
	`"non_goals":{"status":"present","reason":"Explicit exclusions"},` +
	`"invariants":{"status":"present","reason":"Explicit invariants"},` +
	`"acceptance":{"status":"present","reason":"Runnable check supplied"},` +
	`"split_triggers":{"status":"present","reason":"Explicit triggers"}},"mandatory_pending":[],"drafts":[]}`

// fixTest writes a test that a.txt, which repo's base commit has broken, is fixed and broken no longer.
const fixTest = "; echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh"

// fixWork fixes a.txt and tests it.
const fixWork = "echo fixed > a.txt" + fixTest

// work is what the fake agent does in its clone for each task type, a shell line.
var work = map[string]string{"fix": fixWork, "feature": fixWork, "greenfield": fixWork, "refactor": "echo b > b.txt",
	"tests": "echo 'grep -q broken a.txt' > a_test.sh", "docs": "echo doc > README.md", "chore": "echo d > deps.txt"}

// fakeAgent plays the agent CLI name in its working directory, as $FAKE_AGENT says. Its call goes to
// $FAKE_AGENT_CALL. The default plays a run that, when the ponytail skill is where name finds it and outside
// git, uses it, does $FAKE_AGENT_WORK or else the work of the brief's task type, and reports what that work
// left in .atm/fake-report.json, or else fakeReport; fail
// writes boom to stderr and exits 3; silent writes its pid to $FAKE_AGENT_PID and says nothing more; killed
// SIGKILLs itself; no-report, no-test-file, no-skill, skill-error and wrong-skill each break the run one way; hang
// starts a grandchild, whose pid goes to $FAKE_AGENT_PID, and never ends; hang-session does the same with the
// grandchild in its own session; hang-orphan starts it in its own session through a child that exits at once,
// so init adopts it. The ponytail pass plays the same with the ponytail-review skill,
// $FAKE_PONYTAIL as its mode, $FAKE_PONYTAIL_WORK as its work and $FAKE_PONYTAIL_REPORT, or no findings, as
// its report; its call goes to $FAKE_AGENT_CALL.ponytail. The contract review plays the same with $FAKE_REVIEW,
// $FAKE_REVIEW_WORK and $FAKE_REVIEW_REPORT, or an approval, {base} and {head} in it its brief's; its call goes
// to $FAKE_AGENT_CALL.review. A brief headed "# Contract repair" plays $FAKE_REPAIR, $FAKE_REPAIR_WORK
// and $FAKE_REPAIR_REPORT, or the worker's report; its call goes to $FAKE_AGENT_CALL.repairN. Review reports
// and repair work may be sequenced as FAKE_REVIEW_REPORT_N and FAKE_REPAIR_WORK_N. $FAKE_AGENT_ORDER, when
// set, receives one line per call. $FAKE_AGENT_STREAM goes to its stdout after its first
// line, <clone> in it its working directory.
func fakeAgent(name string) int {
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

func fakeAgentCLI(name string) int {
	mode := os.Getenv("FAKE_AGENT")
	if fakeAgentSetup(mode) {
		return 0
	}
	args := os.Args[1:]
	_, call, skill, todo, text := fakeAgentTask(name, args, &mode)
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
	fakeAgentSkill(name, args, skill, mode)
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
	if mode == "blank-lines" {
		for range 6 {
			fmt.Println()
			time.Sleep(250 * time.Millisecond)
		}
		text = "Done, nothing to report."
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

func fakeAgentTask(name string, args []string, mode *string) ([]byte, string, string, string, string) {
	stdin, _ := io.ReadAll(os.Stdin)
	brief := map[bool]string{true: string(stdin), false: args[len(args)-1]}[name == "claude"]
	_, typ, _ := strings.Cut(brief, "Task type: ")
	typ, _, _ = strings.Cut(typ, ".")
	skill, call, todo, text, kind := fakeAgentKind(brief, typ, mode)
	recordFakeAgentCall(args, stdin, call)
	recordOrder(kind)
	return stdin, call, skill, todo, text
}

// fakeAgentKind is which fake the brief plays, and the call suffix its recording uses.
func fakeAgentKind(brief, typ string, mode *string) (skill, call, todo, text, kind string) {
	if k, ok := continuedKind(brief); ok {
		*mode = k.Mode
		return k.Skill, k.Call + ".return", k.Todo, k.Text, k.Kind
	}
	skill, call, todo, text, kind = "ponytail", "", cmp.Or(os.Getenv("FAKE_AGENT_WORK"), work[typ]), fakeReport, "worker"
	switch {
	case strings.Contains(brief, "# Scope review"):
		*mode = os.Getenv("FAKE_SCOPE")
		skill, call, todo = "ponytail-review", ".scope", os.Getenv("FAKE_SCOPE_WORK")
		text, kind = fakeScope(brief, *mode), "scope"
	case strings.Contains(brief, "# Contract repair"):
		skill, call, todo, text, kind = fakeRepair(mode)
	case strings.Contains(brief, "# Contract review"):
		*mode = os.Getenv("FAKE_REVIEW")
		n := bump("review")
		skill, call, kind = "ponytail-review", ".review", "review"
		todo = cmp.Or(os.Getenv("FAKE_REVIEW_WORK_"+strconv.Itoa(n)), os.Getenv("FAKE_REVIEW_WORK"))
		text = substituteBrief(brief, cmp.Or(os.Getenv("FAKE_REVIEW_REPORT_"+strconv.Itoa(n)),
			os.Getenv("FAKE_REVIEW_REPORT"), defaultReview))
	case strings.Contains(brief, "`ponytail-review`"):
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
	if !strings.HasPrefix(strings.TrimSpace(brief), "Your final message was rejected:") {
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

const defaultReview = `{"disposition": "approved", "base": "{base}", "head": "{head}", "findings": [], ` +
	`"summary": "s", "task_slicing":` + fakeSlicing + `}`

func substituteBrief(brief, raw string) string {
	field := func(key string) string {
		_, v, _ := strings.Cut(brief, "\n- "+key+": ")
		v, _, _ = strings.Cut(v, "\n")
		return v
	}
	return strings.NewReplacer("{base}", field("Base"), "{head}", field("HEAD")).Replace(raw)
}

func fakeRepair(mode *string) (skill, call, todo, text, kind string) {
	*mode = os.Getenv("FAKE_REPAIR")
	n := bump("repair")
	return "ponytail", ".repair" + strconv.Itoa(n),
		cmp.Or(os.Getenv("FAKE_REPAIR_WORK_"+strconv.Itoa(n)), os.Getenv("FAKE_REPAIR_WORK")),
		cmp.Or(os.Getenv("FAKE_REPAIR_REPORT_"+strconv.Itoa(n)), os.Getenv("FAKE_REPAIR_REPORT"), fakeReport),
		"repair"
}

func bump(kind string) int {
	base := os.Getenv("FAKE_AGENT_CALL")
	if base == "" {
		return 1
	}
	path := base + "." + kind + ".n"
	b, _ := os.ReadFile(path)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	n++
	_ = os.WriteFile(path, []byte(strconv.Itoa(n)), 0o600)
	return n
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

// fakeAgentSkill loads skill, its result an error in mode skill-error, and loads another skill whose name has
// skill's as a prefix instead in mode wrong-skill.
func fakeAgentSkill(name string, args []string, skill, mode string) {
	dir := map[string]string{"claude": ".claude/skills/" + skill, "codex": ".agents/skills/" + skill,
		"opencode": ".opencode/skills/" + skill, "pi": after(args, "--skill"),
		"cursor-agent": ".cursor/skills/" + skill}[name]
	path := dir + "/SKILL.md"
	b, _ := os.ReadFile(path)
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	if !bytes.Contains(b, []byte("name: "+skill+"\n")) || mode == "no-skill" ||
		exec.Command("git", "check-ignore", "-q", path).Run() != nil {
		return
	}
	if mode == "wrong-skill" {
		skill, path = skill+"x", dir+"x/SKILL.md"
	}
	failed := mode == "skill-error"
	exit := map[bool]int{true: 1}[failed]
	output := map[bool]string{true: "name: " + skill + "\n"}[!failed] // a codex read proves only what it prints
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

// fakeScope is the scope reviewer's report on brief: $FAKE_SCOPE_REPORT, or an approval, {paths} in it the
// brief's. In mode issue it blocks each path its brief's issue does not name, on the issue's Scope line.
func fakeScope(brief, mode string) string {
	_, paths, _ := strings.Cut(brief, "\n- Paths: ")
	paths, _, _ = strings.Cut(paths, "\n")
	if mode == "issue" {
		_, issue, _ := strings.Cut(brief, "## ISSUE\n")
		issue, _, _ = strings.Cut(issue, "\n## PLAN")
		_, clause, _ := strings.Cut(issue, "\nScope:")
		clause, _, _ = strings.Cut(clause, "\n")
		var all []string
		_ = json.Unmarshal([]byte(paths), &all)
		findings := []obj{}
		for _, p := range all {
			if !strings.Contains(issue, p) {
				findings = append(findings, obj{"path": p, "clause": "Scope:" + clause, "reason": "not in the scope"})
			}
		}
		b, _ := json.Marshal(obj{"disposition": map[bool]string{true: "approved", false: "blocked"}[len(findings) == 0],
			"paths": json.RawMessage(paths), "findings": findings, "summary": "s"})
		return string(b)
	}
	return strings.ReplaceAll(cmp.Or(os.Getenv("FAKE_SCOPE_REPORT"),
		`{"disposition": "approved", "paths": {paths}, "findings": [], "summary": "s"}`), "{paths}", paths)
}

// workReport is the report the worker's work left in .atm/fake-report.json, "" for none or for the ponytail
// pass, whose call is ".ponytail".
func workReport(call string) string {
	b, _ := os.ReadFile(".atm/fake-report.json")
	var r obj
	if json.Unmarshal(b, &r) == nil {
		fs, _ := r["follow_ups"].([]any)
		for _, f := range fs {
			if f, ok := f.(obj); ok && f["paths"] == nil {
				f["paths"] = []string{"a.txt", "source.txt", "big.txt", "more.txt", "x.txt"}
			}
		}
		b, _ = json.Marshal(r)
	}
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
			"--allowedTools Read,Edit,Write,Bash,Glob,Grep,Skill --thinking-display summarized --model m --effort e --x y",
		"codex": `exec --json -C <clone> --sandbox workspace-write -o <log>.final.md -c model="m" ` +
			`-c model_reasoning_effort="e" --x y <brief>`,
		"opencode": "run --pure --format json --dir <clone> -m m --variant e --x y <brief>",
		"pi": "-p --mode json --no-extensions --no-skills --no-prompt-templates --no-context-files " +
			"--skill <clone>/.atm/skills/ponytail --model m --thinking e --x y <brief>",
		"cursor": "-p --output-format stream-json --trust --force --workspace <clone> --model m --x y <brief>",
	}
	for harness, want := range cases {
		t.Run(harness, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			call := filepath.Join(t.TempDir(), "call.json")
			t.Setenv("FAKE_AGENT_CALL", call)
			t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
			var out bytes.Buffer
			effort := map[bool]string{false: "e"}[harness == "cursor"] // cursor's goes in its model
			err := Run("t", []string{"--harness", harness, "--model", "m", "--effort", effort, "--harness-arg", "--x",
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
			if want := map[string]int{"claude": 5, "pi": 5, "cursor": 6}[harness]; len(lines) != cmp.Or(want, 4) {
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

type reachCase struct{ name, harness, stream, why string }

func bashEvent(harness, command, description string) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	switch harness {
	case "pi":
		desc := ""
		if description != "" {
			desc = `,"description":` + q(description)
		}
		return `{"type":"tool_execution_start","toolCallId":"c1","toolName":"bash","args":{"command":` +
			q(command) + desc + `}}` + "\n"
	case "opencode":
		desc := ""
		if description != "" {
			desc = `,"description":` + q(description)
		}
		return `{"type":"tool_use","part":{"callID":"c1","tool":"bash","state":{"status":"completed","input":{"command":` +
			q(command) + desc + `}}}}` + "\n"
	default:
		const prefix = `{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"c1","name":"Bash","input":{"command":`
		return prefix +
			q(command) + `}}]}}` + "\n"
	}
}

func TestRunDiesWhenTheAgentReachesOutsideItsClone(t *testing.T) {
	for _, cases := range [][]reachCase{piReachCases(), shellReachCases(), codexReachCases(), otherReachCases()} {
		runReachCases(t, cases)
	}
}

func TestRunRejectsSavedShellPayloadAcrossEvents(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	write := "cat > payload.sh <<'EOF'\n#!/bin/sh\ncurl https://example.com\nEOF\n"
	stream := bashEvent("claude", write, "") + bashEvent("claude", "sh payload.sh", "")
	t.Setenv("FAKE_AGENT_STREAM", stream)
	var out bytes.Buffer
	err := Run("t", []string{"--harness", "claude", issueFile(t, issue)}, &out, io.Discard)
	want := "the agent reached outside its clone: Bash " + write
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want the writer rejected before its later shell use, got %v", err)
	}
}

func piReachCases() []reachCase {
	curl := `curl -sL -m 30 https://www.example.com/assets/index.js -o "$TMPDIR/x.js"`
	const why = "the agent reached outside its clone: "
	return []reachCase{
		{"pi curl", "pi", bashEvent("pi", curl, ""), why + "bash " + curl},
		{"pi wrapped curl", "pi", bashEvent("pi", `env curl ""`, ""),
			why + `bash env curl ""`},
		{"pi command with description", "pi", bashEvent("pi", `echo ready; curl "$URL"`, "Check what's happening"),
			why + `bash echo ready; curl "$URL"`},
		{"pi description url ignored", "pi",
			bashEvent("pi", "pnpm dev --url http://localhost:5173", "https://example.com"), ""},
		{"local variable port", "claude", bashEvent("claude", "pnpm dev --url http://localhost:$PORT", ""), ""},
		{"local userinfo", "claude", bashEvent("claude", "pnpm dev --url http://user@localhost:3000", ""), ""},
		{"local ipv6", "claude", bashEvent("claude", "pnpm dev --url http://[::1]:8080", ""), ""},
		{"userinfo cannot hide remote host", "claude",
			bashEvent("claude", "git ls-remote https://localhost:80@www.example.com/repo.git", ""),
			why + "Bash git ls-remote https://localhost:80@www.example.com/repo.git"},
	}
}

func claudeEvent(tool, input string) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":` +
		q(tool) + `,"input":` + input + `}]}}` + "\n"
}

func shellReachCases() []reachCase {
	claude := claudeEvent
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	const why = "the agent reached outside its clone: "
	return []reachCase{
		{"env curl", "claude", claude("Bash", `{"command":`+q(`env curl "$PROD_URL"`)+`}`),
			why + `Bash env curl "$PROD_URL"`},
		{"wrapped curl", "claude", claude("Bash", `{"command":`+q(`env FOO=bar sudo command exec curl -s a`)+`}`),
			why + "Bash env FOO=bar sudo command exec curl -s a"},
		{"newline curl", "claude", claude("Bash", `{"command":`+q("printf ok\ncurl \"$URL\"")+`}`),
			why + "Bash printf ok\ncurl \"$URL\""},
		{"command separated from description", "claude",
			bashEvent("claude", `echo ready; curl "$URL"`, "Check what's happening"),
			why + `Bash echo ready; curl "$URL"`},
		{"assigned curl", "claude", claude("Bash", `{"command":`+q(`FOO=bar curl "$URL"`)+`}`),
			why + `Bash FOO=bar curl "$URL"`},
		{"chained curl", "claude", claude("Bash", `{"command":"x && curl -s a"}`),
			why + "Bash x && curl -s a"},
		{"semicolon curl", "claude", claude("Bash", `{"command":"x; curl -s a"}`),
			why + "Bash x; curl -s a"},
		{"or curl", "claude", claude("Bash", `{"command":"x || curl -s a"}`),
			why + "Bash x || curl -s a"},
		{"pipe curl", "claude", claude("Bash", `{"command":"x | curl -s a"}`),
			why + "Bash x | curl -s a"},
		{"substitution curl", "claude", claude("Bash", `{"command":`+q(`echo $(curl -s a)`)+`}`),
			why + `Bash echo $(curl -s a)`},
		{"curl argument after substitution", "claude", claude("Bash", `{"command":`+q(`echo $(date) curl`)+`}`), ""},
		{"curl argument after backticks", "claude", claude("Bash", `{"command":`+q("echo `date` curl")+`}`), ""},
		{"backtick curl", "claude", claude("Bash", `{"command":`+q("echo `curl -s a`")+`}`),
			why + "Bash echo `curl -s a`"},
		{"shell wrapped wget", "claude", claude("Bash", `{"command":`+q(`sh -c "wget b"`)+`}`),
			why + `Bash sh -c "wget b"`},
		{"expanded url", "claude", claude("Bash", `{"command":`+q(`node -e "fetch('https://example.com/$ASSET')"`)+`}`),
			why + `Bash node -e "fetch('https://example.com/$ASSET')"`},
		{"expanded nested curl", "claude", claude("Bash", `{"command":`+q(`sh -c "curl https://example.com/$ASSET"`)+`}`),
			why + `Bash sh -c "curl https://example.com/$ASSET"`},
		{"claude reads another run", "claude", claude("Read", `{"file_path":"{root}/.atm/runs/6-2/worker.jsonl"}`),
			why + "Read {root}/.atm/runs/6-2/worker.jsonl"},
		{"codex MCP reads another run", "codex", codexEvent(codexReadArguments()),
			why + "read_file {root}/.atm/runs/6-2/worker.jsonl"},
		{"codex file change inside clone", "codex", codexEvent(
			`"type":"file_change","changes":[{"path":"<clone>/a.txt","kind":"update"}],"status":"completed"`), ""},
		{"codex file change outside clone", "codex", codexEvent(
			`"type":"file_change","changes":[{"path":"{root}/.atm/runs/6-2/report.json","kind":"update"}],"status":"completed"`),
			why + "file_change {root}/.atm/runs/6-2/report.json"},
	}
}

func codexReachCases() []reachCase {
	const why = "the agent reached outside its clone: "
	return []reachCase{
		{"codex lists another clone", "codex", `{"type":"item.started","item":{"id":"c1","type":"command_execution",` +
			`"command":"ls {root}/.atm/clones/other"}}` + "\n", why + "shell ls {root}/.atm/clones/other"},
	}
}

func otherReachCases() []reachCase {
	const why = "the agent reached outside its clone: "
	return append(moreReachCases(claudeEvent, why), []reachCase{{
		"opencode fetches", "opencode", `{"type":"tool_use","part":{"callID":"c1","tool":"bash","state":` +
			`{"status":"completed","input":{"command":"node -e \"fetch('https://example.com')\""}}}}` + "\n",
		why + `bash node -e "fetch('https://example.com')"`},
	}...)
}

func codexEvent(item string) string {
	return `{"type":"item.completed","item":{"id":"c1",` + item + `}}` + "\n"
}

func codexReadArguments() string {
	return `"type":"mcp_tool_call","server":"filesystem","tool":"read_file",` +
		`"arguments":{"path":"{root}/.atm/runs/6-2/worker.jsonl"}`
}

func moreReachCases(claude func(string, string) string, why string) []reachCase {
	return []reachCase{
		{"opencode command with description", "opencode",
			bashEvent("opencode", `echo ready; curl "$URL"`, "Check what's happening"), why + `bash echo ready; curl "$URL"`},
		{"opencode description url ignored", "opencode",
			bashEvent("opencode", "pnpm dev --url http://localhost:5173", "https://example.com"), ""},
		{"inside", "claude", insideStream(claude), ""},
		{"pi writes a follow-up test with relative imports", "pi", `{"type":"tool_execution_start","toolCallId":"c1",` +
			`"toolName":"write","args":{"path":".atm/follow-ups/1/packages/p/src/a/web/form.test.tsx","content":` +
			`"import { rule } from '../../card/rules';\nimport { cfg } from '../../../../w/src/config';\n"}}` + "\n", ""},
		{"pi edit text naming the repository", "pi", `{"type":"tool_execution_start","toolCallId":"c1",` +
			`"toolName":"edit","args":{"path":"a.ts","edits":[{"oldText":"x","newText":"// see {root}/.atm/runs"}]}}` +
			"\n", ""},
		{"claude read content still checks reach", "claude", claude("Read",
			`{"file_path":"src/a.ts","content":"{root}/.atm/runs/6-2/worker.jsonl"}`),
			why + "Read {root}/.atm/runs/6-2/worker.jsonl src/a.ts"},
		{"claude writes outside its clone", "claude", claude("Write",
			`{"file_path":"{root}/.atm/runs/6-2/report.json","content":"{}"}`),
			why + "Write {root}/.atm/runs/6-2/report.json"},
		{"claude edit destination outside its clone", "claude", claude("Edit",
			`{"file_path":"{root}/.atm/runs/6-2/report.json","old_string":"x","new_string":"y"}`),
			why + "Edit {root}/.atm/runs/6-2/report.json"},
	}
}

func insideStream(claude func(string, string) string) string {
	return claude("Read", `{"file_path":"{clone}/AGENTS.md"}`) +
		claude("Bash", `{"command":"rg curl ."}`) +
		claude("Bash", `{"command":"rg wget ."}`) +
		claude("Bash", `{"command":"pnpm dev --url http://localhost:5173","description":"https://example.com"}`) +
		claude("Bash", `{"command":"pnpm add zod && pnpm dev --url http://localhost:5173"}`) +
		claude("Bash", `{"command":"pnpm dev --url http://localhost:5173; echo ready"}`) +
		claude("Read", `{"file_path":"{home}/go/pkg/mod/golang.org/x/mod@v0.20.0/go.mod"}`) +
		claude("Read", `{"file_path":"/archive{root}/file"}`) +
		claude("Write", `{"file_path":"src/a.test.ts","content":"import { b } from '../../b';\n"}`) +
		claude("Edit", `{"file_path":"src/a.ts","old_string":"x","new_string":"import { c } from '../../c';"}`)
}

func runReachCases(t *testing.T, cases []reachCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "https://example.com/owner/repo.git", atmYAML)
			r := strings.NewReplacer("{root}", gitT(t, root, "rev-parse", "--show-toplevel"),
				"{home}", filepath.ToSlash(os.Getenv("HOME")))
			t.Setenv("FAKE_AGENT_STREAM", r.Replace(c.stream))
			var out bytes.Buffer
			err := Run("t", []string{"--harness", c.harness, issueFile(t, issue)}, &out, io.Discard)
			if c.why == "" {
				if err != nil {
					t.Fatalf("a run that stayed in its clone failed: %v", err)
				}
				t.Logf("ATM accepted %s", c.name)
				return
			}
			if err == nil || !strings.Contains(err.Error(), r.Replace(c.why)) {
				t.Fatalf("want an error naming %q, got %v", r.Replace(c.why), err)
			}
			t.Logf("ATM rejected tool call: %s", err)
			if end, _, _ := agentStep(t, &out); end["state"] != "failed" || end["error"] != err.Error() {
				t.Fatalf("end event: %v", end)
			}
		})
	}
}

func TestRunReportsReachBeforeAgentFailure(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	t.Setenv("FAKE_AGENT", "fail")
	const prefix = `{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"c1","name":"Bash","input":{"command":`
	stream := prefix +
		q("curl https://example.com") + `}}]}}` + "\n"
	t.Setenv("FAKE_AGENT_STREAM", stream)
	var out bytes.Buffer
	err := Run("t", []string{"--harness", "claude", issueFile(t, issue)}, &out, io.Discard)
	want := "exit code 3; the agent reached outside its clone: Bash curl https://example.com"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want reach failure to take precedence, got %v", err)
	}
}

func assertFailedCleanup(t *testing.T, root string, end map[string]any, err error) {
	t.Helper()
	if err == nil || end["state"] != "failed" || end["error"] != err.Error() {
		t.Fatalf("end event: %v; error: %v", end, err)
	}
	if left := clones(t, root); len(left) != 0 {
		t.Fatalf("a failed run left its clone: %v", left)
	}
}

func TestRunDiesWhenTheAgentFails(t *testing.T) {
	cases := []struct {
		harness, mode, why string
		args               []string
	}{
		{harness: "claude", mode: "fail", why: "exit code 3"},
		{harness: "pi", mode: "no-report", why: "exit code 0; agent left no report"},
		{harness: "codex", mode: "no-report", why: "exit code 0; agent left no report"},
		{harness: "opencode", mode: "no-test-file", why: "test_file"},
		{harness: "claude", mode: "no-skill", why: "ponytail"},
		{harness: "codex", mode: "no-skill", why: "ponytail"},
		{harness: "opencode", mode: "no-skill", why: "ponytail"},
		{harness: "pi", mode: "no-skill", why: "ponytail"},
		{harness: "claude", mode: "skill-error", why: "ponytail"},
		{harness: "codex", mode: "skill-error", why: "ponytail"},
		{harness: "opencode", mode: "skill-error", why: "ponytail"},
		{harness: "pi", mode: "skill-error", why: "ponytail"},
		{harness: "claude", mode: "wrong-skill", why: "ponytail"},
		{harness: "codex", mode: "wrong-skill", why: "ponytail"},
		{harness: "opencode", mode: "wrong-skill", why: "ponytail"},
		{harness: "pi", mode: "wrong-skill", why: "ponytail"},
		{harness: "claude", why: "KEY=VALUE", args: []string{"--env", "NOVALUE"}},
		{harness: "cursor", mode: "no-skill", why: "agent left no proof it used the ponytail skill"},
		{harness: "cursor", mode: "skill-error", why: "ponytail"},
		{harness: "cursor", mode: "wrong-skill", why: "ponytail"},
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
			end, _, _ := agentStep(t, &out)
			assertFailedCleanup(t, root, end, err)
		})
	}
}

// Recorded from Quote's runs of 13 Oct 2026: a relative path is the agent's cwd's, which a cd moved inside the clone.
func TestReachAcceptsRelativePathsAfterACdInsideTheClone(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	command := "cd packages/journey && cat ../../README.md"
	t.Setenv("FAKE_AGENT_STREAM", bashEvent("claude", command, ""))
	t.Setenv("FAKE_AGENT_WORK", "mkdir -p packages/journey && echo clone-local > README.md && ("+
		command+") > relative-read.txt && grep -qx clone-local relative-read.txt && "+fixWork)
	var out bytes.Buffer
	if err := Run("t", []string{"--harness", "claude", issueFile(t, issue)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	end, _, _ := agentStep(t, &out)
	if end["state"] != "passed" {
		t.Fatalf("agent step: %v", end)
	}
	t.Logf("ATM agent step: %s; %s read clone-local", end["state"], command)
}
