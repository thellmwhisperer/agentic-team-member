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
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// skills are the ponytail skills (github.com/DietrichGebert/ponytail, MIT): the implementing agent loads
// ponytail, the slop detector and the scope reviewer ponytail-review.
//
//go:embed ponytail/SKILL.md ponytail-review/SKILL.md
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
	scratch   string // a read-only review's scratch directory, outside the clone: the one path its calls may reach
	root, dir string
	follow    bool          // atm run --follow: the launching process watches the run itself
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
			"--no-context-files", "--skill", skill}
		argv = with(with(argv, a.Model, "--model", a.Model), a.Effort, "--thinking", a.Effort)
	}
	return append(append(argv, a.args...), brief), ""
}

// resumeArgv continues the harness's session with prompt, the rejected report sent back once. ok is false when
// the harness keeps no session ATM can continue.
func (a agent) resumeArgv(clone, skill, final, session, prompt string) (argv []string, stdin string, ok bool) {
	argv, stdin = a.argv(clone, skill, final, prompt)
	flag := []string{"--resume", session}
	switch {
	case session == "":
		return nil, "", false
	case a.Harness == "claude":
		return append(argv, flag...), stdin, true
	case a.Harness == "codex":
		return codexResume(argv, session), "", true
	case a.Harness == "pi":
		flag[0] = "--session"
	case a.Harness != "cursor":
		return nil, "", false
	}
	last := argv[len(argv)-1]
	return append(append(slices.Clone(argv[:len(argv)-1]), flag...), last), "", true
}

