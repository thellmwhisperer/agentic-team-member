package run

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
)

// pi runs in RPC mode: its stdin takes the brief as a prompt command, then any steer the operator sends with atm
// steer. When its agent ends, ATM asks for its state; once it is neither streaming, compacting nor retrying, ATM
// closes its stdin and pi exits.

// piLine is one command for pi's stdin.
func piLine(m map[string]any) []byte {
	b, _ := json.Marshal(m)
	return append(b, '\n')
}

// piSettled is the id of the get_state ATM sends at each agent_end, retry's end and compaction's end.
const piSettled = "atm-settled"

// piReply reads one line of pi's stdout and is what ATM writes back, nil for nothing. A failed prompt ends the
// conversation; agent_end, and the end of a retry or of a compaction pi does not retry after, asks pi's state,
// whose reply ends it unless pi goes on. A steer's response, or the user message that delivers it, answers the atm
// steer that sent it.
func (s *stream) piReply(line []byte) []byte {
	var m struct {
		ID, Type, Command, Error string
		Success, WillRetry       bool
		Data                     struct {
			SessionID                 string `json:"sessionId"`
			IsStreaming, IsCompacting bool
		}
		Message struct {
			Role    string
			Content json.RawMessage
		}
	}
	_ = json.Unmarshal(line, &m) // a line that is not an object says nothing to answer
	switch {
	case m.Type == "auto_retry_start":
		s.retrying = true
	case m.Type == "auto_compaction_end" && m.WillRetry:
	case m.Type == "auto_retry_end":
		s.retrying = false
		fallthrough
	case m.Type == "agent_end", m.Type == "auto_compaction_end":
		return piLine(map[string]any{"id": piSettled, "type": "get_state"})
	case m.Type == "message_end" && m.Message.Role == "user":
		text := contentText(m.Message.Content)
		s.steered(func(w steerWait) bool { return w.message == text }, nil)
	case m.Type != "response":
	case m.Command == "prompt" && !m.Success:
		s.failure, s.settled = "prompt: "+cmp.Or(m.Error, "failed"), true
	case m.Command == "steer" && !m.Success:
		s.steered(func(w steerWait) bool { return w.id == m.ID },
			errors.New("pi refused the message: "+cmp.Or(m.Error, "failed")))
	case m.ID == piSettled:
		s.session = cmp.Or(s.session, m.Data.SessionID)
		s.settled = !m.Data.IsStreaming && !m.Data.IsCompacting && !s.retrying
	}
	return nil
}

// steerWait is an atm steer waiting until pi delivers its message.
type steerWait struct {
	id, message string
	done        chan error
}

// steer sends message to pi as a steer and waits until pi delivers it as a user message, after its current
// tool calls and before its next model call. It fails when pi refuses it or ends before delivering it.
func (s *stream) steer(message string) error {
	s.mu.Lock()
	if s.rpc == nil {
		s.mu.Unlock()
		return errNoAgent
	}
	s.steerN++
	w := steerWait{id: "atm-steer-" + strconv.Itoa(s.steerN), message: message, done: make(chan error, 1)}
	line := piLine(map[string]any{"id": w.id, "type": "steer", "message": message})
	_, _ = s.rpc.Write(line) // a dead pi's steer fails once its process ends
	_, _ = s.log.Write(line)
	s.steers = append(s.steers, w)
	s.mu.Unlock()
	return <-w.done
}

// steered answers with err the first steer waiting that is picks. Under s.mu.
func (s *stream) steered(is func(w steerWait) bool, err error) {
	if i := slices.IndexFunc(s.steers, is); i >= 0 {
		s.steers[i].done <- err
		s.steers = slices.Delete(s.steers, i, i+1)
	}
}

// unsteered fails every steer pi never delivered, once it ended, and any later one.
func (s *stream) unsteered() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rpc = nil // its process closed it
	for _, w := range s.steers {
		w.done <- errors.New("pi ended before the message reached it")
	}
	s.steers = nil
}

// errNoAgent is a steer to a run whose agent is not working.
var errNoAgent = errors.New("no agent working now; the message was not sent")

// steerable is the agent a run has working now: its harness and its stream.
type steerable struct {
	harness string
	s       *stream
}

// steering is each run's working agent, by the run's directory.
var steering sync.Map

// working runs read, the agent's process, with s where atm steer finds it by the run's directory, and fails
// every steer it left undelivered once it ends.
func (a agent) working(s *stream, read func() error) error {
	w := &steerable{harness: a.Harness, s: s}
	steering.Store(a.dir, w)
	defer s.unsteered()
	defer steering.CompareAndDelete(a.dir, w)
	return read()
}

// steerRun sends message to the agent run label has working now: pi takes it mid-run, no other harness can.
func (s *server) steerRun(label, message string) error {
	r := s.find(label)
	if r == nil {
		return errors.New("no run " + label)
	}
	v, ok := steering.Load(runDir(s.root, r.Run))
	if !ok {
		return fmt.Errorf("run %s has %w", r.Run, errNoAgent)
	}
	a := v.(*steerable)
	if a.harness != "pi" {
		return fmt.Errorf("%s cannot take a message mid-run", a.harness)
	}
	if err := a.s.steer(message); err != nil {
		return fmt.Errorf("run %s: %w", r.Run, err)
	}
	return nil
}

// Steer sends message to the agent working in run label and waits until it takes it.
func Steer(root, label, message string) error {
	return call(root, request{Cmd: "steer", Run: label, Message: message}, nil, func(reply) {})
}
