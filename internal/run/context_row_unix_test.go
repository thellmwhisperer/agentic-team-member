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
// thousand tokens whole, a million in M. An OpenCode 2 worker's is its last step's, of the limit.context OpenCode
// 2's API gives, in the worker's clone and with its environment, for the model it was given, else its default.
// testdata/opencode2-context.jsonl is a real OpenCode 2.3.2 run's log, `opencode --standalone run --auto --format
// json`, and testdata/opencode2-api the answers of its `opencode api --standalone model.default` and model.list,
// the directory, API keys and endpoints taken out.
func TestRealScreenAgentRowContext(t *testing.T) {
	builtATM(t)
	models, err := filepath.Abs(filepath.Join("testdata", "pi-agent"))
	if err != nil {
		t.Fatal(err)
	}
	openCodeAPI, err := filepath.Abs(filepath.Join("testdata", "opencode2-api"))
	if err != nil {
		t.Fatal(err)
	}
	openCode := strings.TrimSpace(fixture(t, "opencode2-context.jsonl"))
	for _, c := range []agentRowContext{
		{"pi", "pi", `{"type":"message_start","message":{"role":"assistant","content":[],"provider":"llama-cpp",` +
			`"model":"qwen3.6-27b","usage":{"input":21,"output":0,"cacheRead":26851,"cacheWrite":0,` +
			`"totalTokens":26872}}}`, "Agent  26.9K / 262K  ", true, nil},
		{"pi million window", "pi", `{"type":"message_end","message":{"role":"assistant","content":[],` +
			`"provider":"llama-cpp","model":"qwen3.6-27b-1m","usage":{"input":600,"output":40,"cacheRead":0,"cacheWrite":0}}}`,
			"Agent  640 / 1.05M  ", false, nil},
		{"pi built-in provider", "pi", `{"type":"message_start","message":{"role":"assistant","content":[],` +
			`"provider":"openai-codex","model":"gpt-6.1-sol","usage":{"input":21,"output":0,"cacheRead":5499,` +
			`"cacheWrite":0}}}`, "Agent  5.52K / 500K  ", false, nil},
		{"pi model without a listed window", "pi", `{"type":"message_end","message":{"role":"assistant","content":[],` +
			`"provider":"openai-codex","model":"unlisted-model","usage":{"input":10,"output":5,"cacheRead":0,` +
			`"cacheWrite":0}}}`, "Agent  15 / ?  ", false, nil},
		{"pi built-in million window", "pi", `{"type":"message_end","message":{"role":"assistant","content":[],` +
			`"provider":"deepseek","model":"deepseek-flash","usage":{"input":600,"output":40,"cacheRead":0,"cacheWrite":0}}}`,
			"Agent  640 / 1M  ", false, nil},
		{"claude", "claude", `{"type":"assistant","message":{"role":"assistant","model":"claude-opus-5-5","content":[],` +
			`"usage":{"input_tokens":3,"cache_creation_input_tokens":4120,"cache_read_input_tokens":18234,` +
			`"output_tokens":8}}}`, "Agent  22.4K / ?  ", false, nil},
		{"opencode default model", "opencode", openCode, "Agent  7.42K / 131K  ", false, nil},
		{"opencode model", "opencode", openCode, "Agent  7.42K / 200K  ", false,
			[]string{"--model", "opencode/deepseek-v4.1-flash-free", "--effort", "high"}},
		{"opencode model without a window", "opencode", openCode, "Agent  7.42K / ?  ", false,
			[]string{"--model", "local/unlisted-model"}},
	} {
		t.Run(c.name, func(t *testing.T) { showsAgentRowContext(t, c, models, openCodeAPI) })
	}
}

type agentRowContext struct {
	name, harness, stream, want string
	narrow                      bool     // at 100 columns, the context does not fit beside the label
	args                        []string // the run's model and effort
}

// showsAgentRowContext runs c's worker, pi's models.json in models and the recorded OpenCode 2 answers in
// openCodeAPI, and waits for its Agent row to show c.want.
func showsAgentRowContext(t *testing.T, c agentRowContext, models, openCodeAPI string) {
	partial := filepath.Join(t.TempDir(), "partial")
	writeFixture(t, partial, "stderr") // the worker keeps running 3 s after its stream
	t.Setenv("FAKE_AGENT_PARTIAL", partial)
	t.Setenv("FAKE_AGENT_STREAM", c.stream+"\n")
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	// pi's own table: a built-in provider's models are not in models.json
	t.Setenv("FAKE_PI_MODELS", "openai-codex  gpt-5.6-luna   272K  128K  yes  yes\n"+
		"openai-codex  gpt-6.1-sol    500K  128K  yes  yes\n"+"deepseek      deepseek-flash  1M    384K  yes  yes\n")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	top := gitT(t, repo(t, "local", atmYAML), "rev-parse", "--show-toplevel")
	// the recorded OpenCode 2 answers reach only the worker's environment
	args := append([]string{"run", "--harness", c.harness, "--env", "PI_CODING_AGENT_DIR=" + models,
		"--env", "FAKE_OPENCODE_API=" + openCodeAPI}, c.args...)
	f := startFollow(t, versionServer(t, top, "follow-test"), top, true, append(args, issueFile(t, issue))...)
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
}