// codexResume is `codex exec resume`: it takes neither -C nor --sandbox (codex-cli 0.155.1), so the clone is the
// process working directory and the sandbox is -c sandbox_mode="workspace-write". It keeps the first call's
// --json, -o, -m and -c model settings.
func codexResume(argv []string, session string) []string {
	out := []string{argv[0], argv[1], "resume"}
	args := argv[2 : len(argv)-1]
	for i := 0; i < len(args); i++ {
		if args[i] == "-C" || args[i] == "--cd" || args[i] == "--sandbox" {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return append(out, "-c", `sandbox_mode="workspace-write"`, session, argv[len(argv)-1])
}

// run runs the agent on brief in clone with skill and returns its report, which check accepts. A report check
// rejects is sent back once, in the same session, with that error; the step passes with the new report, and ev
// records the rejection. A second rejection fails as the first did, as a reportError. Once it ran, a failure
// puts in ev how it ended, its exit, and the last lines it wrote, its tail.
func (a agent) run(clone, name, skill, brief string, check func(map[string]any) error) (map[string]any, error) {
	path := filepath.Join(a.dir, name+".jsonl")
	ev := map[string]any{"log": path}
	dir, err := placeSkill(clone, a.Harness, skill)
	if err != nil {
		return ev, err
	}
	if _, acp := config.ACP[a.Harness]; acp { // no skill directory: the brief has the skill
		brief = withSkill(brief, skill)
	}
	final := strings.TrimSuffix(path, ".jsonl") + ".final.md"
	argv, stdin := a.argv(clone, dir, final, brief)
	s, exit, cause, err := a.exec(clone, path, argv, stdin, false)
	invalid, err := a.reported(name, path, final, exit, cause, err, s, check, ev)
	if invalid == nil {
		return ev, err
	}
	ev["rejected"] = invalid.Error()
	f, logErr := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o666)
	if logErr != nil {
		return ev, errors.Join(err, logErr)
	}
	logErr = json.NewEncoder(f).Encode(map[string]any{"type": "report_rejected", "error": invalid.Error()})
	if logErr = errors.Join(logErr, f.Close()); logErr != nil {
		return ev, errors.Join(err, logErr)
	}
	prompt := "Your final message was rejected: " + invalid.Error() +
		". Reply with only the JSON object the brief asks for, nothing else."
	argv, stdin, ok := a.resumeArgv(clone, dir, final, s.session, prompt)
	if !ok {
		argv, stdin = a.argv(clone, dir, final, s.final+"\n"+prompt)
	}
	s, exit, cause, err = a.exec(clone, path, argv, stdin, true)
	invalid, err = a.reported(name, path, final, exit, cause, err, s, check, ev)
	if invalid != nil {
		return ev, reportError{err}
	}
	if err != nil {
		return ev, err
	}
	delete(ev, "exit")
	delete(ev, "tail")
	return ev, nil
}

// reportError is why an agent step failed when the agent ended cleanly but left its report missing or invalid.
type reportError struct{ error }

func (e reportError) Unwrap() error { return e.error }

// reported reads the run's report into ev. invalid is a validation error, so the caller can return it once;
// fatal is why the step fails, that error wrapped as today when the report is what failed.
func (a agent) reported(name, path, final, exit, cause string, runErr error, s *stream,
	check func(map[string]any) error, ev map[string]any) (invalid, fatal error) {
	if s == nil {
		return nil, runErr
	}
	if a.Harness == "codex" {
		b, _ := os.ReadFile(final) // none is no report
		s.final = string(b)
	}
	s.finish()
	ev["agent_model"] = s.model
	if s.session != "" {
		ev["session_id"] = s.session
	}
	fail := func(err error) error {
		if exit != "" {
			ev["exit"], ev["tail"] = exit, strings.Join(s.tail, "\n")
		}
		if s.failure != "" && s.reach == "" {
			if a.Harness == "pi" {
				err = fmt.Errorf("%s; pi: %s; log: %s", exit, s.failure, path)
			} else {
				err = fmt.Errorf("%w; cursor: %s", err, s.failure)
			}
		}
		return err
	}
	switch {
	case s.reach != "":
		return nil, fail(fmt.Errorf("%s; the agent reached outside its clone: %s; log: %s", exit, s.reach, path))
	case cause != "":
		return nil, fail(fmt.Errorf("%s; log: %s", cause, path))
	case runErr != nil:
		return nil, fail(fmt.Errorf("agent: %w; log: %s", runErr, path))
	case s.failure != "":
		return nil, fail(fmt.Errorf("%s; log: %s", exit, path))
	}
	report, err := finalReport(name, s.final, check)
	ev["report"] = report
	if err != nil {
		return err, fail(fmt.Errorf("%s; %w; log: %s", exit, err, path))
	}
	return nil, nil
}

// strict is the review each agent run, by its name without its unit number, whose final message must be a
// single JSON object, nothing around it.
var strict = map[string]string{"scope-review-unit": "scope review"}

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
// well. The timeout, SIGINT and SIGTERM kill the group.
func (a agent) exec(
	clone, path string, argv []string, stdin string, appendLog bool,
) (*stream, string, string, error) {
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendLog {
		flags = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(path, flags, 0o666)
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = f.Close() }()
	stderr, err := os.OpenFile(strings.TrimSuffix(path, ".jsonl")+".stderr.txt", flags, 0o666)
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = stderr.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	limit := cmp.Or(a.timeout, agentTimeout)
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdin, cmd.WaitDelay = clone, strings.NewReader(stdin), time.Second
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CLAUDE_CODE_CHILD_SESSION=") || // set, the agent's transcripts are off
			a.scratch != "" && strings.HasPrefix(kv, "TMPDIR=") // the review's scratch is its TMPDIR
	}), a.env...)
	if a.Harness == "claude" { // else a background command outlives its turn, and the run its report
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1")
	}
	s := &stream{live: relative(clone, liveIn(clone)), clone: clone,
		scratch: a.scratch}
	if err := s.talk(cmd, f, a.Harness, stdin); err != nil {
		return nil, "", "", err
	}
	err = s.read(cmd, f, stderr, a.Harness)
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

