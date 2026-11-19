//go:build unix

package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The recorded last-call usage and window are the 17-Nov-2026 session payload quoted in #713.
// Other records exercise missing usage, another session, partial writes and later calls.
func codexContextRow(t *testing.T) {
	recorded := fixture(t, "codex-context.jsonl")
	for _, customHome := range []bool{false, true} {
		t.Run(fmt.Sprintf("CODEX_HOME=%t", customHome), func(t *testing.T) {
			t.Setenv("CODEX_HOME", "")
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("FAKE_AGENT_STREAM", `{"type":"thread.started","thread_id":"713-session"}`+"\n"+
				`{"type":"item.completed","item":{"type":"agent_message","text":"Reading codex session."}}`+"\n"+
				`{"type":"item.completed","item":{"type":"agent_message","text":"{\"summary\":\"codex-report-held\"}"}}`+"\n")
			t.Setenv("FAKE_AGENT_FINAL_STREAM", `{"type":"turn.completed","usage":{"input_tokens":2067781,`+
				`"cached_input_tokens":2014592,"output_tokens":49888}}`+"\n")
			top := repo(t, "local", atmYAML)
			_, release := heldWork(t)
			defer release()
			home := filepath.Join(os.Getenv("HOME"), ".codex")
			args := []string{"run", "--harness", "codex", issueFile(t, issue)}
			if customHome {
				t.Setenv("CODEX_HOME", t.TempDir()) // the worker's --env must win over ATM's environment
				home = t.TempDir()
				args = append(args, "--env", "CODEX_HOME="+home)
			}
			log := filepath.Join(home, "sessions", "2026", "11", "17", "rollout-2026-11-17T12-00-00-713-session.jsonl")
			if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(filepath.Dir(log), "rollout-000-other-session.jsonl"),
				strings.ReplaceAll(recorded, "475000", "999000"))
			f := startFollow(t, versionServer(t, top, "follow-test"), top, true, args...)
			sc := &screenRead{f: f}
			sc.resized(160, func(s string) bool { return strings.Contains(s, "Reading codex session.") })
			// The session can appear after thread.started. A partial record must survive EOF until completed.
			writeFixture(t, log, "not json\n"+`{"type":"event_msg","payload":{"type":"token_count","info":null}}`+"\n"+
				`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":0}}}}`+"\n"+
				`{"type":"response_item","payload":{"type":"message"}}`+"\n"+recorded[:len(recorded)/2])
			sc.until(func(s string) bool { return strings.Contains(s, "Agent  1.") })
			appendCodexContext(t, log, recorded[len(recorded)/2:])
			sc.until(func(s string) bool { return strings.Contains(s, "Agent  34.7K / 475K  ") })
			// No stdout accompanies this call, and neither cached tokens nor the turn total count twice.
			appendCodexContext(t, log, `{"type":"event_msg","payload":{"type":"token_count","info":{`+
				`"total_token_usage":{"total_tokens":2117669},"last_token_usage":{"input_tokens":12000,`+
				`"cached_input_tokens":10000,"output_tokens":340,"total_tokens":12340},"model_context_window":321000}}}`+"\n")
			sc.until(func(s string) bool { return strings.Contains(s, "Agent  12.3K / 321K  ") })
			if strings.Contains(ansi.Strip(sc.shown.String()), "codex-report-held") {
				t.Fatalf("a context update flushed the pending JSON report:\n%s", ansi.Strip(sc.shown.String()))
			}
			release()
			sc.until(func(s string) bool { return strings.Contains(s, "q close") })
			screen := ansi.Strip(sc.shown.String())
			if strings.Contains(screen, "/ 999K") || strings.Contains(screen, "2.12M /") {
				t.Fatalf("another session or the turn total reached the Agent row:\n%s", screen)
			}
			f.exit(0)
			data, err := os.ReadFile(filepath.Join(runDir(top, lastRun(t, top, "passed")), "worker.jsonl"))
			if err != nil || !strings.Contains(string(data), `"total_tokens":34684`) ||
				!strings.Contains(string(data), `"total_tokens":12340`) {
				t.Fatalf("last-call context was not recorded in the run: %v\n%s", err, data)
			}
		})
	}
}

func appendCodexContext(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(text)
	if closeErr := f.Close(); err != nil || closeErr != nil {
		t.Fatalf("append session: %v, close: %v", err, closeErr)
	}
}
