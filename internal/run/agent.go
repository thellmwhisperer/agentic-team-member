package run

import (
	"bytes"
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"mvdan.cc/sh/v3/syntax"
)

// skills are the ponytail skills (github.com/DietrichGebert/ponytail, MIT) and task-slicing:
// the implementing agent loads ponytail, the slop detector ponytail-review, and the contract reviewer both
// ponytail-review and task-slicing.
//
//go:embed ponytail/SKILL.md ponytail-review/SKILL.md task-slicing/SKILL.md
var skills embed.FS

// skillDirs is where each harness finds a project skill; pi finds it where --skill points, under .atm/.
var skillDirs = map[string]string{"claude": ".claude/skills", "codex": ".agents/skills",
	"opencode": ".opencode/skills", "pi": ".atm/skills", "cursor": ".cursor/skills"}

// tools is all claude needs for a unit.
const tools = "Read,Edit,Write,Bash,Glob,Grep,Skill"

// agent is the coding agent of the run: its harness, model and effort, the --harness-arg and --env of the run,
// the repository it runs in and the run's directory, where its logs go.
type agent struct {
	config.Agent
	args, env []string
	root, dir string
	watcher   string        // the run's atm watch, which atm run --watcher names
	timeout   time.Duration // .atm.yaml's agent_timeout, 0 for agentTimeout
}

// flags adds the agent's flags to fs.
func (a *agent) flags(fs *flag.FlagSet) {
	fs.StringVar(&a.Harness, "harness", "", "agent CLI: claude, codex, opencode, pi, cursor or hermes")
	fs.StringVar(&a.Model, "model", "", "the agent's model")
	fs.StringVar(&a.Effort, "effort", "", "the agent's effort")
	fs.Func("harness-arg", "an argument for the agent CLI, before the brief; repeatable", func(v string) error {
		a.args = append(a.args, v)
		return nil
	})
	fs.Func("env", "KEY=VALUE in the agent's environment; repeatable", func(v string) error {
		if !strings.Contains(v, "=") {
			return fmt.Errorf("want KEY=VALUE, got %q", v)
		}
		a.env = append(a.env, v)
		return nil
	})
}

// argv is the agent's command line, in its most minimal mode, and what goes on its stdin: claude reads the
// brief there, the others take it as the last argument, but an ACP agent, whose prompt it is. final is where
// codex writes its last message.
func (a agent) argv(clone, skill, final, brief string) (argv []string, stdin string) {
	if acp, ok := config.ACP[a.Harness]; ok {
		return append(slices.Clone(acp.Argv), a.args...), brief
	}
	with := func(argv []string, value string, args ...string) []string {
		if value == "" {
			return argv
		}
		return append(argv, args...)
	}
	switch a.Harness {
	case "claude":
		argv = []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--setting-sources", "project",
			"--strict-mcp-config", "--permission-mode", "acceptEdits", "--tools", tools, "--allowedTools", tools,
			"--thinking-display", "summarized"} // else claude's thinking blocks come empty
		argv = with(with(argv, a.Model, "--model", a.Model), a.Effort, "--effort", a.Effort)
		return append(argv, a.args...), brief
	case "codex":
		argv = []string{"codex", "exec", "--json", "-C", clone, "--sandbox", "workspace-write", "-o", final}
		argv = with(argv, a.Model, "-c", "model="+strconv.Quote(a.Model))
		argv = with(argv, a.Effort, "-c", "model_reasoning_effort="+strconv.Quote(a.Effort))
	case "opencode":
		argv = []string{"opencode", "run", "--pure", "--format", "json", "--dir", clone}
		argv = with(with(argv, a.Model, "-m", a.Model), a.Effort, "--variant", a.Effort)
	case "cursor":
		argv = []string{"cursor-agent", "-p", "--output-format", "stream-json", "--trust", "--force", "--workspace", clone}
		argv = with(argv, a.Model, "--model", a.Model)
	case "pi":
		argv = []string{"pi", "-p", "--mode", "json", "--no-extensions", "--no-skills", "--no-prompt-templates",
			"--no-context-files", "--no-session", "--skill", skill}
		argv = with(with(argv, a.Model, "--model", a.Model), a.Effort, "--thinking", a.Effort)
	}
	return append(append(argv, a.args...), brief), ""
}

// errSilent cancels an agent that said nothing for agentSilence.
var errSilent = errors.New("silent")

// agentSilence is how long the agent may write nothing, on stdout or stderr, before ATM stops it.
var agentSilence = 10 * time.Minute

// run runs the agent on brief in clone with skill and returns its report, which check accepts. Once it ran, a
// failure puts in ev how it ended, its exit, and the last lines it wrote, its tail.
func (a agent) run(clone, name, skill, brief string, check func(map[string]any) error) (map[string]any, error) {
	path := filepath.Join(a.dir, name+".jsonl")
	ev := map[string]any{"log": path}
	dir, err := placeSkill(clone, a.Harness, skill)
	if err != nil {
		return ev, err
	}
	if _, acp := config.ACP[a.Harness]; acp { // no skill directory: the brief has the skill, the report its proof
		brief, check = withSkill(brief, skill), withoutSkill(check)
	}
	final := strings.TrimSuffix(path, ".jsonl") + ".final.md"
	argv, stdin := a.argv(clone, dir, final, brief)
	s, exit, cause, err := a.exec(clone, skill, path, argv, stdin)
	if s == nil {
		return ev, err
	}
	if a.Harness == "codex" {
		b, _ := os.ReadFile(final) // none is no report
		s.final = string(b)
	}
	s.finish()
	fail := func(err error) (map[string]any, error) {
		if exit != "" {
			ev["exit"], ev["tail"] = exit, strings.Join(s.tail, "\n")
		}
		if s.failure != "" && s.reach == "" {
			err = fmt.Errorf("%w; cursor: %s", err, s.failure)
		}
		return ev, err
	}
	switch {
	case s.reach != "":
		return fail(fmt.Errorf("%s; the agent reached outside its clone: %s; log: %s", exit, s.reach, path))
	case cause != "":
		return fail(fmt.Errorf("%s; log: %s", cause, path))
	case err != nil:
		return fail(fmt.Errorf("agent: %w; log: %s", err, path))
	case s.failure != "":
		return fail(fmt.Errorf("%s; log: %s", exit, path))
	case !s.used:
		return fail(fmt.Errorf("%s; agent left no proof it used the %s skill; log: %s", exit, skill, path))
	}
	if ev["report"], err = finalReport(name, s.final, check); err != nil {
		return fail(fmt.Errorf("%s; %w; log: %s", exit, err, path))
	}
	return ev, nil
}

