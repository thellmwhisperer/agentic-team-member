package run

import (
	"cmp"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// acpUpdate is an ACP session/update's update, and the tool call a permission request names.
type acpUpdate struct {
	SessionUpdate, MessageID, ToolCallID, Kind, Title, Status string
	Content                                                   json.RawMessage // a chunk's block, a tool call's list
	Locations                                                 []struct{ Path string }
	RawInput                                                  json.RawMessage
}

// acp is what one line of an ACP agent tells the screen: its thought and message chunks joined by message, one
// line each, and its tool calls by toolCallId, named by kind with their title. The final message is the message
// chunks after the last tool call.
func (s *stream) acp(line []byte) (evs []map[string]any) {
	var m struct {
		Method string
		Params struct{ Update acpUpdate }
	}
	if json.Unmarshal(line, &m) != nil || m.Method != "session/update" {
		return nil
	}
	u := m.Params.Update
	var text struct{ Text string }
	_ = json.Unmarshal(u.Content, &text)
	chunk := u.SessionUpdate == "agent_thought_chunk" || u.SessionUpdate == "agent_message_chunk"
	if of := u.SessionUpdate + " " + u.MessageID; !chunk || of != s.chunkOf {
		evs, s.chunkOf = s.chunkEnd(), of
	}
	s.reach = cmp.Or(s.reach, outside(u.calls(), s.root, s.clone))
	r := map[string]string{"completed": "passed", "failed": "failed"}[u.Status]
	switch u.SessionUpdate {
	case "agent_message_chunk":
		s.final += text.Text
		s.chunk += text.Text
	case "agent_thought_chunk":
		s.chunk += text.Text
	case "tool_call":
		s.final = ""
		evs = append(evs, toolCall(u.ToolCallID, u.Kind, u.Title, cmp.Or(r, "running")))
	case "tool_call_update":
		if r != "" {
			evs = append(evs, toolCall(u.ToolCallID, "", "", r))
		}
	}
	return evs
}

// chunkEnd is the chunks joined since the last, as one thought or message.
func (s *stream) chunkEnd() []map[string]any {
	text, of := s.chunk, s.chunkOf
	s.chunk, s.chunkOf = "", ""
	switch {
	case strings.HasPrefix(of, "agent_message_chunk"):
		return says(text)
	case strings.HasPrefix(of, "agent_thought_chunk") && strings.TrimSpace(text) != "":
		return []map[string]any{{"thinking": text}}
	}
	return nil
}

// calls is u's tool call as the reach check reads it: its rawInput, its locations and each of its commands.
func (u acpUpdate) calls() []use {
	if u.ToolCallID == "" {
		return nil
	}
	cs := []use{{u.Kind, strs(u.RawInput, writesFile(u.Kind)), ""}}
	for _, l := range u.Locations {
		cs = append(cs, use{u.Kind, l.Path, ""})
	}
	for _, c := range u.commands() {
		cs = append(cs, use{u.Kind, c, c})
	}
	return cs
}

// commands is u's rawInput command and each command in its content, which hermes shows as "$ <command>".
func (u acpUpdate) commands() (cs []string) {
	var in struct{ Command string }
	_ = json.Unmarshal(u.RawInput, &in)
	var content []struct{ Content struct{ Type, Text string } }
	_ = json.Unmarshal(u.Content, &content)
	for _, c := range content {
		if command, ok := strings.CutPrefix(c.Content.Text, "$ "); ok && c.Content.Type == "text" {
			cs = append(cs, command)
		}
	}
	if in.Command != "" {
		cs = append(cs, in.Command)
	}
	return cs
}

// answer is what ATM writes back to one line of an ACP agent, nil for nothing: the conversation's next call once
// the agent replies to one, the answer to a permission request, method not found to any other request. A
// failed call or the prompt's reply ends the conversation, stop its stopReason or the call and its error.
func (s *stream) answer(line []byte) []byte {
	var m struct {
		ID     json.RawMessage
		Method string
		Params json.RawMessage
		Result struct{ SessionID, StopReason string }
		Error  *struct{ Message string }
	}
	if json.Unmarshal(line, &m) != nil || m.ID == nil {
		return nil
	}
	switch id := string(m.ID); {
	case m.Method == "session/request_permission":
		return jsonLine(map[string]any{"id": m.ID, "result": s.permit(m.Params)})
	case m.Method != "":
		return jsonLine(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
	case m.Error != nil:
		s.stop = map[string]string{"1": "initialize", "2": "session/new", "3": "session/prompt"}[id] + ": " +
			m.Error.Message
	case id == "1":
		return jsonLine(map[string]any{"id": 2, "method": "session/new",
			"params": map[string]any{"cwd": s.clone, "mcpServers": []any{}}})
	case id == "2":
		return jsonLine(map[string]any{"id": 3, "method": "session/prompt", "params": map[string]any{
			"sessionId": m.Result.SessionID, "prompt": []any{map[string]any{"type": "text", "text": s.prompt}}}})
	case id == "3":
		s.stop = cmp.Or(m.Result.StopReason, "no stopReason")
	}
	return nil
}

// talk makes cmd's stdin ATM's ACP conversation when harness is an ACP agent, prompt its prompt, and opens it
// with initialize: protocol version 1, and neither fs nor terminal for the agent.
func (s *stream) talk(cmd *exec.Cmd, log io.Writer, harness, prompt string) (err error) {
	if _, acp := config.ACP[harness]; !acp {
		return nil
	}
	cmd.Stdin, s.prompt = nil, prompt
	if s.rpc, err = cmd.StdinPipe(); err == nil { // the pipe holds it until the agent starts
		s.say(cmd, log, jsonLine(map[string]any{"id": 1, "method": "initialize",
			"params": map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}}))
	}
	return err
}

// say writes msg, if any, to the ACP agent cmd runs, and to log; once the conversation ends, ATM stops the agent.
func (s *stream) say(cmd *exec.Cmd, log io.Writer, msg []byte) {
	if msg != nil {
		_, _ = s.rpc.Write(msg) // a dead agent's failure is its own
		_, _ = log.Write(msg)
	}
	if s.stop != "" {
		_ = s.rpc.Close()
		s.rpc = nil
		_ = cmd.Cancel()
	}
}

// acpEnded is how an ACP agent ended, from how its process did: at its prompt's reply, which ATM stopped it at,
// end_turn passes and any other stopReason or failed call fails; without one, even exit code 0 fails.
func (s *stream) acpEnded(exit, cause string, err error) (*stream, string, string, error) {
	switch {
	case s.stop != "":
		return s, s.stop, map[bool]string{false: s.stop}[s.stop == "end_turn"], nil
	case s.prompt != "" && cause == "" && exit != "":
		cause = exit + "; the agent never replied to its prompt"
	}
	return s, exit, cause, err
}

// withoutSkill is check of a report without the skill an ACP agent's carries.
func withoutSkill(check func(map[string]any) error) func(map[string]any) error {
	return func(r map[string]any) error {
		delete(r, "skill")
		return check(r)
	}
}

func jsonLine(m map[string]any) []byte {
	m["jsonrpc"] = "2.0"
	b, _ := json.Marshal(m)
	return append(b, '\n')
}

// permit answers a permission request (rule 7): its first allow_once option when the tool call is inside the
// clone, else its first reject_once, never an always one.
func (s *stream) permit(params json.RawMessage) map[string]any {
	var p struct {
		ToolCall acpUpdate
		Options  []struct{ OptionID, Kind string }
	}
	_ = json.Unmarshal(params, &p)
	kind := map[bool]string{true: "allow_once", false: "reject_once"}[p.ToolCall.inside(s.clone)]
	for _, o := range p.Options {
		if o.Kind == kind {
			return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}}
		}
	}
	return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
}

