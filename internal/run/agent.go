package run

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
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

// ponytailSkill is the ponytail skill (github.com/DietrichGebert/ponytail, MIT): the implementing agent
// loads it, always.
//
//go:embed ponytail/SKILL.md
var ponytailSkill []byte

// skillDirs is where each harness finds a project skill; pi finds it where --skill points, under .atm/.
var skillDirs = map[string]string{"claude": ".claude/skills", "codex": ".agents/skills",
	"opencode": ".opencode/skills", "pi": ".atm/skills"}

// tools is all claude needs for a unit.
const tools = "Read,Edit,Write,Bash,Glob,Grep,Skill"

// agent is the coding agent of the run: its harness, model and effort, and the --harness-arg and --env of
// the run.
type agent struct {
	config.Agent
	args, env []string
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

// run runs the agent on brief in clone, in its own process group, and returns its report. Every line it
// writes goes to .atm/worker-<timestamp>.jsonl, named after the clone, so it is as unique, and outlives it.
// The timeout, SIGINT and SIGTERM kill the group.
func (a agent) run(clone, brief string) (map[string]any, error) {
	path := filepath.Join(filepath.Dir(filepath.Dir(clone)),
		"worker-"+strings.TrimPrefix(filepath.Base(clone), "atm-run-")+".jsonl")
	ev := map[string]any{"log": path}
	skill, err := placeSkill(clone, a.Harness)
	if err != nil {
		return ev, err
	}
	f, err := os.Create(path)
	if err != nil {
		return ev, err
	}
	defer func() { _ = f.Close() }()
	final := strings.TrimSuffix(path, ".jsonl") + ".final.md"
	argv, stdin := a.argv(clone, skill, final, brief)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Stdin, cmd.WaitDelay = clone, strings.NewReader(stdin), time.Second
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CLAUDE_CODE_CHILD_SESSION=") // set, the agent's transcripts are off
	}), a.env...)
	ownGroup(cmd)
	var s stream
	err = s.read(cmd, f, a.Harness)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ev, fmt.Errorf("agent timed out after %s; log: %s", human(agentTimeout), path)
	case ctx.Err() != nil:
		return ev, fmt.Errorf("agent interrupted; log: %s", path)
	case err != nil:
		return ev, fmt.Errorf("agent: %w; log: %s", err, path)
	case !s.skill:
		return ev, fmt.Errorf("agent left no proof it used the ponytail skill; log: %s", path)
	}
	if a.Harness == "codex" {
		b, _ := os.ReadFile(final) // none is no report
		s.final = string(b)
	}
	ev["report"], err = report(s.final)
	if err != nil {
		return ev, fmt.Errorf("%w; log: %s", err, path)
	}
	return ev, nil
}

// report is the agent's report, the last JSON object of its final message, with the test file it names: ATM
// never guesses it.
func report(final string) (map[string]any, error) {
	r := lastObject(final)
	if r == nil {
		return nil, errors.New("agent left no report: no JSON object in its final message")
	}
	if _, ok := r["test_file"].(string); !ok {
		return nil, errors.New("agent report has no test_file")
	}
	return r, nil
}

// placeSkill puts the ponytail skill where harness finds it in clone, outside git, and returns its directory.
func placeSkill(clone, harness string) (string, error) {
	rel := skillDirs[harness] + "/ponytail"
	dir := filepath.Join(clone, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return dir, err
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), ponytailSkill, 0o644); err != nil {
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

// stream is what the agent's lines said: its final message and whether it used the ponytail skill.
type stream struct {
	final string
	skill bool
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
	err := cmd.Run()
	_ = pw.Close()
	return errors.Join(err, <-done)
}

// event is the fields of every harness's events that say what its final message is and that it used the
// ponytail skill.
type event struct {
	Type    string
	Result  string // claude
	Message struct {
		Role    string
		Content []block
	}
	ToolName string `json:"toolName"` // pi
	Args     json.RawMessage
	Item     struct{ Type, Command string } // codex
	Part     struct {                       // opencode
		Tool, Text string
		State      struct{ Input json.RawMessage }
	}
}

type block struct {
	Type, Name, Text string
	Input            json.RawMessage
}

// see reads one JSON line of harness's stream.
func (s *stream) see(harness string, line []byte) {
	var e event
	_ = json.Unmarshal(line, &e) // a field of another type is left empty, the rest is read
	s.skill = s.skill || e.usedSkill(harness)
	if text, ok := e.final(harness); ok {
		s.final = text
	}
}

// usedSkill says whether e proves the agent used the ponytail skill: a Skill tool call for claude, a read of
// its SKILL.md for pi and codex, a skill event for opencode.
func (e event) usedSkill(harness string) bool {
	ponytail := func(b []byte) bool { return bytes.Contains(b, []byte("ponytail")) }
	read := func(b []byte) bool { return ponytail(b) && bytes.Contains(b, []byte("SKILL.md")) }
	switch harness {
	case "claude":
		return slices.ContainsFunc(e.Message.Content, func(b block) bool {
			return b.Type == "tool_use" && b.Name == "Skill" && ponytail(b.Input)
		})
	case "codex":
		return e.Item.Type == "command_execution" && read([]byte(e.Item.Command))
	case "opencode":
		return e.Type == "tool_use" && e.Part.Tool == "skill" && ponytail(e.Part.State.Input)
	case "pi":
		return e.Type == "tool_execution_start" && e.ToolName == "read" && read(e.Args)
	}
	return false
}

// final is the final message e ends, if it ends one; codex's is in its -o file instead.
func (e event) final(harness string) (string, bool) {
	switch {
	case harness == "claude" && e.Type == "result":
		return e.Result, true
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