// strict is the review each agent run, by its name without its unit number, whose final message must be a
// single JSON object, nothing around it.
var strict = map[string]string{"contract-review": "contract review", "scope-review-unit": "scope review"}

// finalReport is the report in the agent's final message, which check accepts.
func finalReport(name, final string, check func(map[string]any) error) (map[string]any, error) {
	review, ok := strict[strings.TrimRight(name, "0123456789")]
	if !ok {
		return report(final, check)
	}
	var r map[string]any
	if json.Unmarshal([]byte(final), &r) != nil {
		return nil, fmt.Errorf("agent left no report: invalid %s: want a single JSON object", review)
	}
	return r, check(r)
}

// relative is live with each tool call's detail relative to clone: a path in it without clone, clone itself ".".
func relative(clone string, live func(map[string]any)) func(map[string]any) {
	return func(ev map[string]any) {
		if d, ok := ev["detail"].(string); ok {
			ev["detail"] = relativeDetail(relativeDetail(d, clone), filepath.ToSlash(clone))
		}
		live(ev)
	}
}

func relativeDetail(detail, clone string) string {
	var out strings.Builder
	for start := 0; start < len(detail); {
		i := strings.Index(detail[start:], clone)
		if i < 0 {
			out.WriteString(detail[start:])
			break
		}
		i += start
		end := i + len(clone)
		beforeOK := i == 0 || !clonePathRune(lastRune(detail[:i]))
		next := rune(0)
		if end < len(detail) {
			next, _ = utf8.DecodeRuneInString(detail[end:])
		}
		afterOK := end == len(detail) || next == '/' || next == '\\' || !clonePathRune(next)
		if beforeOK && afterOK {
			out.WriteString(detail[start:i])
			if i == 0 && (next == '/' || next == '\\') {
				if end+1 == len(detail) {
					out.WriteByte('.')
				}
				start = end + 1
			} else {
				out.WriteByte('.')
				start = end
			}
		} else {
			out.WriteString(detail[start:end])
			start = end
		}
	}
	return out.String()
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

func clonePathRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("_-.\\/", r)
}

// exec runs argv in clone, stdin on its stdin, in its own process group, and returns what its lines said, and
// how it ended, as ended says; a nil stream when it never got to run. Every line it writes goes to the log at
// path, in the run's directory, which outlives the clone, and its stderr to <name>.stderr.txt next to it as
// well. The timeout, agentSilence without a line, SIGINT and SIGTERM kill the group.
func (a agent) exec(clone, skill, path string, argv []string, stdin string) (*stream, string, string, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = f.Close() }()
	stderr, err := os.Create(strings.TrimSuffix(path, ".jsonl") + ".stderr.txt")
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = stderr.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	limit := cmp.Or(a.timeout, agentTimeout)
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ctx, hush := context.WithCancelCause(ctx)
	defer hush(nil)
	quiet := time.AfterFunc(agentSilence, func() { hush(errSilent) })
	defer quiet.Stop()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdin, cmd.WaitDelay = clone, strings.NewReader(stdin), time.Second
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CLAUDE_CODE_CHILD_SESSION=") // set, the agent's transcripts are off
	}), a.env...)
	if a.Harness == "claude" { // else a background command outlives its turn, and the run its report
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1")
	}
	if err := inheritCloneLease(cmd, clone); err != nil {
		return nil, "", "", err
	}
	s := &stream{skill: skill, live: relative(clone, liveIn(clone)), root: a.root, clone: clone,
		heard: func() { quiet.Reset(agentSilence) }}
	if err := s.talk(cmd, f, a.Harness, stdin); err != nil {
		return nil, "", "", err
	}
	err = s.read(cmd, f, stderr, a.Harness)
	quiet.Stop()
	limitText := Human(limit)
	if a.timeout != 0 {
		limitText = a.timeout.String()
	}
	exit, cause := ended(ctx, cmd.ProcessState, limitText)
	return s.acpEnded(exit, cause, err)
}

// ended is how the agent run under ctx, bounded by limit, ended, its exit, "" when it never ran, and the cause
// its step fails with, "" when it exited 0.
func ended(ctx context.Context, p *os.ProcessState, limitText string) (exit, cause string) {
	switch {
	case errors.Is(context.Cause(ctx), errSilent):
		return "silent for " + Human(agentSilence), "the agent said nothing for " + Human(agentSilence) +
			"; ATM stopped it"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		exit = "timed out after " + limitText
	case ctx.Err() != nil:
		exit = "interrupted"
	case p == nil:
		return "", ""
	case p.Success():
		return "exit code 0", ""
	case p.ExitCode() >= 0:
		exit = "exit code " + strconv.Itoa(p.ExitCode())
	default:
		exit = "signal " + strings.TrimPrefix(p.String(), "signal: ")
	}
	return exit, exit
}

