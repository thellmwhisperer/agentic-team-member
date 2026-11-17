package run

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
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
func contextUsed(harness string, line []byte) []map[string]any {
	var e usageLine
	if json.Unmarshal(line, &e) != nil || e.Message.Role != "assistant" {
		return nil
	}
	u, used, window := e.Message.Usage, 0, 0
	switch {
	case harness == "pi" && (e.Type == "message_start" || e.Type == "message_end"):
		used = u.Input + u.Output + u.CacheRead + u.CacheWrite
		window = piWindow(e.Message.Provider, e.Message.Model)
	case harness == "claude" && e.Type == "assistant":
		used = u.InputTokens + u.OutputTokens + u.CacheCreation + u.CacheReadTokens
	}
	if used == 0 {
		return nil
	}
	return []map[string]any{{"context": map[string]any{"used": used, "window": window}}}
}

// piWindow is the context window pi's models.json gives model of provider, 0 when it gives none.
func piWindow(provider, model string) int {
	home, _ := os.UserHomeDir()
	b, _ := os.ReadFile(filepath.Join(cmp.Or(os.Getenv("PI_CODING_AGENT_DIR"), filepath.Join(home, ".pi", "agent")),
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
		if m.ID == model {
			return m.ContextWindow
		}
	}
	return 0
}