// inside says whether every path u names, each whole, a relative one the clone's, is inside clone, and none of
// its commands reaches outside it, as the check after each step reads them, with every absolute path its root.
func (u acpUpdate) inside(clone string) bool {
	for _, p := range u.paths() {
		if !filepath.IsAbs(p) {
			p = filepath.Join(clone, p)
		}
		rel, err := filepath.Rel(clone, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
	}
	var cs []use
	for _, c := range u.commands() {
		cs = append(cs, use{u.Kind, c, c})
	}
	return outside(cs, "/", clone) == ""
}

// paths is u's locations and every value of a rawInput key naming a path, as path, file_path or cwd.
func (u acpUpdate) paths() (ps []string) {
	for _, l := range u.Locations {
		ps = append(ps, l.Path)
	}
	var in map[string]any
	_ = json.Unmarshal(u.RawInput, &in)
	for k, v := range in {
		if !strings.Contains(strings.ToLower(k), "path") && k != "cwd" {
			continue
		}
		vs, _ := v.([]any)
		for _, v := range append(vs, v) {
			if p, ok := v.(string); ok {
				ps = append(ps, p)
			}
		}
	}
	return ps
}

// withSkill is brief with skill in it, for an ACP agent, which has no skill directory: its report proves it.
func withSkill(brief, skill string) string {
	b, _ := skills.ReadFile(skill + "/SKILL.md")
	return brief + "\n## SKILL `" + skill + "`\nYour report carries \"skill\": \"" + skill + "\". The skill:\n\n" +
		string(b)
}