// report is the agent's report, the last JSON object of its final message, which check accepts.
func report(final string, check func(map[string]any) error) (map[string]any, error) {
	r, _ := lastObject(final)
	if r == nil {
		return nil, errors.New("agent left no report: no JSON object in its final message")
	}
	return r, check(r)
}

// testFile checks the implementing agent's report r names its test file: ATM never guesses it.
func testFile(r map[string]any) error {
	if _, ok := r["test_file"].(string); !ok {
		return errors.New("agent report has no test_file")
	}
	return nil
}

// placeSkill puts skill where the harness finds it in clone, outside git, records the clone as the agent finds it,
// and returns the skill's directory.
func placeSkill(clone, harness, skill string) (string, error) {
	if _, acp := config.ACP[harness]; acp { // its skill goes in its brief
		return "", before(clone)
	}
	rel := skillDirs[harness] + "/" + skill
	dir := filepath.Join(clone, filepath.FromSlash(rel))
	b, err := skills.ReadFile(skill + "/SKILL.md")
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "SKILL.md"), b, 0o644)
	}
	if err == nil {
		err = exclude(clone, "/"+rel+"/")
	}
	if err == nil {
		err = before(clone)
	}
	return dir, err
}

// exclude makes git in clone ignore pattern, even when the target does not.
func exclude(clone, pattern string) error {
	path := filepath.Join(clone, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString("\n" + pattern + "\n")
	return errors.Join(err, f.Close())
}

// stream is what the agent's lines said: its final message, whether it used its skill, and its first
// prohibited path or network call. Live gets what each line tells the screen.
type stream struct {
	final, skill string
	loads        []string // the ids of the agent's calls that load its skill
	root, clone  string
	reach        string
	used         bool
	live         func(map[string]any)
	pending      string
	thought      string   // cursor's thinking deltas until completed
	failure      string   // cursor's error result, even when the process exits 0
	tail         []string // the last 20 lines the agent wrote
	heard        func()   // each line calls it
	// An ACP agent's: its prompt, its stdin until its prompt's reply, the reply's stopReason or the failed call,
	// and the chunks of one thought or message so far.
	prompt, stop   string
	rpc            io.WriteCloser
	chunk, chunkOf string
}

// read runs cmd and writes each line of its stdout and stderr to log, as it is when JSON, else as a JSON
// string, reading the stream of harness from it. Its stderr goes to stderr too, as it comes.
func (s *stream) read(cmd *exec.Cmd, log, stderr io.Writer, harness string) error {
	attach, closeGroup, err := ownGroup(cmd)
	if err != nil {
		return err
	}
	defer closeGroup()
	var mu sync.Mutex
	var werr error
	emit := func(line string, stdout bool) {
		line = string(bytes.TrimSpace([]byte(line)))
		mu.Lock()
		defer mu.Unlock()
		s.heard()
		s.tail = append(s.tail, line)
		s.tail = s.tail[max(0, len(s.tail)-20):]
		b := []byte(line)
		if !json.Valid(b) {
			b, _ = json.Marshal(line)
		} else {
			s.see(harness, b)
		}
		if _, err := log.Write(append(b, '\n')); werr == nil {
			werr = err
		}
		if stdout && s.rpc != nil {
			s.say(cmd, log, s.answer(b))
		}
	}
	out, errout := &lineWriter{fn: func(l string) { emit(l, true) }}, &lineWriter{fn: func(l string) { emit(l, false) }}
	cmd.Stdout, cmd.Stderr = out, io.MultiWriter(stderr, errout)
	err = cmd.Start()
	if err == nil {
		if err = attach(); err != nil {
			_ = cmd.Cancel()
			err = errors.Join(err, cmd.Wait())
		} else {
			err = cmd.Wait()
		}
	}
	// Exited but failed, or left something holding its output: kill what it left, whatever its exit.
	if err != nil && cmd.ProcessState != nil {
		_ = cmd.Cancel() // its group may be empty already, which is no failure
	}
	if errors.Is(err, exec.ErrWaitDelay) { // exited 0, and only left something holding its output
		err = nil
	}
	out.flush()
	errout.flush()
	return errors.Join(err, werr)
}

// event is the fields of every harness's events that say what its final message is and that it used its
// skill.
type event struct {
	Type    string
	Result  json.RawMessage // claude and pi
	Message struct {
		Role    string
		Content []block
	}
	ToolName   string `json:"toolName"` // pi
	ToolCallID string `json:"toolCallId"`
	IsError    bool   `json:"isError"`
	Args       json.RawMessage
	Item       struct { // codex
		ID, Type, Command, Text, Status, Tool string
		Arguments                             json.RawMessage `json:"arguments"`
		Changes                               []struct {
			Path string
		} `json:"changes"`
		ExitCode         *int   `json:"exit_code"`
		AggregatedOutput string `json:"aggregated_output"`
	}
	Part struct { // opencode
		CallID     string `json:"callID"`
		Tool, Text string
		State      struct {
			Status, Output, Error string
			Input                 json.RawMessage
		}
	}
	Subtype, Text string                     // cursor
	CallID        string                     `json:"call_id"`
	ToolCall      map[string]json.RawMessage `json:"tool_call"`
	ResultIsError bool                       `json:"is_error"`
}

// cursorTool ignores metadata beside the <kind>ToolCall key. Its output is a shell's stdout or a read's content,
// a failure's stderr before its stdout.
func (e event) cursorTool() (kind string, args json.RawMessage, passed bool, output string) {
	for k, v := range e.ToolCall {
		if kind, ok := strings.CutSuffix(k, "ToolCall"); ok {
			var c struct {
				Args   json.RawMessage
				Result struct {
					Success json.RawMessage
					Failure struct{ Stdout, Stderr string }
				}
			}
			_ = json.Unmarshal(v, &c)
			var out struct{ Stdout, Content string }
			_ = json.Unmarshal(c.Result.Success, &out)
			return kind, c.Args, c.Result.Success != nil && string(c.Result.Success) != "null",
				cmp.Or(c.Result.Failure.Stderr, c.Result.Failure.Stdout, out.Stdout, out.Content)
		}
	}
	return "", nil, false, ""
}

type block struct {
	Type, Name, Text, Thinking, ID string
	ToolUseID                      string          `json:"tool_use_id"`
	IsError                        bool            `json:"is_error"`
	Input, Arguments               json.RawMessage // claude's and pi's
	Content                        json.RawMessage // claude's tool_result: a string or text blocks
}

// see reads one JSON line of harness's stream.
func (s *stream) see(harness string, line []byte) {
	if _, acp := config.ACP[harness]; acp {
		s.show(s.acp(line))
		return
	}
	var e event
	_ = json.Unmarshal(line, &e) // a field of another type is left empty, the rest is read
	if harness == "cursor" {
		s.cursorState(&e)
	}
	final, ends := e.final(harness)
	if ends {
		s.final = final
	}
	if s.pending != "" {
		s.flush(e.Type == "result" && (harness == "claude" && ends || harness == "cursor"))
	}
	s.loads = append(s.loads, e.skillLoads(harness, s.skill, s.clone)...)
	s.reach = cmp.Or(s.reach, outside(e.calls(harness), s.root, s.clone))
	s.show(e.activity(harness))
}

// show tells the screen evs, what the agent says once the stream knows whether it ends with a report.
func (s *stream) show(evs []map[string]any) {
	for _, ev := range evs {
		id, _ := ev["id"].(string)
		s.used = s.used || ev["result"] == "passed" && slices.Contains(s.loads, id)
		if says, ok := ev["says"].(string); ok {
			if s.pending != "" {
				s.flush(false)
			}
			if _, at := lastObject(says); at < 0 {
				s.live(ev)
			} else {
				s.pending = says
			}
		} else {
			if s.pending != "" {
				s.flush(false)
			}
			s.live(ev)
		}
	}
}

// cursorState joins a thought and keeps an error result even if the process exits successfully.
func (s *stream) cursorState(e *event) {
	if e.Type == "thinking" {
		if e.Subtype == "delta" {
			s.thought += e.Text
		}
		if e.Subtype == "completed" {
			e.Text, s.thought = s.thought, ""
		}
	}
	if e.Type == "result" && e.ResultIsError {
		var text string
		_ = json.Unmarshal(e.Result, &text)
		s.failure = cmp.Or(s.failure, text, "error result")
	}
}

func (s *stream) finish() {
	s.show(s.chunkEnd())
	s.flush(true)
	if s.prompt != "" { // an ACP agent's report proves its skill
		r, _ := lastObject(s.final)
		s.used = r["skill"] == s.skill
	}
}

func (s *stream) flush(report bool) {
	if s.pending == "" {
		return
	}
	text := s.pending
	if report && strings.HasSuffix(strings.TrimSpace(s.final), strings.TrimSpace(text)) {
		if _, at := lastObject(text); at >= 0 {
			text = strings.TrimSpace(text[:at])
		}
	}
	s.pending = ""
	if text != "" {
		s.live(map[string]any{"says": text})
	}
}

// skillLoads is the ids of e's calls that load skill, by its exact name: a Skill tool call for claude, a read
// of its SKILL.md for pi and codex, a skill call for opencode, a read or cat of its SKILL.md for cursor. A load
// proves the agent used skill once its result is not an error.
func (e event) skillLoads(harness, skill, clone string) []string {
	read := func(path string) bool { return strings.Contains(filepath.ToSlash(path), "/"+skill+"/SKILL.md") }
	switch {
	case harness == "claude":
		return e.skillCalls(skill)
	case harness == "codex" && e.Item.Type == "command_execution" && read(e.Item.Command):
		return []string{e.Item.ID}
	case harness == "opencode" && e.Type == "tool_use" && e.Part.Tool == "skill" &&
		names(e.Part.State.Input, "name", skill):
		return []string{e.Part.CallID}
	case harness == "pi" && e.Type == "tool_execution_start" && e.ToolName == "read" && read(detail(e.Args)):
		return []string{e.ToolCallID}
	case harness == "cursor":
		return e.cursorSkillLoads(skill, clone)
	}
	return nil
}

func (e event) cursorSkillLoads(skill, clone string) []string {
	if e.Type != "tool_call" || e.Subtype != "started" {
		return nil
	}
	kind, args, _, _ := e.cursorTool()
	var input struct{ Path, Command string }
	_ = json.Unmarshal(args, &input)
	target := filepath.Join(clone, skillDirs["cursor"], skill, "SKILL.md")
	exact := func(path string) bool {
		if !filepath.IsAbs(path) {
			path = filepath.Join(clone, path)
		}
		return filepath.Clean(path) == target
	}
	if kind == "read" && exact(input.Path) || kind == "shell" && cursorCatReads(input.Command, exact) {
		return []string{e.CallID}
	}
	return nil
}

// cursorCatReads proves only a direct literal cat of the installed skill, not arbitrary shell execution.
func cursorCatReads(command string, exact func(string) bool) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return false
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Negated || stmt.Background || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 {
		return false
	}
	var words []string
	for _, arg := range call.Args {
		word, literal := literalWord(arg)
		if !literal {
			return false
		}
		words = append(words, word)
	}
	if len(words) == 3 && words[1] == "--" {
		words = append(words[:1], words[2])
	}
	return len(words) == 2 && (words[0] == "cat" || words[0] == "/bin/cat") && exact(words[1])
}

