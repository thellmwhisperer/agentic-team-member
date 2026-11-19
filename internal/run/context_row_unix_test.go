//go:build unix

package run

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// A real run's Agent row shows, while the worker runs, the context its last model call used and its window: pi's
// from its models.json, or else the table pi lists for its model, ? for claude, whose stream names none; under a
// thousand tokens whole, a million in M.
func TestRealScreenAgentRowContext(t *testing.T) {
	builtATM(t)
	models, err := filepath.Abs(filepath.Join("testdata", "pi-agent"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, harness, stream, want string
		narrow                      bool // at 100 columns, the context does not fit beside the label
	}{
		{"pi", "pi", `{"type":"message_start","message":{"role":"assistant","content":[],"provider":"llama-cpp",` +
			`"model":"qwen3.6-27b","usage":{"input":21,"output":0,"cacheRead":26851,"cacheWrite":0,` +
			`"totalTokens":26872}}}`, "Agent  26.9K / 262K  ", true},
		{"pi million window", "pi", `{"type":"message_end","message":{"role":"assistant","content":[],` +
			`"provider":"llama-cpp","model":"qwen3.6-27b-1m","usage":{"input":600,"output":40,"cacheRead":0,"cacheWrite":0}}}`,
			"Agent  640 / 1.05M  ", false},
		{"pi built-in provider", "pi", `{"type":"message_start","message":{"role":"assistant","content":[],` +
			`"provider":"openai-codex","model":"gpt-6.1-sol","usage":{"input":21,"output":0,"cacheRead":5499,` +
			`"cacheWrite":0}}}`, "Agent  5.52K / 500K  ", false},
		{"pi built-in million window", "pi", `{"type":"message_end","message":{"role":"assistant","content":[],` +
			`"provider":"deepseek","model":"deepseek-flash","usage":{"input":600,"output":40,"cacheRead":0,"cacheWrite":0}}}`,
			"Agent  640 / 1M  ", false},
		{"claude", "claude", `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5-5","content":[],` +
			`"usage":{"input_tokens":3,"cache_creation_input_tokens":4120,"cache_read_input_tokens":18234,` +
			`"output_tokens":8}}}`, "Agent  22.4K / ?  ", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			partial := filepath.Join(t.TempDir(), "partial")
			writeFixture(t, partial, "stderr") // the worker keeps running 3 s after its stream
			t.Setenv("FAKE_AGENT_PARTIAL", partial)
			t.Setenv("FAKE_AGENT_STREAM", c.stream+"\n")
			t.Setenv("PI_CODING_AGENT_DIR", models)
			// pi's own table: a built-in provider's models are not in models.json
			t.Setenv("FAKE_PI_MODELS", "openai-codex  gpt-5.6-luna   272K  128K  yes  yes\n"+
				"openai-codex  gpt-6.1-sol    500K  128K  yes  yes\n"+"deepseek      deepseek-flash  1M    384K  yes  yes\n")
			t.Setenv("TERM", "xterm-256color")
			top := gitT(t, repo(t, "local", atmYAML), "rev-parse", "--show-toplevel")
			f := startFollow(t, versionServer(t, top, "follow-test"), top, true,
				"run", "--harness", c.harness, issueFile(t, issue))
			// At 100 columns the ATM box may leave the context, there a second after the stream, no room beside
			// the label, which stays whole; wide enough, the context shows.
			var shown strings.Builder
			if c.narrow {
				untilShown(t, f, &shown, "Agent  1.")
				if s := ansi.Strip(shown.String()); strings.Contains(s, "Ag…") || strings.Contains(s, c.want) {
					t.Fatalf("want the whole label and no context at 100 columns:\n%s", s)
				}
			}
			if err := pty.Setsize(f.term, &pty.Winsize{Rows: 24, Cols: 160}); err != nil {
				t.Fatal(err)
			}
			if err := f.cmd.Process.Signal(syscall.SIGWINCH); err != nil {
				t.Fatal(err)
			}
			untilShown(t, f, &shown, c.want)
			untilShown(t, f, &shown, "q close")
			f.exit(0)
		})
	}
}
