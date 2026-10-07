package run

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// skills are the ponytail skills (github.com/DietrichGebert/ponytail, MIT): the implementing agent loads
// ponytail, the slop detector ponytail-review, always.
//
//go:embed ponytail/SKILL.md ponytail-review/SKILL.md
var skills embed.FS

// skillDirs is where each harness finds a project skill; pi finds it where --skill points, under .atm/.
var skillDirs = map[string]string{"claude": ".claude/skills", "codex": ".agents/skills",
	"opencode": ".opencode/skills", "pi": ".atm/skills"}

// tools is all claude needs for a unit.
const tools = "Read,Edit,Write,Bash,Glob,Grep,Skill"

// agent is the coding agent of the run: its harness, model and effort, the --harness-arg and --env of the run,
// and the run's directory, where its logs go.
type agent struct {
	config.Agent
	args, env []string
	dir       string
}

// flags adds the agent's flags to fs.
func (a *agent) flags(fs *flag.FlagSet) {
	fs.StringVar(&a.Harness, "harness", "", "agent CLI: claude, codex, opencode or pi")
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
// brief there, the others take it as the last argument. final is where codex writes its last message.
func (a agent) argv(clone, skill, final, brief string) (argv []string, stdin string) {
	with := func(argv []string, value string, args ...string) []string {
		if value == "" {
			return argv
		}
		return append(argv, args...)
	}
	switch a.Harness {
	case "claude":
		argv = []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--setting-sources", "project",
			"--strict-mcp-config", "--permission-mode", "acceptEdits", "--tools", tools, "--allowedTools", tools}
		argv = with(with(argv, a.Model, "--model", a.Model), a.Effort, "--effort", a.Effort)
		return append(argv, a.args...), brief
	case "codex":
		argv = []string{"codex", "exec", "--json", "-C", clone, "--sandbox", "workspace-write", "-o", final}
		argv = with(argv, a.Model, "-c", "model="+strconv.Quote(a.Model))
		argv = with(argv, a.Effort, "-c", "model_reasoning_effort="+strconv.Quote(a.Effort))
	case "opencode":
		argv = []string{"opencode", "run", "--pure", "--format", "json", "--dir", clone}
		argv = with(with(argv, a.Model, "-m", a.Model), a.Effort, "--variant", a.Effort)
	case "pi":
		argv = []string{"pi", "-p", "--mode", "json", "--no-extensions", "--no-skills", "--no-prompt-templates",
			"--no-context-files", "--no-session", "--skill", skill}
		argv = with(with(argv, a.Model, "--model", a.Model), a.Effort, "--thinking", a.Effort)
	}
	return append(append(argv, a.args...), brief), ""
}

// run runs the agent on brief in clone with skill, in its own process group, and returns its report, which
// check accepts. Every line it writes goes to <name>.jsonl in the run's directory, which outlives the clone.
// The timeout, SIGINT and SIGTERM kill the group.
func (a agent) run(clone, name, skill, brief string, check func(map[string]any) error) (map[string]any, error) {
	path := filepath.Join(a.dir, name+".jsonl")
	ev := map[string]any{"log": path}
	dir, err := placeSkill(clone, a.Harness, skill)
	if err != nil {
		return ev, err
	}
	f, err := os.Create(path)
	if err != nil {
		return ev, err
	}
	defer func() { _ = f.Close() }()
	final := strings.TrimSuffix(path, ".jsonl") + ".final.md"
	argv, stdin := a.argv(clone, dir, final, brief)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdin, cmd.WaitDelay = clone, strings.NewReader(stdin), time.Second
	if err := inheritCloneLease(cmd, clone); err != nil {
		return ev, err
	}
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CLAUDE_CODE_CHILD_SESSION=") // set, the agent's transcripts are off
	}), a.env...)
	ownGroup(cmd)
	s := stream{skill: skill, live: liveIn(clone)}
	err = s.read(cmd, f, a.Harness)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ev, fmt.Errorf("agent timed out after %s; log: %s", Human(agentTimeout), path)
	case ctx.Err() != nil:
		return ev, fmt.Errorf("agent interrupted; log: %s", path)
	case err != nil:
		return ev, fmt.Errorf("agent: %w; log: %s", err, path)
	case !s.used:
		return ev, fmt.Errorf("agent left no proof it used the %s skill; log: %s", skill, path)
	}
	if a.Harness == "codex" {
		b, _ := os.ReadFile(final) // none is no report
		s.final = string(b)
	}
	ev["report"], err = report(s.final, check)
	if err != nil {
		return ev, fmt.Errorf("%w; log: %s", err, path)
	}
	return ev, nil
}

// report is the agent's report, the last JSON object of its final message, which check accepts.
func report(final string, check func(map[string]any) error) (map[string]any, error) {
	r := lastObject(final)
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

// placeSkill puts skill where harness finds it in clone, outside git, and returns its directory.
func placeSkill(clone, harness, skill string) (string, error) {
	rel := skillDirs[harness] + "/" + skill
	dir := filepath.Join(clone, filepath.FromSlash(rel))
	b, err := skills.ReadFile(skill + "/SKILL.md")
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "SKILL.md"), b, 0o644)
	}
	if err != nil {
		return dir, err
	}
	return dir, exclude(clone, "/"+rel+"/")
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

// stream is what the agent's lines said: its final message and whether it used its skill. Live gets what
// each line tells the screen.
type stream struct {
	final, skill string
	used         bool
	live         func(map[string]any)
}