// skillCalls is the ids of claude's Skill tool calls in e that load skill.
func (e event) skillCalls(skill string) (ids []string) {
	for _, b := range e.Message.Content {
		if b.Type == "tool_use" && b.Name == "Skill" && names(b.Input, "skill", skill) {
			ids = append(ids, b.ID)
		}
	}
	return ids
}

// names says whether a tool call's input has name as its key.
func names(input json.RawMessage, key, name string) bool {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	return in[key] == name
}

// final is the final message e ends, if it ends one; codex's is in its -o file instead.
func (e event) final(harness string) (string, bool) {
	switch {
	case harness == "claude" && e.Type == "result":
		var text string
		_ = json.Unmarshal(e.Result, &text)
		return text, true
	case harness == "opencode" && e.Type == "text":
		return e.Part.Text, true
	case (harness == "pi" && e.Type == "message_end" || harness == "cursor" && e.Type == "assistant") &&
		e.Message.Role == "assistant":
		var texts []string
		for _, b := range e.Message.Content {
			if b.Type == "text" {
				texts = append(texts, b.Text)
			}
		}
		return strings.Join(texts, "\n"), true
	}
	return "", false
}

// lastObject is the last top-level JSON object in text and where it starts, nil and -1 when there is none.
func lastObject(text string) (map[string]any, int) {
	var last map[string]any
	at := -1
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		var v map[string]any
		if dec.Decode(&v) == nil {
			last, at, i = v, i, i+int(dec.InputOffset())-1
		}
	}
	return last, at
}

