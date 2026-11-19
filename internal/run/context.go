package run

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// usageLine is the fields of a pi or claude line that say how much context a model call used.
type usageLine struct {
	Type    string
	Message struct {
		Role, Model, Provider string
		Usage                 struct {
			Input, Output, CacheRead, CacheWrite int // pi
			InputTokens                          int `json:"input_tokens"` // claude
			OutputTokens                         int `json:"output_tokens"`
			CacheCreation                        int `json:"cache_creation_input_tokens"`
			CacheReadTokens                      int `json:"cache_read_input_tokens"`
		}
	}
}

// contextUsed is the screen's event for line of harness's stream when it says how much context a model call used:
// the tokens used and the model's window, 0 when the line and the harness's own configuration do not say it (claude's
// stream names none: see claudeWindow). None when the line says no usage. dir and model are the worker's directory
// and the model it was given, "" for its default.
func contextUsed(harness string, line []byte, env []string, dir, model string) []map[string]any {
	if harness == "opencode" {
		return openCodeContext(line, env, dir, model)
	}
	var e usageLine
	if json.Unmarshal(line, &e) != nil || e.Message.Role != "assistant" {
		return nil
	}
	u, used, window := e.Message.Usage, 0, 0
	switch {
	case harness == "pi" && (e.Type == "message_start" || e.Type == "message_end"):
		used = u.Input + u.Output + u.CacheRead + u.CacheWrite
		if used > 0 && e.Message.Provider != "" && e.Message.Model != "" {
			window = piWindow(e.Message.Provider, e.Message.Model, env)
		}
	case harness == "claude" && e.Type == "assistant":
		used = u.InputTokens + u.OutputTokens + u.CacheCreation + u.CacheReadTokens
	}
	if used == 0 {
		return nil
	}
	return []map[string]any{{"context": map[string]any{"used": used, "window": window}}}
}

// openCodeStep is the fields of an OpenCode 2 step_finish line that say how much context its step used: its cache
// reads are not in its input.
type openCodeStep struct {
	Type string
	Part struct {
		Tokens struct {
			Input, Output, Reasoning int
			Cache                    struct{ Read, Write int }
		}
	}
}

// openCodeContext is contextUsed for an OpenCode 2 line: its step's tokens, and the window OpenCode 2 gives the
// worker's model.
func openCodeContext(line []byte, env []string, dir, model string) []map[string]any {
	var e openCodeStep
	_ = json.Unmarshal(line, &e) // not JSON: no step
	t := e.Part.Tokens
	if used := t.Input + t.Output + t.Reasoning + t.Cache.Read + t.Cache.Write; e.Type == "step_finish" && used > 0 {
		return []map[string]any{{"context": map[string]any{"used": used, "window": openCodeWindow(dir, model, env)}}}
	}
	return nil
}

// openCodeModel is the fields of a model in OpenCode 2's API that name it and give its window.
type openCodeModel struct {
	ID, ProviderID string
	Limit          struct{ Context int }
}

// openCodeWindows is each window OpenCode 2's API gave, by the worker's directory and model.
var openCodeWindows sync.Map

// openCodeWindow is the limit.context OpenCode 2's API gives, in dir with env, for model, provider/model with any
// #variant, else for its default model; 0 when it gives none or cannot run. It is kept for the process, so each
// step does not start opencode.
func openCodeWindow(dir, model string, env []string) int {
	model, _, _ = strings.Cut(model, "#") // a variant's window is its model's
	key := dir + "\x00" + model
	if v, ok := openCodeWindows.Load(key); ok {
		return v.(int)
	}
	method := "model.list"
	if model == "" {
		method = "model.default"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "opencode", "api", "--standalone", method)
	cmd.Dir, cmd.Env = dir, env
	out, _ := cmd.Output()
	var listed struct{ Data []openCodeModel }
	var given struct{ Data openCodeModel }
	window := 0
	if model == "" {
		_ = json.Unmarshal(out, &given) // none, or not JSON: no window
		window = given.Data.Limit.Context
	} else {
		_ = json.Unmarshal(out, &listed)
		provider, id, _ := strings.Cut(model, "/")
		for _, m := range listed.Data {
			if m.ProviderID == provider && m.ID == id {
				window = m.Limit.Context
				break
			}
		}
	}
	openCodeWindows.Store(key, window)
	return window
}

