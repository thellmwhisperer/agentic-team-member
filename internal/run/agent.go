package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// reportSchema is the agent's report, for codex --output-schema.
const reportSchema = `{"type": "object", "properties": {"test_file": {"type": "string"},
 "changed_files": {"type": "array", "items": {"type": "string"}}, "summary": {"type": "string"},
 "commands_run": {"type": "array", "items": {"type": "string"}},
 "follow_ups": {"type": "array", "items": {"type": "object", "properties": {"title": {"type": "string"},
  "paths": {"type": "array", "items": {"type": "string"}}, "red_test": {"type": "string"},
  "criterion": {"type": "string"}}, "required": ["title", "paths", "red_test", "criterion"],
  "additionalProperties": false}}},
 "required": ["test_file", "changed_files", "summary", "commands_run", "follow_ups"], "additionalProperties": false}
`

// agentCall is one run of the agent CLI on a brief.
type agentCall struct {
	harness, model, effort string
	clone, schema, lastMsg string // codex writes its last message to lastMsg
	timeout                time.Duration
}

// argv is the command line of the harness and what goes on its stdin: claude reads the brief there, the
// others take it as the last argument.
func (a agentCall) argv(brief string) (argv []string, stdin string) {
	switch a.harness {
	case "claude":
		// In -p mode thinking comes back "omitted" unless told otherwise; "summarized" is what the TUI shows.
		argv = []string{"claude", "-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
			"--thinking-display", "summarized", "--permission-mode", "acceptEdits",
			"--allowedTools", "Read", "Edit", "Write", "Bash", "Glob", "Grep"}
		argv = appendIf(argv, a.model, "--model", a.model)
		return appendIf(argv, a.effort, "--effort", a.effort), brief
	case "codex":
		argv = []string{"codex", "exec", "--json", "-C", a.clone, "--sandbox", "workspace-write",
			"--output-schema", a.schema, "-o", a.lastMsg}
		argv = appendIf(argv, a.model, "-c", "model="+quote(a.model))
		// Without it codex inherits ~/.codex/config.toml's effort.
		argv = appendIf(argv, a.effort, "-c", "model_reasoning_effort="+quote(a.effort))
	case "opencode":
		// --pure: no external plugins; the brief is the whole context the unit needs.
		argv = []string{"opencode", "run", "--pure", "--format", "json", "--dir", a.clone}
		argv = appendIf(argv, a.model, "-m", a.model)
	default: // pi, bare on purpose: extensions, skills and context files multiplied the prompt by 27.
		argv = []string{"pi", "-p", "--mode", "json", "--no-extensions", "--no-skills", "--no-prompt-templates",
			"--no-context-files", "--no-session"}
		argv = appendIf(argv, a.model, "--model", a.model)
		argv = appendIf(argv, a.effort, "--thinking", a.effort)
	}
	return append(argv, brief), ""
}

func appendIf(argv []string, value string, args ...string) []string {
	if value == "" {
		return argv
	}
	return append(argv, args...)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// agentRun is how the agent ended.
type agentRun struct {
	ExitCode int    `json:"harness_exit_code"` // -1 when it never started or was killed
	TimedOut bool   `json:"timed_out"`
	Error    string `json:"harness_error,omitempty"`
	final    string // the last message
}

// run starts the agent in the clone in its own process group, logs every line it prints, and kills the
// whole group at the timeout: a grandchild holding stdout would otherwise keep the stream open.
func (a agentCall) run(brief string, log *runLog) agentRun {
	ctx, cancel := context.WithTimeout(context.Background(), a.timeout)
	defer cancel()
	argv, stdin := a.argv(brief)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = a.clone, withoutChildSession(os.Environ())
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	r := agentRun{ExitCode: -1}
	lines := &lineWriter{line: func(line []byte) {
		var event json.RawMessage = line
		if !json.Valid(line) {
			event, _ = json.Marshal(string(line))
		}
		log.write(event)
		if text := finalText(a.harness, line); text != "" {
			r.final = text
		}
	}}
	cmd.Stdout, cmd.Stderr = lines, lines
	inOwnGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	lines.flush()
	r.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	if cmd.ProcessState != nil && !r.TimedOut {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		r.Error = err.Error()
	}
	if b, err := os.ReadFile(a.lastMsg); a.harness == "codex" && err == nil && len(b) > 0 {
		r.final = string(b)
	}
	return r
}

// withoutChildSession drops CLAUDE_CODE_CHILD_SESSION, which an agent launched from Claude Code inherits
// and which turns its transcripts off.
func withoutChildSession(env []string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "CLAUDE_CODE_CHILD_SESSION=") {
			out = append(out, kv)
		}
	}
	return out
}

// lineWriter calls line for every complete non-empty line written to it, however long.
type lineWriter struct {
	buf  []byte
	line func([]byte)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
}

func (w *lineWriter) flush() {
	w.emit(w.buf)
	w.buf = nil
}

func (w *lineWriter) emit(line []byte) {
	if line = bytes.TrimSpace(line); len(line) > 0 {
		w.line(bytes.Clone(line))
	}
}

// finalText is the agent's last message if line is the event its harness ends a message with.
func finalText(harness string, line []byte) string {
	var e struct {
		Type   string `json:"type"`
		Result string `json:"result"`
		Item   struct {
			Type, Text string
		} `json:"item"`
		Part struct {
			Text string
		} `json:"part"`
		Message struct {
			Role    string
			Content []struct{ Type, Text string }
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil {
		return ""
	}
	switch {
	case harness == "claude" && e.Type == "result":
		return e.Result
	case harness == "codex" && e.Item.Type == "agent_message":
		return e.Item.Text
	case harness == "opencode" && e.Type == "text":
		return e.Part.Text
	case harness == "pi" && e.Type == "message_end" && e.Message.Role == "assistant":
		var texts []string
		for _, block := range e.Message.Content {
			if block.Type == "text" && block.Text != "" {
				texts = append(texts, block.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

// extractReport is the last top-level JSON object in the agent's last message.
func extractReport(text string) (map[string]any, string) {
	if text == "" {
		return nil, "no final message"
	}
	var found map[string]any
	for i := strings.IndexByte(text, '{'); i >= 0 && i < len(text); {
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		var v map[string]any
		if dec.Decode(&v) != nil {
			i++
		} else {
			found, i = v, i+int(dec.InputOffset())
		}
		next := strings.IndexByte(text[i:], '{')
		if next < 0 {
			break
		}
		i += next
	}
	if found == nil {
		return nil, "no JSON object in final message"
	}
	return found, ""
}