// activity is what e tells the screen the agent does: what it says, its thinking, and its tool calls, each by
// its id, with its result once known: running, passed or failed.
func (e event) activity(harness string) []map[string]any {
	return map[string]func(event) []map[string]any{"claude": event.blocks, "pi": event.pi, "codex": event.codex,
		"opencode": event.opencode, "cursor": event.cursor}[harness](e)
}

func (e event) cursor() []map[string]any {
	switch {
	case e.Type == "thinking" && e.Subtype == "completed" && strings.TrimSpace(e.Text) != "":
		return []map[string]any{{"thinking": e.Text}}
	case e.Type == "assistant":
		return e.blocks()
	case e.Type == "tool_call" && (e.Subtype == "started" || e.Subtype == "completed"):
		kind, args, passed, output := e.cursorTool()
		if kind == "" {
			return nil
		}
		r := cmp.Or(map[string]string{"started": "running"}[e.Subtype], result(!passed))
		return []map[string]any{summed(toolCall(e.CallID, kind, detail(args), r), output)}
	}
	return nil
}

func (e event) pi() []map[string]any {
	switch e.Type {
	case "message_end":
		return e.blocks()
	case "tool_execution_start":
		return []map[string]any{toolCall(e.ToolCallID, e.ToolName, detail(e.Args), "running")}
	case "tool_execution_end":
		var r struct {
			IsError bool
			Content json.RawMessage
		}
		_ = json.Unmarshal(e.Result, &r)
		return []map[string]any{summed(toolCall(e.ToolCallID, e.ToolName, "", result(e.IsError || r.IsError)),
			contentText(r.Content))}
	}
	return nil
}

func (e event) codex() []map[string]any {
	switch {
	case e.Item.Type == "reasoning" && e.Type == "item.completed":
		return []map[string]any{{"thinking": e.Item.Text}}
	case e.Item.Type == "agent_message" && e.Type == "item.completed":
		return says(e.Item.Text)
	case e.Item.Type != "command_execution":
		return nil
	case e.Type == "item.started":
		return []map[string]any{toolCall(e.Item.ID, "shell", e.Item.Command, "running")}
	}
	failed := e.Item.Status == "failed" || e.Item.ExitCode != nil && *e.Item.ExitCode != 0
	return []map[string]any{summed(toolCall(e.Item.ID, "shell", e.Item.Command, result(failed)), e.Item.AggregatedOutput)}
}

func (e event) opencode() []map[string]any {
	switch e.Type {
	case "reasoning":
		return []map[string]any{{"thinking": e.Part.Text}}
	case "text":
		return says(e.Part.Text)
	case "tool_use":
		s := e.Part.State
		r := map[string]string{"completed": "passed", "error": "failed"}[s.Status]
		return []map[string]any{summed(toolCall(e.Part.CallID, e.Part.Tool, detail(s.Input), cmp.Or(r, "running")),
			cmp.Or(s.Output, s.Error))}
	}
	return nil
}

// blocks is the activity of claude's and pi's message content.
func (e event) blocks() (evs []map[string]any) {
	for _, b := range e.Message.Content {
		switch b.Type {
		case "text":
			if e.Type == "assistant" || e.Message.Role == "assistant" { // not a skill's text or a tool result
				evs = append(evs, says(b.Text)...)
			}
		case "thinking":
			if strings.TrimSpace(b.Thinking) != "" { // claude's, without its summary, is only a signature
				evs = append(evs, map[string]any{"thinking": b.Thinking})
			}
		case "tool_use":
			evs = append(evs, toolCall(b.ID, b.Name, detail(b.Input), "running"))
		case "toolCall": // pi's, which its tool_execution_start may never follow
			evs = append(evs, toolCall(b.ID, b.Name, detail(b.Arguments), "running"))
		case "tool_result":
			evs = append(evs, summed(toolCall(b.ToolUseID, "", "", result(b.IsError)), contentText(b.Content)))
		}
	}
	return evs
}

// says is the agent saying text, before the stream knows whether it ends with a report.
func says(text string) []map[string]any {
	if text = strings.TrimSpace(text); text == "" {
		return nil
	}
	return []map[string]any{{"says": text}}
}