// stream is what the agent's lines said: its final message and its first write outside its clone.
// Live gets what each line tells the screen.
type stream struct {
	final   string
	model   string // first model named by this invocation's stream; never the requested model
	clone   string
	scratch string // the read-only review's directory outside the clone its calls may write in
	reach   string
	live    func(map[string]any)
	pending string
	thought string   // cursor's thinking deltas until completed
	failure string   // cursor's error result or pi's unrecovered model error, even when the process exits 0
	tail    []string // the last 20 lines the agent wrote
	session string   // the harness session, so a rejected report is returned to it
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
	Model   string
	Result  json.RawMessage // claude and pi
	Message struct {
		Role                     string
		Model, Provider          string
		StopReason, ErrorMessage string // pi
		Content                  []block
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
	s.noteSession(line)
	if _, acp := config.ACP[harness]; acp {
		s.show(s.acp(line))
		return
	}
	var e event
	_ = json.Unmarshal(line, &e) // a field of another type is left empty, the rest is read
	if s.model == "" {
		s.model = e.agentModel(harness)
	}
	if harness == "cursor" {
		s.cursorState(&e)
	}
	final, ends := e.final(harness)
	if ends {
		s.final = final
		if harness == "pi" {
			switch e.Message.StopReason {
			case "error":
				s.failure = e.Message.ErrorMessage
			case "stop", "length", "toolUse":
				s.failure = ""
			}
		}
	}
	if s.pending != "" {
		s.flush(e.Type == "result" && (harness == "claude" && ends || harness == "cursor"))
	}
	s.reach = cmp.Or(s.reach, outside(e.calls(harness), s.clone, s.scratch))
	s.show(e.activity(harness))
}

// agentModel reads only model metadata, never text or a tool's arguments.
func (e event) agentModel(harness string) string {
	switch harness {
	case "claude", "cursor":
		if e.Type == "system" && e.Subtype == "init" {
			return e.Model
		}
	case "pi":
		if e.Type == "message_end" && e.Message.Role == "assistant" && e.Message.Model != "" {
			if e.Message.Provider != "" {
				return e.Message.Provider + "/" + e.Message.Model
			}
			return e.Message.Model
		}
	case "codex", "opencode":
		return e.Model
	}
	return ""
}

// noteSession keeps the first session id the harness's stream names, so a rejected report returns to it.
func (s *stream) noteSession(line []byte) {
	var v map[string]any
	if s.session != "" || json.Unmarshal(line, &v) != nil {
		return
	}
	s.session, _ = v["session_id"].(string)
	if s.session == "" {
		s.session, _ = v["thread_id"].(string)
	}
	if s.session == "" && v["type"] == "session" {
		s.session, _ = v["id"].(string)
	}
}

// show tells the screen evs, what the agent says once the stream knows whether it ends with a report.
func (s *stream) show(evs []map[string]any) {
	for _, ev := range evs {
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
type use struct {
	tool, args, command string
	paths               []string
}

func toolUse(tool string, input json.RawMessage) use {
	paths := strs(input, writesFile(tool))
	return use{tool: tool, args: strings.Join(paths, " "), command: shellCommand(tool, input), paths: paths}
}

// calls is e's tool calls: claude's tool_use and pi's toolCall blocks, pi's tool executions, codex's commands
// and opencode's tool uses.
func (e event) calls(harness string) (cs []use) {
	switch {
	case harness == "cursor" && e.Type == "tool_call":
		kind, args, _, _ := e.cursorTool()
		return []use{toolUse(kind, args)}
	case harness == "codex":
		return codexCalls(e.Item)
	case harness == "opencode" && e.Type == "tool_use":
		return []use{toolUse(e.Part.Tool, e.Part.State.Input)}
	case harness == "pi" && e.Type == "tool_execution_start":
		return []use{toolUse(e.ToolName, e.Args)}
	case e.Type != "assistant" && e.Type != "message_end": // not a pi message_update's partial arguments
		return nil
	}
	for _, b := range e.Message.Content {
		if b.Type == "tool_use" || b.Type == "toolCall" {
			input := b.Input
			if len(input) == 0 {
				input = b.Arguments
			}
			paths := append(strs(b.Input, writesFile(b.Name)), strs(b.Arguments, writesFile(b.Name))...)
			cs = append(cs, use{tool: b.Name, args: strings.Join(paths, " "), command: shellCommand(b.Name, input),
				paths: paths})
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
		return []use{{tool: "shell", args: item.Command, command: item.Command}}
	case "mcp_tool_call":
		return []use{toolUse(item.Tool, item.Arguments)}
	case "file_change":
		paths := make([]string, 0, len(item.Changes))
		for _, change := range item.Changes {
			paths = append(paths, change.Path)
		}
		return []use{{tool: "file_change", args: strings.Join(paths, " "), paths: paths}}
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
func strs(input json.RawMessage, write bool) []string {
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
	return walk(in)
}

// outside is the first of calls that writes outside clone, as its tool and its first 200 characters: a write or
// edit tool, or a shell command, whose destination is an absolute path not under clone. Reading outside, and
// writing inside by any means, pass. scratch, when given, is the one directory outside clone those calls may
// write in: a read-only review's.
func outside(calls []use, clone string, scratch ...string) string {
	allowed := []string{filepath.ToSlash(clone)}
	if len(scratch) > 0 && scratch[0] != "" {
		allowed = append(allowed, filepath.ToSlash(scratch[0]))
	}
	for _, c := range calls {
		var dests []string
		switch {
		case c.command != "":
			dests = shellWrites(c.command)
		case writesFile(c.tool) || c.tool == "file_change" || c.tool == "delete" || c.tool == "move":
			dests = c.paths
		}
		for _, d := range dests {
			if escapes(d, allowed) {
				r := []rune(c.args)
				return c.tool + " " + string(r[:min(len(r), 200)])
			}
		}
	}
	return ""
}

var windowsAbs = regexp.MustCompile(`^[A-Za-z]:/`)

// escapes says whether p, a destination, is an absolute path, ~ or $HOME included, under none of allowed and
// not a device such as /dev/null.
func escapes(p string, allowed []string) bool {
	p = filepath.ToSlash(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return true
		}
		p = filepath.ToSlash(home) + p[1:]
	}
	if !path.IsAbs(p) && !windowsAbs.MatchString(p) {
		return false
	}
	p = path.Clean(p)
	if p == "/dev/null" || p == "/dev/stdout" || p == "/dev/stderr" {
		return false
	}
	for _, a := range allowed {
		if p == a || strings.HasPrefix(p, a+"/") {
			return false
		}
	}
	return true
}

// shellWrites is what command writes to, as far as its words are literal: each output redirection's target, and
// the paths a command that writes them names.
func shellWrites(command string) (out []string) {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrInOut, syntax.RdrAll, syntax.AppAll, syntax.DplOut:
				out = append(out, wordText(n.Word))
			}
		case *syntax.CallExpr:
			out = append(out, callWrites(n)...)
		}
		return true
	})
	return out
}

// callWrites is the paths call writes: every operand of tee, touch, mkdir, rm, rmdir, mv and the like, the last
// of cp, ln, install and rsync, sed's and perl's files when in place, and what sh -c's command writes.
func callWrites(call *syntax.CallExpr) []string {
	var words []string
	for _, a := range call.Args {
		words = append(words, wordText(a))
	}
	return commandWrites(words)
}

func commandWrites(words []string) []string {
	if len(words) == 0 {
		return nil
	}
	switch name := path.Base(words[0]); name {
	case "command", "exec", "env": // what the command it runs, past its options and assignments, writes
		var out []string
		for i := 1; i < len(words); i++ {
			out = append(out, commandWrites(words[i:])...)
		}
		return out
	case "sh", "bash", "zsh": // what the command -c gives writes
		var out []string
		for _, w := range words[1:] {
			out = append(out, shellWrites(w)...)
		}
		return out
	case "tee", "touch", "mkdir", "rm", "rmdir", "mv", "truncate", "unlink":
		return words[1:] // an option is never an absolute path
	case "cp", "ln", "install", "rsync":
		return copyWrites(name, words)
	case "curl", "wget":
		return downloadWrites(name, words)
	case "sed":
		return editWrites(words, sedOption)
	case "perl":
		return editWrites(words, perlOption)
	}
	return nil
}

// copyWrites is the target directory of cp, ln or install, or the last operand.
func copyWrites(name string, words []string) []string {
	var operands []string
	var target string
	targets := name != "rsync"
	for i := 1; i < len(words); i++ {
		a := words[i]
		switch {
		case targets && (a == "-t" || a == "--target-directory") && i+1 < len(words):
			target = words[i+1]
			i++
		case targets && strings.HasPrefix(a, "--target-directory="):
			target = strings.TrimPrefix(a, "--target-directory=")
		case targets && strings.HasPrefix(a, "-t") && len(a) > 2:
			target = a[2:]
		case !strings.HasPrefix(a, "-"):
			operands = append(operands, a)
		}
	}
	if target != "" {
		return []string{target}
	}
	return operands[max(0, len(operands)-1):]
}

// downloadWrites is the file curl -o or wget -O writes.
func downloadWrites(name string, words []string) []string {
	flag := "-o"
	if name == "wget" {
		flag = "-O"
	}
	var target string
	for i := 1; i+1 < len(words); i++ {
		if words[i] == flag {
			target = words[i+1]
			i++
		}
	}
	if target != "" {
		return []string{target}
	}
	return nil
}

// editWrites is the files sed or perl edits in place, past its script, option saying what each word is.
func editWrites(words []string, option func([]string) (n int, script, inPlace bool)) []string {
	var operands []string
	inPlace, script := false, false
	for i := 1; i < len(words); {
		n, s, edits := option(words[i:])
		if n == 0 {
			operands = append(operands, words[i])
			n = 1
		}
		script, inPlace = script || s, inPlace || edits
		i += n
	}
	if !inPlace {
		return nil
	}
	if !script && len(operands) > 0 {
		operands = operands[1:]
	}
	return operands
}

// sedOption is what w[0], a word of sed's, is: the words it takes, 0 for an operand, whether it gives the
// script and whether it edits in place.
func sedOption(w []string) (n int, script, inPlace bool) {
	a := w[0]
	switch {
	case scriptWords(w, true) > 0:
		return scriptWords(w, true), true, false
	case a == "-i" && len(w) > 1 && (w[1] == "" || w[1] == ".bak"):
		return 2, false, true
	case strings.HasPrefix(a, "-"):
		return 1, false, strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "--in-place")
	}
	return 0, false, false
}