// piWindow is the context window pi gives model of provider, 0 when it gives none: the models.json entry of a
// provider added by hand, else the figure pi lists for the model, as built-in providers have none in models.json.
func piWindow(provider, model string, env []string) int {
	home := environmentValue(env, "HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	piDir := cmp.Or(environmentValue(env, "PI_CODING_AGENT_DIR"), filepath.Join(home, ".pi", "agent"))
	b, _ := os.ReadFile(filepath.Join(piDir,
		"models.json"))
	var models struct {
		Providers map[string]struct {
			Models []struct {
				ID            string
				ContextWindow int `json:"contextWindow"`
			}
		}
	}
	_ = json.Unmarshal(b, &models) // none, or not JSON: no window
	for _, m := range models.Providers[provider].Models {
		if m.ID == model && m.ContextWindow > 0 {
			return m.ContextWindow
		}
	}
	return piListedWindow(piDir, provider, model, env)
}

// piListings is each model's window `pi --list-models` showed, by the pi directory, provider and model.
var piListings sync.Map

// piListedWindow is the context window `pi --list-models` shows for model of provider, 0 when pi lists none or
// cannot run. It is kept for the process, so each model call does not start pi.
func piListedWindow(piDir, provider, model string, env []string) int {
	key := piDir + "\x00" + provider + "\x00" + model
	if v, ok := piListings.Load(key); ok {
		return v.(int)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pi", "--offline", "--no-extensions", "--list-models", model)
	cmd.Env = env
	out, _ := cmd.Output()
	window := 0
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 2 && f[0] == provider && f[1] == model {
			window = listedTokens(f[2])
			break
		}
	}
	piListings.Store(key, window)
	return window
}

func environmentValue(env []string, name string) string {
	if env == nil {
		return os.Getenv(name)
	}
	for i := len(env) - 1; i >= 0; i-- {
		key, value, ok := strings.Cut(env[i], "=")
		if ok && key == name {
			return value
		}
	}
	return ""
}

// listedTokens is the token count of a `pi --list-models` figure such as 272K or 1M, 0 when it is none.
func listedTokens(s string) int {
	unit := 1.0
	switch {
	case strings.HasSuffix(s, "K"):
		unit, s = 1e3, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		unit, s = 1e6, strings.TrimSuffix(s, "M")
	}
	n, _ := strconv.ParseFloat(s, 64) // not a figure: 0
	return int(n*unit + 0.5)
}

// claudeProbes is, by model, the one question ATM asks claude about its window in this process.
var claudeProbes sync.Map // model -> *claudeProbe

// claudeProbe is claude's answer about one model's window, once done is closed; 0 when it gave none.
type claudeProbe struct {
	done   chan struct{}
	window int
}

// askClaudeWindow starts asking claude, once per model and process, for the window of model, and returns the
// question. A claude stream says the window only in its final result, after the agent has ended, so ATM asks claude
// itself, which reads it from the model: the figure is claude's, never ATM's.
func askClaudeWindow(model string) *claudeProbe {
	p := &claudeProbe{done: make(chan struct{})}
	if got, loaded := claudeProbes.LoadOrStore(model, p); loaded {
		return got.(*claudeProbe)
	}
	go func() {
		defer close(p.done)
		if p.window = probeClaudeWindow(model); p.window == 0 {
			claudeProbes.Delete(model) // no answer is not remembered: the next agent asks again
		}
	}()
	return p
}

// claudeWindow is the window claude says model has, waiting for its answer; 0 when it gave none.
func claudeWindow(model string) int {
	if model == "" {
		return 0
	}
	p := askClaudeWindow(model)
	<-p.done
	return p.window
}

// probeClaudeWindow runs claude on model in a temporary directory and reads the contextWindow of model in its
// result's modelUsage; 0 when claude fails or does not say it.
func probeClaudeWindow(model string) int {
	dir, _ := os.MkdirTemp("", "atm-window-") // none: claude runs where ATM does, with no tools
	defer func() { _ = os.RemoveAll(dir) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // all claude has to answer
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "json", "--setting-sources", "project",
		"--strict-mcp-config", "--tools", "", "--no-session-persistence", "--model", model)
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.WaitDelay = dir, agentEnv(), strings.NewReader(helloBrief), time.Second
	out, _ := cmd.Output() // failed: no JSON, no window
	var r struct {
		ModelUsage map[string]struct {
			ContextWindow int `json:"contextWindow"`
		}
	}
	_ = json.Unmarshal(out, &r)
	return r.ModelUsage[model].ContextWindow
}