// toolCall is a tool call's live event; a later one with its id and empty fields leaves those as they were.
func toolCall(id, name, detail, result string) map[string]any {
	return map[string]any{"id": id, "tool": name, "detail": detail, "result": result}
}

// summed is tool call ev with its summary: the first line of its output, or of its error, when it has one.
func summed(ev map[string]any, output string) map[string]any {
	line, _, _ := strings.Cut(output, "\n")
	if line = strings.TrimSpace(line); line != "" {
		ev["summary"] = line
	}
	return ev
}

// contentText is a tool result's content: a string, or the text of its blocks.
func contentText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []block
	_ = json.Unmarshal(content, &blocks)
	for _, b := range blocks {
		text += b.Text
	}
	return text
}

func result(failed bool) string {
	return map[bool]string{true: "failed", false: "passed"}[failed]
}

// detail is what a tool call's input is about, in one line: its command, path, pattern or skill.
func detail(input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	for _, k := range []string{"command", "file_path", "filePath", "path", "pattern", "skill", "url", "query"} {
		if s, ok := in[k].(string); ok {
			line, _, _ := strings.Cut(s, "\n")
			return line
		}
	}
	return ""
}

// use is one of the agent's tool calls: its tool, every string of its arguments, and its shell command.
type use struct{ tool, args, command string }

// calls is e's tool calls: claude's tool_use and pi's toolCall blocks, pi's tool executions, codex's commands
// and opencode's tool uses.
func (e event) calls(harness string) (cs []use) {
	switch {
	case harness == "cursor" && e.Type == "tool_call":
		kind, args, _, _ := e.cursorTool()
		return []use{{kind, strs(args, writesFile(kind)), shellCommand(kind, args)}}
	case harness == "codex":
		return codexCalls(e.Item)
	case harness == "opencode" && e.Type == "tool_use":
		args := strs(e.Part.State.Input, writesFile(e.Part.Tool))
		return []use{{e.Part.Tool, args, shellCommand(e.Part.Tool, e.Part.State.Input)}}
	case harness == "pi" && e.Type == "tool_execution_start":
		args := strs(e.Args, writesFile(e.ToolName))
		return []use{{e.ToolName, args, shellCommand(e.ToolName, e.Args)}}
	case e.Type != "assistant" && e.Type != "message_end": // not a pi message_update's partial arguments
		return nil
	}
	for _, b := range e.Message.Content {
		if b.Type == "tool_use" || b.Type == "toolCall" {
			input := b.Input
			if len(input) == 0 {
				input = b.Arguments
			}
			args := strs(b.Input, writesFile(b.Name)) + strs(b.Arguments, writesFile(b.Name))
			cs = append(cs, use{b.Name, args, shellCommand(b.Name, input)})
		}
	}
	return cs
}

func codexCalls(item struct {
	ID, Type, Command, Text, Status, Tool string
	Arguments                             json.RawMessage         `json:"arguments"`
	Changes                               []struct{ Path string } `json:"changes"`
	ExitCode                              *int                    `json:"exit_code"`
	AggregatedOutput                      string                  `json:"aggregated_output"`
}) []use {
	switch item.Type {
	case "command_execution":
		return []use{{"shell", item.Command, item.Command}}
	case "mcp_tool_call":
		return []use{{item.Tool, strs(item.Arguments, writesFile(item.Tool)), ""}}
	case "file_change":
		paths := make([]string, 0, len(item.Changes))
		for _, change := range item.Changes {
			paths = append(paths, change.Path)
		}
		return []use{{"file_change", strings.Join(paths, " "), ""}}
	default:
		return nil
	}
}

func shellCommand(tool string, input json.RawMessage) string {
	if !strings.EqualFold(tool, "bash") && !strings.EqualFold(tool, "shell") {
		return ""
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(input, &fields)
	var command string
	_ = json.Unmarshal(fields["command"], &command)
	return command
}

func writesFile(tool string) bool {
	switch strings.ToLower(tool) {
	case "write", "edit", "write_file", "edit_file":
		return true
	default:
		return false
	}
}

// strs is every string in a tool call's input, by key, joined by spaces.
func strs(input json.RawMessage, write bool) string {
	written := map[string]bool{"content": true, "old_string": true, "new_string": true, "oldString": true,
		"newString": true, "oldText": true, "newText": true, "fileText": true}
	var walk func(any) []string
	walk = func(v any) (out []string) {
		switch v := v.(type) {
		case string:
			return []string{v}
		case []any:
			for _, x := range v {
				out = append(out, walk(x)...)
			}
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(v)) {
				if !write || !written[k] {
					out = append(out, walk(v[k])...)
				}
			}
		}
		return out
	}
	var in any
	_ = json.Unmarshal(input, &in)
	return strings.Join(walk(in), " ")
}

var (
	link = regexp.MustCompile("https?://[^\\s\"'`;|&)]+")
	sep  = "\\s\"'`;|&()<>,="
)

// outside is the first of calls that reaches outside clone, as its tool and its first 200 characters: one that
// names a path under root not under clone, or a shell command that runs curl or wget or names a URL off this
// machine. A path outside root, the module cache's, passes; root "/" is every absolute path.
func outside(calls []use, root, clone string) string {
	if len(calls) == 0 {
		return ""
	}
	root, clone = filepath.ToSlash(root), filepath.ToSlash(clone)
	pattern := regexp.QuoteMeta(root) + "(/[^" + sep + "]*)?"
	if root == "/" {
		pattern = "/[^" + sep + "]*"
	}
	quoted := regexp.QuoteMeta(root)
	if root != "/" {
		quoted += "/"
	}
	paths := regexp.MustCompile(`"` + quoted + `[^"]*"` + `|'` + quoted + `[^']*'|` + pattern)
	for _, c := range calls {
		if reachesPath(c.args, root, clone, paths) || c.command != "" && fetchesAt(c.command, clone) {
			r := []rune(c.args)
			return c.tool + " " + string(r[:min(len(r), 200)])
		}
	}
	return ""
}