// read runs cmd and writes each line of its stdout and stderr to log, as it is when JSON, else as a JSON
// string, reading the stream of harness from it.
func (s *stream) read(cmd *exec.Cmd, log io.Writer, harness string) error {
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	done := make(chan error, 1)
	go func() {
		var werr error
		for r := bufio.NewReader(pr); ; {
			line, err := r.ReadBytes('\n')
			if line = bytes.TrimSpace(line); len(line) > 0 {
				if !json.Valid(line) {
					line, _ = json.Marshal(string(line))
				} else {
					s.see(harness, line)
				}
				if _, e := log.Write(append(line, '\n')); werr == nil {
					werr = e
				}
			}
			if err != nil {
				done <- werr
				return
			}
		}
	}()
	err := cmd.Start()
	if err == nil {
		err = cmd.Wait()
	}
	_ = pw.Close()
	return errors.Join(err, <-done)
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
		ID, Type, Command, Text, Status string
		ExitCode                        *int `json:"exit_code"`
	}
	Part struct { // opencode
		CallID     string `json:"callID"`
		Tool, Text string
		State      struct {
			Status string
			Input  json.RawMessage
		}
	}
}

type block struct {
	Type, Name, Text, Thinking, ID string
	ToolUseID                      string `json:"tool_use_id"`
	IsError                        bool   `json:"is_error"`
	Input                          json.RawMessage
}

// see reads one JSON line of harness's stream.
func (s *stream) see(harness string, line []byte) {
	var e event
	_ = json.Unmarshal(line, &e) // a field of another type is left empty, the rest is read
	s.used = s.used || e.usedSkill(harness, s.skill)
	for _, ev := range e.activity(harness) {
		s.live(ev)
	}
	if text, ok := e.final(harness); ok {
		s.final = text
	}
}

// usedSkill says whether e proves the agent used skill: a Skill tool call for claude, a read of its SKILL.md
// for pi and codex, a skill event for opencode.
func (e event) usedSkill(harness, skill string) bool {
	named := func(b []byte) bool { return bytes.Contains(b, []byte(skill)) }
	read := func(b []byte) bool { return named(b) && bytes.Contains(b, []byte("SKILL.md")) }
	switch harness {
	case "claude":
		return slices.ContainsFunc(e.Message.Content, func(b block) bool {
			return b.Type == "tool_use" && b.Name == "Skill" && named(b.Input)
		})
	case "codex":
		return e.Item.Type == "command_execution" && read([]byte(e.Item.Command))
	case "opencode":
		return e.Type == "tool_use" && e.Part.Tool == "skill" && named(e.Part.State.Input)
	case "pi":
		return e.Type == "tool_execution_start" && e.ToolName == "read" && read(e.Args)
	}
	return false
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
	case harness == "pi" && e.Type == "message_end" && e.Message.Role == "assistant":
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

// lastObject is the last top-level JSON object in text, nil when there is none.
func lastObject(text string) map[string]any {
	var last map[string]any
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		var v map[string]any
		if dec.Decode(&v) == nil {
			last, i = v, i+int(dec.InputOffset())-1
		}
	}
	return last
}

// activity is what e tells the screen the agent does: its thinking, and its tool calls, each by its id, with
// its result once known: running, passed or failed.
func (e event) activity(harness string) []map[string]any {
	return map[string]func(event) []map[string]any{"claude": event.blocks, "pi": event.pi, "codex": event.codex,
		"opencode": event.opencode}[harness](e)
}

func (e event) pi() []map[string]any {
	switch e.Type {
	case "message_end":
		return e.blocks()
	case "tool_execution_start":
		return []map[string]any{toolCall(e.ToolCallID, e.ToolName, detail(e.Args), "running")}
	case "tool_execution_end":
		var r struct{ IsError bool }
		_ = json.Unmarshal(e.Result, &r)
		return []map[string]any{toolCall(e.ToolCallID, e.ToolName, "", result(e.IsError || r.IsError))}
	}
	return nil
}

func (e event) codex() []map[string]any {
	switch {
	case e.Item.Type == "reasoning" && e.Type == "item.completed":
		return []map[string]any{{"thinking": e.Item.Text}}
	case e.Item.Type != "command_execution":
		return nil
	case e.Type == "item.started":
		return []map[string]any{toolCall(e.Item.ID, "shell", e.Item.Command, "running")}
	}
	failed := e.Item.Status == "failed" || e.Item.ExitCode != nil && *e.Item.ExitCode != 0
	return []map[string]any{toolCall(e.Item.ID, "shell", e.Item.Command, result(failed))}
}

func (e event) opencode() []map[string]any {
	switch e.Type {
	case "reasoning":
		return []map[string]any{{"thinking": e.Part.Text}}
	case "tool_use":
		r := map[string]string{"completed": "passed", "error": "failed"}[e.Part.State.Status]
		return []map[string]any{toolCall(e.Part.CallID, e.Part.Tool, detail(e.Part.State.Input), cmp.Or(r, "running"))}
	}
	return nil
}

// blocks is the activity of claude's and pi's message content.
func (e event) blocks() (evs []map[string]any) {
	for _, b := range e.Message.Content {
		switch b.Type {
		case "thinking":
			evs = append(evs, map[string]any{"thinking": b.Thinking})
		case "tool_use":
			evs = append(evs, toolCall(b.ID, b.Name, detail(b.Input), "running"))
		case "tool_result":
			evs = append(evs, toolCall(b.ToolUseID, "", "", result(b.IsError)))
		}
	}
	return evs
}

// toolCall is a tool call's live event; a later one with its id and empty fields leaves those as they were.
func toolCall(id, name, detail, result string) map[string]any {
	return map[string]any{"id": id, "tool": name, "detail": detail, "result": result}
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
