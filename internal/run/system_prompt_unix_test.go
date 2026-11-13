//go:build unix

package run

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	system "github.com/thellmwhisperer/agentic-team-member"
)

func TestWorkerGetsTheRunSystemPrompt(t *testing.T) {
	bin := builtATM(t)
	for _, harness := range []string{"claude", "codex", "opencode", "pi", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			workerGetsSystemPrompt(t, harness)
		})
	}
	for _, kind := range []string{"missing", "unreadable", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := repo(t, "local", atmYAML)
			path := filepath.Join(t.TempDir(), "system.md")
			switch kind {
			case "unreadable":
				writeFixture(t, path, "secret", 0o000)
			case "directory":
				path = t.TempDir()
			}
			out, code := preflightLaunch(t, bin, root, "--system-prompt="+path, issueFile(t, issue))
			assertStopped(t, root, out, code, "worker files", "--system-prompt:")
		})
	}
	t.Run("help", func(t *testing.T) {
		root := repo(t, "local", atmYAML)
		out, _ := preflightLaunch(t, bin, root, "--help")
		if !strings.Contains(out, "[--system-prompt file]") {
			t.Fatalf("missing flag in usage:\n%s", out)
		}
	})
	t.Run("OpenCode inline config", openCodePromptConfig)
	t.Run("injection errors", systemPromptInjectionErrors)
	t.Run("removed after preflight", systemPromptRemovedAfterPreflight)
}

func openCodePromptConfig(t *testing.T) {
	for _, raw := range []string{"null", "preserved"} {
		t.Run(raw, func(t *testing.T) {
			root := repo(t, "local", atmYAML)
			prior := filepath.Join(t.TempDir(), "prior.md")
			writeFixture(t, prior, "Prior rule.")
			cfg := raw
			want := system.Prompt + "\n\n"
			if raw == "preserved" {
				cfg = fmt.Sprintf(`{"instructions":[%q],"agent":{"local":{"model":"local/model"}}}`, prior)
				want = "Prior rule.\n\n" + want
			}
			// The run's --env overrides an inherited inline config.
			t.Setenv("OPENCODE_CONFIG_CONTENT", "invalid inherited config")
			path, call := filepath.Join(t.TempDir(), "empty.md"), filepath.Join(t.TempDir(), "call")
			writeFixture(t, path, "")
			t.Setenv("FAKE_AGENT_CALL", call)
			bin := followUpsServer(t, root)
			out, code := preflightLaunch(t, bin, root, "--follow", "--harness", "opencode", "--env",
				"OPENCODE_CONFIG_CONTENT="+cfg, "--harness-arg=--agent", "--harness-arg=local",
				"--system-prompt="+path, issueFile(t, issue))
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if got := recordedSystemPrompt(t, call); got != want {
				t.Fatalf("received %q, want %q", got, want)
			}
			if raw == "preserved" {
				var effective string
				for _, kv := range agentCall(t, call).Env {
					if strings.HasPrefix(kv, "OPENCODE_CONFIG_CONTENT=") {
						effective = strings.TrimPrefix(kv, "OPENCODE_CONFIG_CONTENT=")
					}
				}
				if !strings.Contains(effective, `"agent":{"local":{"model":"local/model"}}`) {
					t.Fatalf("lost existing OpenCode agent config: %s", effective)
				}
			}
		})
	}
}

func systemPromptInjectionErrors(t *testing.T) {
	for _, tc := range []struct{ name, harness, install, cfg, why string }{
		{"OpenCode invalid config", "opencode", "", "{", "unexpected end of JSON input"},
		{"OpenCode output is a directory", "opencode",
			`for run in "$ATM_PROMPT_RUNS"/*; do mkdir "$run/system-prompt.md"; done`, "", "is a directory"},
		{"Cursor rules is a file", "cursor", "mkdir -p .cursor; touch .cursor/rules", "", "not a directory"},
		{"Cursor rules is read only", "cursor", "mkdir -p .cursor/rules; chmod 500 .cursor/rules", "", "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := repo(t, "local", atmSet(atmYAML, "install", tc.install))
			t.Setenv("ATM_PROMPT_RUNS", filepath.Join(root, ".atm", "runs"))
			t.Setenv("OPENCODE_CONFIG_CONTENT", tc.cfg)
			path := filepath.Join(t.TempDir(), "system.md")
			writeFixture(t, path, "Keep this rule.")
			bin := followUpsServer(t, root)
			out, code := preflightLaunch(t, bin, root, "--follow", "--harness", tc.harness,
				"--system-prompt", path, issueFile(t, issue))
			if code != 1 || !strings.Contains(out, tc.why) {
				t.Fatalf("exit %d, want agent failure %q:\n%s", code, tc.why, out)
			}
		})
	}
}