func reachesPath(args, root, clone string, paths *regexp.Regexp) bool {
	text := filepath.ToSlash(args)
	for _, m := range paths.FindAllStringIndex(text, -1) {
		if root == "" || m[0] > 0 && !strings.ContainsRune(" \t\r\n\"'`;|&()<>,=", rune(text[m[0]-1])) ||
			m[1] < len(text) && !strings.ContainsRune(" \t\r\n\"'`;|&()<>,=/", rune(text[m[1]])) {
			continue
		}
		p := text[m[0]:m[1]]
		if p[0] == '\'' || p[0] == '"' {
			p = p[1 : len(p)-1]
		}
		p = path.Clean(p)
		if p != clone && !strings.HasPrefix(p, clone+"/") {
			return true
		}
	}
	return false
}

// fetches says whether a shell command runs curl or wget or names a URL off this machine.
func fetches(command string) bool {
	return fetchesAt(command, "")
}

func fetchesAt(command, clone string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		if badURL(command) {
			return true
		}
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return false
		}
		word := strings.Trim(fields[0], "\"'`;|&()")
		return word == "curl" || word == "wget"
	}
	checked := []byte(command)
	if len(file.Stmts) > 0 {
		maskQuotedGoTestData(checked, file.Stmts, clone)
	}
	if badURL(string(checked)) {
		return true
	}
	return containsFetchCall(file)
}

func containsFetchCall(file *syntax.File) bool {
	unsafe := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok && fetchCall(call) {
			unsafe = true
		}
		return !unsafe
	})
	return unsafe
}

func maskQuotedGoTestData(checked []byte, stmts []*syntax.Stmt, clone string) {
	if len(stmts) == 0 {
		return
	}
	data, output, appended := quotedGoTestData(stmts[0])
	if data == nil || !goTestContinuation(stmts[1:]) {
		return
	}
	body := bytes.ReplaceAll([]byte(data.Lit()), []byte("\r\n"), []byte("\n"))
	start := int(data.Pos().Offset())
	if start < 0 || start > len(checked) {
		return
	}
	end := start + len(body)
	for extra := 0; extra <= bytes.Count(body, []byte("\n")) && end+extra <= len(checked); extra++ {
		end = start + len(body) + extra
		if bytes.Equal(bytes.ReplaceAll(checked[start:end], []byte("\r\n"), []byte("\n")), body) {
			break
		}
	}
	if end < start || end > len(checked) ||
		!bytes.Equal(bytes.ReplaceAll(checked[start:end], []byte("\r\n"), []byte("\n")), body) ||
		!goTestSource(body, appended && existingTestingImport(clone, output)) {
		return
	}
	clear(checked[start:end])
}

func quotedGoTestData(stmt *syntax.Stmt) (*syntax.Word, string, bool) {
	if stmt == nil || stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) != 2 {
		return nil, "", false
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 1 || call.Args[0].Lit() != "cat" || len(call.Assigns) != 0 {
		return nil, "", false
	}
	var data *syntax.Word
	output := ""
	appended := false
	data, output, appended = classifyGoTestRedirs(stmt.Redirs)
	if data != nil && output != "" {
		return data, output, appended
	}
	return nil, "", false
}

func classifyGoTestRedirs(redirs []*syntax.Redirect) (*syntax.Word, string, bool) {
	var data *syntax.Word
	output := ""
	appended := false
	for _, redir := range redirs {
		if redir.N != nil {
			return nil, "", false
		}
		switch redir.Op {
		case syntax.Hdoc:
			_, literal := literalWord(redir.Word)
			if literal && redir.Word.Lit() == "" {
				data = redir.Hdoc
			}
		case syntax.RdrOut, syntax.AppOut:
			if literalGoTestOutput(redir.Word) {
				output, _ = literalWord(redir.Word)
				appended = redir.Op == syntax.AppOut
			}
		}
	}
	return data, output, appended
}

func literalGoTestOutput(word *syntax.Word) bool {
	name, literal := literalShellWord(word)
	clean := path.Clean(name)
	return literal && name != "" && !path.IsAbs(name) && !filepath.IsAbs(name) &&
		clean != ".." && !strings.HasPrefix(clean, "../") && strings.HasSuffix(clean, "_test.go")
}

func literalShellWord(word *syntax.Word) (string, bool) {
	name, literal := literalWord(word)
	if !literal {
		return "", false
	}
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(part.Value, "*?[{}~") {
				return "", false
			}
		case *syntax.BraceExp, *syntax.ExtGlob:
			return "", false
		}
	}
	return name, true
}

func goTestSource(src []byte, existingTestingImport bool) bool {
	f, err := goparser.ParseFile(token.NewFileSet(), "fixture_test.go", src, goparser.ParseComments)
	if err != nil {
		f, err = goparser.ParseFile(token.NewFileSet(), "fixture_test.go", append([]byte("package run\n"), src...),
			goparser.ParseComments)
	}
	if err != nil || f.Name == nil {
		return false
	}
	testingImported := existingTestingImport
	for _, imp := range f.Imports {
		if testingImport(imp) {
			testingImported = true
			break
		}
	}
	if !testingImported {
		return false
	}
	test := false
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && goTestFunc(fn) {
			test = true
		}
	}
	return test
}

func existingTestingImport(clone, output string) bool {
	if clone == "" {
		return false
	}
	src, err := os.ReadFile(filepath.Join(clone, filepath.FromSlash(output)))
	if err != nil {
		return false
	}
	f, err := goparser.ParseFile(token.NewFileSet(), output, src, 0)
	if err != nil {
		return false
	}
	for _, imp := range f.Imports {
		if testingImport(imp) {
			return true
		}
	}
	return false
}

