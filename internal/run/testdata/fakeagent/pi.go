package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

// piRPC plays pi in RPC mode, as pi 1.5.2 does: its stdin's first line is the prompt command; each steer is
// answered as queued and kept until the worker's work ends, but one whose message is $FAKE_PI_REFUSE, which it
// refuses; each get_state is answered as idle; it exits once its stdin closes.
type piRPC struct {
	steers chan string
	closed chan struct{}
}

var pi *piRPC

// piPrompt reads pi's prompt command and starts serving the rest of its stdin. With $FAKE_AGENT pi-prompt-fail,
// the prompt is refused, as pi refuses one it cannot send.
func piPrompt() (prompt string, line []byte) {
	in := bufio.NewReader(os.Stdin)
	line, _ = in.ReadBytes('\n')
	var c struct{ Type, Message string }
	_ = json.Unmarshal(line, &c)
	pi = &piRPC{steers: make(chan string, 16), closed: make(chan struct{})}
	if os.Getenv("FAKE_AGENT") == "pi-prompt-fail" {
		emit(obj{"type": "response", "command": "prompt", "success": false, "error": "No API key for fake"})
	} else {
		emit(obj{"type": "response", "command": "prompt", "success": true})
	}
	go pi.serve(in)
	return c.Message, line
}

func (p *piRPC) serve(in *bufio.Reader) {
	defer close(p.closed)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var c struct{ ID, Type, Message string }
		_ = json.Unmarshal(line, &c)
		switch c.Type {
		case "steer":
			if c.Message == os.Getenv("FAKE_PI_REFUSE") {
				emit(obj{"id": c.ID, "type": "response", "command": "steer", "success": false,
					"error": "steering is off"})
				continue
			}
			emit(obj{"type": "queue_update", "steering": []string{c.Message}, "followUp": []string{}})
			emit(obj{"id": c.ID, "type": "response", "command": "steer", "success": true,
				"data": obj{"disposition": "queued"}})
			p.steers <- c.Message
		case "get_state":
			emit(obj{"id": c.ID, "type": "response", "command": "get_state", "success": true,
				"data": obj{"sessionId": "pi-rpc-session", "isStreaming": false, "isCompacting": false}})
		}
	}
}

// deliver waits for $FAKE_PI_STEERS steers and delivers each as a user message, after the worker's work and
// before its final message.
func (p *piRPC) deliver() {
	n, _ := strconv.Atoi(os.Getenv("FAKE_PI_STEERS"))
	for range n {
		m := <-p.steers
		emit(obj{"type": "queue_update", "steering": []string{}, "followUp": []string{}})
		user := obj{"role": "user", "content": []obj{{"type": "text", "text": m}}}
		emit(obj{"type": "message_start", "message": user})
		emit(obj{"type": "message_end", "message": user})
	}
}

// retry plays, with $FAKE_AGENT pi-retry, a model call that fails and that pi retries after its agent_end.
func (p *piRPC) retry() {
	if os.Getenv("FAKE_AGENT") != "pi-retry" {
		return
	}
	emit(obj{"type": "message_end", "message": obj{"role": "assistant", "stopReason": "error",
		"errorMessage": "The model is currently at capacity"}})
	_, _ = os.Stdout.WriteString(`{"type":"agent_end","messages":[]}` + "\n" +
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":0}` + "\n")
	emit(obj{"type": "agent_start"})
}

// end ends the agent's run and waits until ATM closes pi's stdin. A worker with $FAKE_PI_LINGER, a directory,
// then stays alive, as pi may for a moment: it says so in its file ready and ends once a line comes on its FIFO go.
func (p *piRPC) end(worker bool) {
	end := `{"type":"agent_end","messages":[]}` + "\n"
	if os.Getenv("FAKE_AGENT") == "pi-retry" {
		end += `{"type":"auto_retry_end","success":true,"attempt":1}` + "\n"
	}
	if os.Getenv("FAKE_AGENT") != "pi-prompt-fail" {
		_, _ = os.Stdout.WriteString(end)
	}
	<-p.closed
	if dir := os.Getenv("FAKE_PI_LINGER"); worker && dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600)
		_, _ = os.ReadFile(filepath.Join(dir, "go"))
	}
}