// perlOption is sedOption for perl.
func perlOption(w []string) (n int, script, inPlace bool) {
	a := w[0]
	short := strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--")
	switch {
	case scriptWords(w, false) > 0:
		return scriptWords(w, false), true, false
	case short && strings.HasSuffix(a, "e") && len(w) > 1:
		return 2, true, strings.Contains(a, "i")
	case strings.HasPrefix(a, "-"):
		return 1, false, strings.HasPrefix(a, "--in-place") || short && strings.Contains(a, "i")
	}
	return 0, false, false
}

// scriptWords is how many words of w, from the first, give sed's script, or perl's when not sed: 0 when none.
func scriptWords(w []string, sed bool) int {
	a := w[0]
	switch {
	case (a == "-e" || a == "--expression" || sed && (a == "-f" || a == "--file")) && len(w) > 1:
		return 2
	case len(a) > 2 && (strings.HasPrefix(a, "-e") || sed && strings.HasPrefix(a, "-f")):
		return 1
	case strings.HasPrefix(a, "--expression=") || sed && strings.HasPrefix(a, "--file="):
		return 1
	}
	return 0
}

// wordText is word as the shell reads it, ~ and $HOME as the home directory and any other parameter or command
// left as $.
func wordText(word *syntax.Word) string {
	fields, _ := expand.Fields(&shellWords, word)
	return strings.Join(fields, " ")
}

var shellWords = expand.Config{
	Env: expand.FuncEnviron(func(name string) string {
		switch name {
		case "HOME":
			home, _ := os.UserHomeDir()
			return home
		case "IFS":
			return ""
		}
		return "$"
	}),
	CmdSubst: func(w io.Writer, _ *syntax.CmdSubst) error { _, err := io.WriteString(w, "$"); return err },
}