func testingImport(imp *ast.ImportSpec) bool {
	importPath, err := strconv.Unquote(imp.Path.Value)
	return err == nil && imp.Name == nil && importPath == "testing"
}

func goTestFunc(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || fn.Type.TypeParams != nil || fn.Type.Results != nil && len(fn.Type.Results.List) != 0 ||
		fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) > 1 {
		return false
	}
	name := fn.Name.Name
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if len(name) > len("Test") {
		r, _ := utf8.DecodeRuneInString(name[len("Test"):])
		if unicode.IsLower(r) {
			return false
		}
	}
	param, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	typ, ok := param.X.(*ast.SelectorExpr)
	if !ok || typ.Sel.Name != "T" {
		return false
	}
	packageName, ok := typ.X.(*ast.Ident)
	return ok && packageName.Name == "testing"
}

func goTestContinuation(stmts []*syntax.Stmt) bool {
	if len(stmts) == 0 {
		return true
	}
	if len(stmts) != 1 {
		return false
	}
	stmt := stmts[0]
	if call, ok := stmt.Cmd.(*syntax.CallExpr); ok {
		return literalGoTestCall(stmt, call, false)
	}
	pipe, ok := stmt.Cmd.(*syntax.BinaryCmd)
	return ok && pipe.Op == syntax.Pipe && !stmt.Negated && !stmt.Background && !stmt.Coprocess &&
		len(stmt.Redirs) == 0 && literalGoTestCall(pipe.X, callOf(pipe.X), true) && literalTail(pipe.Y)
}

func callOf(stmt *syntax.Stmt) *syntax.CallExpr {
	if stmt == nil {
		return nil
	}
	call, _ := stmt.Cmd.(*syntax.CallExpr)
	return call
}

func literalGoTestCall(stmt *syntax.Stmt, call *syntax.CallExpr, stderrToStdout bool) bool {
	if !literalGoTestCallShape(stmt, call) {
		return false
	}
	if !goTestRedirsMatch(stmt.Redirs, stderrToStdout) {
		return false
	}
	for i, arg := range call.Args {
		word, literal := literalShellWord(arg)
		if !literal || (i == 0 && word != "go") || (i == 1 && word != "test") {
			return false
		}
	}
	return true
}

func goTestRedirsMatch(redirs []*syntax.Redirect, stderrToStdout bool) bool {
	if !stderrToStdout {
		return len(redirs) == 0
	}
	if len(redirs) != 1 {
		return false
	}
	redir := redirs[0]
	target, literal := literalShellWord(redir.Word)
	return redir.Op == syntax.DplOut && redir.N != nil && redir.N.Value == "2" && literal && target == "1"
}

func literalGoTestCallShape(stmt *syntax.Stmt, call *syntax.CallExpr) bool {
	return stmt != nil && call != nil && !stmt.Negated && !stmt.Background && !stmt.Coprocess &&
		len(call.Assigns) == 0 && len(call.Args) >= 2
}

func literalTail(stmt *syntax.Stmt) bool {
	if stmt == nil || stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) != 0 {
		return false
	}
	call := callOf(stmt)
	if call == nil || len(call.Assigns) != 0 || len(call.Args) != 2 {
		return false
	}
	name, nameLiteral := literalShellWord(call.Args[0])
	count, countLiteral := literalShellWord(call.Args[1])
	return nameLiteral && countLiteral && name == "tail" && count == "-20"
}

func literalWord(word *syntax.Word) (string, bool) {
	var text strings.Builder
	var walk func([]syntax.WordPart) bool
	walk = func(parts []syntax.WordPart) bool {
		for _, part := range parts {
			switch part := part.(type) {
			case *syntax.Lit:
				text.WriteString(part.Value)
			case *syntax.SglQuoted:
				text.WriteString(part.Value)
			case *syntax.DblQuoted:
				if !walk(part.Parts) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	literal := walk(word.Parts)
	return text.String(), literal
}

func badURL(word string) bool {
	for _, l := range link.FindAllString(word, -1) {
		scheme := strings.Index(l, "://")
		if scheme < 0 {
			return true
		}
		authority := l[scheme+3:]
		if end := strings.IndexAny(authority, "/?#"); end >= 0 {
			authority = authority[:end]
		}
		if user := strings.LastIndexByte(authority, '@'); user >= 0 {
			authority = authority[user+1:]
		}
		host := authority
		if strings.HasPrefix(host, "[") {
			end := strings.IndexByte(host, ']')
			if end < 0 || host[end+1:] != "" && !strings.HasPrefix(host[end+1:], ":") {
				return true
			}
			host = host[1:end]
		} else if end := strings.IndexByte(host, ':'); end >= 0 {
			host = host[:end]
		}
		if !slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, strings.ToLower(host)) {
			return true
		}
	}
	return false
}

func fetchCall(call *syntax.CallExpr) bool {
	args := call.Args
	for i := 0; i < len(args); {
		word, literal := literalWord(args[i])
		if !literal {
			return false
		}
		switch word {
		case "env":
			i++
			for i < len(args) {
				if !strings.Contains(literalFragments(args[i]), "=") {
					break
				}
				i++
			}
		case "sudo", "command", "exec":
			i++
		case "sh", "bash":
			if i+2 < len(args) {
				flag, flagLiteral := literalWord(args[i+1])
				return flagLiteral && flag == "-c" && fetches(literalFragments(args[i+2]))
			}
			return false
		default:
			return word == "curl" || word == "wget"
		}
	}
	return false
}

func literalFragments(word *syntax.Word) string {
	text, _ := literalWord(word)
	return text
}
