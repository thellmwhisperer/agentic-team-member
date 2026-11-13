//go:build unix

package run

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openCode2CLI follows OpenCode v2.3.0's help: it rejects the removed --pure, --dir and --variant, and requires the
// global --standalone (a private server instead of the background service) and run's --auto (approve permissions
// not explicitly denied). No model or service is contacted.
const openCode2CLI = `#!/bin/sh
set -eu
fail() { echo "$*" >&2; exit 1; }
for arg do
  case "$arg" in --pure|--dir|--variant) fail "Unrecognized flag: $arg in command opencode run" ;; esac
done
[ "$1" = --standalone ] || fail 'expected a private server: opencode --standalone'
shift
[ "$1" = run ] || fail 'expected opencode run'
shift
auto= format= model= agent=
while [ "$#" -gt 1 ]; do
  case "$1" in
    --auto) auto=1; shift ;;
    --format) format="$2"; shift 2 ;;
    -m|--model) model="$2"; shift 2 ;;
    --agent) agent="$2"; shift 2 ;;
    *) fail "Unrecognized flag: $1 in command opencode run" ;;
  esac
done
[ "$#" -eq 1 ] && [ -n "$1" ] || fail 'missing worker brief'
[ -n "$auto" ] || fail 'worker tools need --auto'
[ "$format" = json ] || fail 'expected JSON output'
[ "$model" = "$ATM_OC2_MODEL" ] || fail "wrong model/variant: $model"
[ "$agent" = local ] || fail "wrong agent: $agent"
case "$PWD" in "$ATM_OC2_ROOT"/.atm/clones/*) ;; *) fail "wrong working directory: $PWD" ;; esac
[ "$(git rev-parse --show-toplevel)" = "$PWD" ] || fail 'not in clone root'
[ ! -t 0 ] && [ -z "$(cat)" ] || fail 'worker must run without interactive input'
skill=
case "$1" in *ponytail-review*) skill=ponytail-review ;; esac
if [ -n "$skill" ]; then
  [ -f ".opencode/skills/$skill/SKILL.md" ] || fail "missing $skill skill"
else
  [ ! -e .opencode/skills/ponytail ] || fail 'the worker must not get the ponytail skill'
fi
printf '%s\n' "$PWD" >> "$ATM_OC2_CALLS"
echo '{"type":"step_start","timestamp":1,"sessionID":"ses_oc2","part":{"type":"step-start"}}'
echo '{"type":"reasoning","timestamp":2,"sessionID":"ses_oc2","part":{"type":"reasoning","text":"Checking the clone."}}'
if [ -z "$skill" ]; then
  echo fixed > a.txt
  echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh
  echo '{"type":"tool_use","timestamp":3,"sessionID":"ses_oc2","part":{"type":"tool",'\
'"callID":"call_oc2","tool":"bash","state":{"status":"completed",'\
'"input":{"command":"sh a_test.sh"},"output":"","title":"Test fix"}}}'
  sh a_test.sh
  report='{"test_file":"a_test.sh","changed_files":["a.txt","a_test.sh"],'
  report="$report"'"summary":"OpenCode 2 worker finished.","commands_run":["sh a_test.sh"]}'
else
  report='{"findings":[],"summary":"Lean already."}'
fi
text=$(printf '%s' "$report" | sed 's/"/\\"/g; s/^/"/; s/$/"/')
printf '{"type":"text","timestamp":4,"sessionID":"ses_oc2","part":{"type":"text","text":%s}}\n' "$text"
echo '{"type":"step_finish","timestamp":5,"sessionID":"ses_oc2","part":{"type":"step-finish",'\
'"reason":"stop","cost":0,"tokens":{"input":10,"output":20}}}'
`

func TestRunOpenCode2Compatibility(t *testing.T) {
	builtATM(t)
	for _, tc := range []struct{ name, model, effort, selected string }{
		{"default model", "", "", ""},
		{"model", "local/qwen3.8-27b", "", "local/qwen3.8-27b"},
		{"effort", "local/qwen3.8-27b", "none", "local/qwen3.8-27b#none"},
		{"inline variant", "local/qwen3.8-27b#low", "", "local/qwen3.8-27b#low"},
		{"effort overrides variant", "local/qwen3.8-27b#low", "none", "local/qwen3.8-27b#none"},
	} {
		t.Run(tc.name, func(t *testing.T) { runOpenCode2(t, tc.model, tc.effort, tc.selected) })
	}
}

func runOpenCode2(t *testing.T, model, effort, selected string) {
	t.Helper()
	root := gitT(t, repo(t, "local", atmSet(atmYAML, "delivery", "true")), "rev-parse", "--show-toplevel")
	fixture := t.TempDir()
	writeFixture(t, filepath.Join(fixture, "opencode"), openCode2CLI, 0o755)
	call := filepath.Join(fixture, "calls")
	for k, v := range map[string]string{
		"PATH":         fixture + string(os.PathListSeparator) + os.Getenv("PATH"),
		"ATM_OC2_ROOT": root, "ATM_OC2_MODEL": selected, "ATM_OC2_CALLS": call,
	} {
		t.Setenv(k, v)
	}
	bin := followUpsServer(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "--follow", "--harness", "opencode", "--model", model,
		"--effort", effort, "--harness-arg", "--agent", "--harness-arg", "local", issueFile(t, issue))
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("atm run --follow: %v\nstderr: %s\nevents: %s", err, &stderr, out)
	}
	assertOpenCode2Run(t, root, call, out)
}

func assertOpenCode2Run(t *testing.T, root, call string, out []byte) {
	t.Helper()
	var clone, log string
	var accepted bool
	evs := events(t, bytes.NewReader(out))
	thinking := has(evs, obj{"step": "agent", "thinking": "Checking the clone."})
	tool := has(evs, obj{"step": "agent", "tool": "bash", "result": "passed", "detail": "sh a_test.sh"})
	for _, ev := range evs {
		if ev["step"] != "agent" {
			continue
		}
		if ev["state"] == "passed" && ev["check"] == nil {
			r, _ := ev["report"].(map[string]any)
			accepted = r["test_file"] == "a_test.sh" && r["summary"] == "OpenCode 2 worker finished."
			log, _ = ev["log"].(string)
		}
	}
	rep := readReport(t, filepath.Join(runDir(root, lastRun(t, root, "passed")), "report.json"))
	clone, _ = reportNode(t, rep, "clone")["clone"].(string)
	for _, node := range []string{"agent", "checks", "ponytail", "delivery"} {
		if n := reportNode(t, rep, node); n["result"] != "passed" {
			t.Errorf("%s did not pass: %v", node, n)
		}
	}
	if b, err := os.ReadFile(call); err != nil || string(b) != clone+"\n"+clone+"\n" {
		t.Fatalf("worker and review must use the run's clone: %q, %v; clone %q", b, err, clone)
	}
	if !accepted || !thinking || !tool {
		t.Fatalf("OpenCode 2 events/report not accepted: report=%t thinking=%t tool=%t\n%s", accepted, thinking, tool, out)
	}
	if lines := logLines(t, log); len(lines) != 5 || !strings.Contains(lines[4], `"type":"step_finish"`) {
		t.Fatalf("OpenCode 2 event log: %q", lines)
	}
}
