package run

import (
	"bufio"
	"cmp"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// wait tails a codex worker's session while it runs: exec --json only reports usage for the whole turn.
// Its usage records go only to the screen: neither the run's log nor its tail is what the agent wrote.
func (s *stream) wait(cmd *exec.Cmd, harness string) error {
	if harness != "codex" {
		return cmd.Wait()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var file *os.File
	var reader *bufio.Reader
	var pending []byte
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()
	read := func() {
		if reader == nil {
			s.mu.Lock()
			session := s.session
			s.mu.Unlock()
			file = codexSession(cmd.Environ(), session)
			if file == nil {
				return
			}
			reader = bufio.NewReader(file)
		}
		for {
			part, err := reader.ReadBytes('\n')
			pending = append(pending, part...)
			if err != nil {
				return // keep a partial JSON line until codex completes it
			}
			s.mu.Lock()
			for _, ev := range codexContextUsed(pending) {
				s.live(ev)
			}
			s.mu.Unlock()
			pending = nil
		}
	}
	for {
		select {
		case err := <-done:
			read() // the final call may land between the last tick and process exit
			return err
		case <-tick.C:
			read()
		}
	}
}

// codexSession finds only the session named by this invocation's thread_id, under its effective CODEX_HOME.
func codexSession(env []string, session string) *os.File {
	if session == "" {
		return nil
	}
	var home, codexHome string
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		switch key {
		case "HOME":
			home = value
		case "CODEX_HOME":
			codexHome = value
		}
	}
	paths, _ := filepath.Glob(filepath.Join(cmp.Or(codexHome, filepath.Join(home, ".codex")),
		"sessions", "*", "*", "*", "rollout-*.jsonl"))
	for _, path := range paths {
		if strings.HasSuffix(path, "-"+session+".jsonl") {
			file, _ := os.Open(path)
			return file
		}
	}
	return nil
}

func codexContextUsed(line []byte) []map[string]any {
	var e struct {
		Type    string
		Payload struct {
			Type string
			Info struct {
				LastTokenUsage struct {
					TotalTokens int `json:"total_tokens"`
				} `json:"last_token_usage"`
				ModelContextWindow int `json:"model_context_window"`
			}
		}
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "event_msg" || e.Payload.Type != "token_count" ||
		e.Payload.Info.LastTokenUsage.TotalTokens == 0 {
		return nil
	}
	return []map[string]any{{"context": map[string]any{
		"used": e.Payload.Info.LastTokenUsage.TotalTokens, "window": e.Payload.Info.ModelContextWindow}}}
}
