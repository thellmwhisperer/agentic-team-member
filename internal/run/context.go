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
// the tokens used and the model's window, 0 when the harness's own configuration does not say it. None when the
// line says no usage.
func contextUsed(harness string, line []byte, env []string) []map[string]any {
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