func systemPromptRemovedAfterPreflight(t *testing.T) {
	root := repo(t, "local", atmYAML+"compile: 'rm \"$ATM_PROMPT_FILE\"'\n")
	path := filepath.Join(t.TempDir(), "system.md")
	writeFixture(t, path, "Keep this rule.")
	t.Setenv("ATM_PROMPT_FILE", path)
	bin := followUpsServer(t, root)
	out, code := preflightLaunch(t, bin, root, "--follow", "--system-prompt", path, issueFile(t, issue))
	if code != 2 || !strings.Contains(out, "--system-prompt: open "+path) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func recordedSystemPrompt(t *testing.T, path string) string {
	t.Helper()
	var call struct {
		SystemPrompt string `json:"system_prompt"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &call); err != nil {
		t.Fatal(err)
	}
	return call.SystemPrompt
}

func assertPromptReport(t *testing.T, root, label, path, text string) {
	t.Helper()
	r := readReport(t, filepath.Join(runDir(root, label), "report.json"))
	if r["system_prompt_file"] != path || r["system_prompt_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) {
		t.Fatalf("system prompt provenance missing or wrong: %v", r)
	}
}

func workerGetsSystemPrompt(t *testing.T, harness string) {
	t.Helper()
	green := filepath.Join(t.TempDir(), "green")
	t.Setenv("ATM_PROMPT_GREEN", green)
	cfg := atmYAML + "e2e: 'test -f \"$ATM_PROMPT_GREEN\"'\n"
	root := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	call := filepath.Join(t.TempDir(), "call")
	t.Setenv("FAKE_AGENT_CALL", call)
	t.Setenv("FAKE_AGENT_STREAM", `{"type":"session","id":"sess-1","session_id":"sess-1","thread_id":"sess-1"}`+"\n")
	launch := filepath.Join(root, "launch")
	if err := os.Mkdir(launch, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "\nKeep the whole rule: á, \"quotes\", \\slashes, $(literal).\n\n  Last line.\n"
	path := filepath.Join(launch, "run system.md")
	writeFixture(t, path, text)
	issuePath := issueFile(t, issue)
	server := followUpsServer(t, root)
	label, end := promptRun(t, server, launch, "--harness", harness, "--system-prompt", "run system.md", issuePath)
	if end.FailedNode != "checks" {
		t.Fatalf("first run: %+v", end)
	}
	stopCheckRun(t, root, label)
	want := system.Prompt + "\n\n" + text
	if got := recordedSystemPrompt(t, call); got != want {
		t.Fatalf("worker system prompt: %q, want %q", got, want)
	}
	assertPromptReport(t, root, label, path, text)
	// Resume must use the saved bytes even after the original file disappears.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, green, "")
	resumed, end := promptRun(t, server, root, "--resume", label)
	if end.Outcome != "passed" {
		t.Fatalf("resume: %+v", end)
	}
	if got := recordedSystemPrompt(t, call+".ponytail"); got != want {
		t.Fatalf("resumed review system prompt: %q, want %q", got, want)
	}
	if harness != "opencode" {
		got := agentCall(t, call)
		if recordedSystemPrompt(t, call) != want || !strings.Contains(strings.Join(got.Args, " "), "sess-1") {
			t.Fatalf("resumed worker did not receive saved system prompt/session: %+v", got)
		}
	}
	assertPromptReport(t, root, resumed, path, text)
}

func promptRun(t *testing.T, bin, root string, args ...string) (string, Outcome) {
	t.Helper()
	out, code := preflightLaunch(t, bin, root, args...)
	if code != 0 {
		t.Fatalf("launch exited %d:\n%s", code, out)
	}
	label := strings.TrimSpace(out[strings.LastIndex(strings.TrimSpace(out), "\n")+1:])
	root = gitT(t, root, "rev-parse", "--show-toplevel")
	end, err := Attach(root, label, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return label, end
}
