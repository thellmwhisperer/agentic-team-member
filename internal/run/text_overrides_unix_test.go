//go:build unix

package run

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	system "github.com/thellmwhisperer/agentic-team-member"
)

var overrideFlags = []string{"system-md", "brief-template", "ponytail-brief-template", "ponytail-skill",
	"ponytail-review-skill", "rejection-message"}

// The recorder responds by invocation number, so even an empty brief or skill is exercised by a real run.
const textOverrideHarness = `#!/bin/sh
set -eu
n=1
while [ -f "$ATM_TEXT_CAPTURE/$n.args" ]; do n=$((n+1)); done
printf '%s\000' "$@" > "$ATM_TEXT_CAPTURE/$n.args"
cat > "$ATM_TEXT_CAPTURE/$n.stdin"
for skill in ponytail ponytail-review; do
  path=".claude/skills/$skill/SKILL.md"
  if [ -f "$path" ]; then cp "$path" "$ATM_TEXT_CAPTURE/$n.$skill"; fi
done
if [ "$n" -eq 1 ]; then
  echo fixed > a.txt
  echo 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh
fi
if [ "$ATM_TEXT_REJECT" = yes ] && [ "$n" -eq 1 ]; then
  printf '%s\n' '{"type":"system","session_id":"text-session"}'
  printf '%s\n' '{"type":"result","result":"not json"}'
elif [ "$n" -le "$ATM_TEXT_WORKER_CALLS" ]; then
  printf '%s\n' '{"type":"result","result":"{\"test_file\":\"a_test.sh\",'\
'\"changed_files\":[\"a.txt\",\"a_test.sh\"],\"summary\":\"fixed\",\"commands_run\":[]}"}'
else
  printf '%s\n' '{"type":"result","result":"{\"findings\":[],\"summary\":\"lean\"}"}'
fi
`

func TestRunTextOverrides(t *testing.T) {
	bin := builtATM(t)
	t.Run("defaults", func(t *testing.T) { runTextOverride(t, "", "") })
	for _, name := range overrideFlags {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/empty=%v", name, empty), func(t *testing.T) {
				text := "Replacement á \"quoted\" $(literal).\n\n"
				if strings.HasSuffix(name, "template") {
					text = "Replacement {{.Title}}: {{cmd .Test}}\n"
				}
				if empty {
					text = ""
				}
				runTextOverride(t, name, text)
			})
		}
	}
	for _, harness := range []string{"codex", "pi", "opencode", "cursor"} {
		for _, text := range []string{"Only this system instruction.\n", ""} {
			t.Run(harness+"/system="+text, func(t *testing.T) { textOverrideSystem(t, harness, text) })
		}
	}
	t.Run("append", textOverrideAppend)
	t.Run("default rejection", textOverrideDefaultRejection)
	t.Run("worker skill with review disabled", textOverrideWorkerOnlySkill)
	t.Run("resume", textOverrideResume)
	for _, harness := range []string{"pi", "hermes"} {
		t.Run("fix skill/"+harness, func(t *testing.T) { textOverrideFixSkill(t, harness) })
	}
	t.Run("errors", func(t *testing.T) { textOverrideErrors(t, bin) })
	for _, text := range []string{"ACP system replacement.\n", ""} {
		t.Run("ACP system="+text, func(t *testing.T) { textOverrideACP(t, text) })
	}
}

func textOverrideSetup(t *testing.T, reject, retry bool) (root, capture string) {
	t.Helper()
	root = repo(t, "local", atmYAML)
	stopServing(t, root)
	capture = t.TempDir()
	bin := t.TempDir()
	writeFixture(t, filepath.Join(bin, "claude"), textOverrideHarness, 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ATM_TEXT_CAPTURE", capture)
	t.Setenv("ATM_TEXT_REJECT", map[bool]string{true: "yes", false: "no"}[reject])
	t.Setenv("ATM_TEXT_WORKER_CALLS", map[bool]string{true: "2", false: "1"}[retry])
	return root, capture
}

func runTextOverride(t *testing.T, name, text string) {
	t.Helper()
	reject := name == "rejection-message"
	retry := reject && text != ""
	root, capture := textOverrideSetup(t, reject, retry)
	bin := followUpsServer(t, root)
	args := []string{"--harness", "claude", issueFile(t, issue)}
	path := filepath.Join(root, "replacement file.md")
	if name != "" {
		writeFixture(t, path, text)
		args = append(args, "--"+name, filepath.Base(path))
	}
	label, end := promptRun(t, bin, root, args...)
	if end.Outcome != "passed" {
		t.Fatalf("run: %+v", end)
	}
	worker, review := 1, 2
	if retry {
		review = 3
	}
	assertTextOverrideInputs(t, capture, worker, review, name, text)
	wantSkill := ""
	if name == "ponytail-skill" {
		wantSkill = text
	}
	if got := readTextCapture(t, capture, worker, "ponytail"); got != wantSkill {
		t.Fatalf("worker skill received %q, want %q", got, wantSkill)
	}
	assertTextOverridesReport(t, root, label, map[string]string{name: path}, map[string]string{name: text})
	if reject {
		if retry && readTextCapture(t, capture, 2, "stdin") != text {
			t.Fatal("rejection prompt did not replace the whole message")
		}
		if _, err := os.Stat(filepath.Join(capture, fmt.Sprint(review+1)+".args")); !os.IsNotExist(err) {
			t.Fatalf("unexpected extra agent call: %v", err)
		}
	}
}

func assertTextOverrideInputs(t *testing.T, capture string, worker, review int, name, text string) {
	t.Helper()
	wantSystem := system.Prompt
	if name == "system-md" {
		wantSystem = text
	}
	for _, n := range []int{worker, review} {
		args := strings.Split(readTextCapture(t, capture, n, "args"), "\x00")
		i := slices.Index(args, "--append-system-prompt")
		if i < 0 || args[i+1] != wantSystem {
			t.Fatalf("system prompt for call %d: %q", n, args)
		}
	}
	for _, tc := range []struct {
		name, suffix, marker string
		n                    int
	}{
		{"brief-template", "stdin", "# Brief", worker},
		{"ponytail-brief-template", "stdin", "# Ponytail pass", review},
		{"ponytail-review-skill", "ponytail-review", "name: ponytail-review\n", review},
	} {
		got := readTextCapture(t, capture, tc.n, tc.suffix)
		if name == tc.name {
			want := strings.ReplaceAll(text, "{{.Title}}", "Retry on timeout")
			want = strings.ReplaceAll(want, "{{cmd .Test}}", "`sh a_test.sh`")
			if text == "" && tc.suffix == "ponytail-review" {
				if _, err := os.Stat(filepath.Join(capture, fmt.Sprint(tc.n)+"."+tc.suffix)); !os.IsNotExist(err) {
					t.Fatalf("disabled skill still exists: %v", err)
				}
			} else if got != want {
				t.Fatalf("%s received %q, want %q", tc.name, got, want)
			}
		} else if !strings.Contains(got, tc.marker) {
			t.Fatalf("default %s changed: %q", tc.name, got)
		}
	}
}

func readTextCapture(t *testing.T, dir string, n int, suffix string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, fmt.Sprint(n)+"."+suffix))
	if err != nil && (!os.IsNotExist(err) || suffix == "stdin" || suffix == "args") {
		t.Fatal(err)
	}
	return string(b)
}

func assertTextOverridesReport(t *testing.T, root, label string, paths, texts map[string]string) {
	t.Helper()
	report := readReport(t, filepath.Join(runDir(root, label), "report.json"))
	settings := report["agent"].(map[string]any)
	got, _ := settings["text_overrides"].(map[string]any)
	delete(paths, "")
	if len(got) != len(paths) {
		t.Fatalf("text provenance: %v, want %v", got, paths)
	}
	for name, path := range paths {
		file, ok := got[name].(map[string]any)
		want := fmt.Sprintf("%x", sha256.Sum256([]byte(texts[name])))
		if !ok || file["file"] != path || file["sha256"] != want {
			t.Fatalf("%s provenance: %v, want %s %s", name, file, path, want)
		}
	}
}

func textOverrideSystem(t *testing.T, harness, text string) {
	t.Helper()
	root := repo(t, "local", atmYAML)
	call, path := filepath.Join(t.TempDir(), "call"), filepath.Join(t.TempDir(), "system.md")
	t.Setenv("FAKE_AGENT_CALL", call)
	writeFixture(t, path, text)
	bin := followUpsServer(t, root)
	label, end := promptRun(t, bin, root, "--harness", harness, "--system-md", path, issueFile(t, issue))
	if end.Outcome != "passed" {
		t.Fatalf("run: %+v", end)
	}
	for _, path := range []string{call, call + ".ponytail"} {
		if got := recordedSystemPrompt(t, path); got != text {
			t.Fatalf("system override received %q, want %q", got, text)
		}
	}
	assertTextOverridesReport(t, root, label, map[string]string{"system-md": path}, map[string]string{"system-md": text})
}

func textOverrideAppend(t *testing.T) {
	root, capture := textOverrideSetup(t, false, false)
	base, extra := filepath.Join(root, "base.md"), filepath.Join(root, "extra.md")
	writeFixture(t, base, "Replacement.")
	writeFixture(t, extra, "Appendix.")
	bin := followUpsServer(t, root)
	_, end := promptRun(t, bin, root, "--system-md", base, "--system-prompt", extra, issueFile(t, issue))
	if end.Outcome != "passed" {
		t.Fatalf("run: %+v", end)
	}
	args := strings.Split(readTextCapture(t, capture, 1, "args"), "\x00")
	if i := slices.Index(args, "--append-system-prompt"); i < 0 || args[i+1] != "Replacement.\n\nAppendix." {
		t.Fatalf("append did not follow the replacement: %q", args)
	}
}

func textOverrideDefaultRejection(t *testing.T) {
	root, capture := textOverrideSetup(t, true, true)
	bin := followUpsServer(t, root)
	_, end := promptRun(t, bin, root, issueFile(t, issue))
	if end.Outcome != "passed" {
		t.Fatalf("run: %+v", end)
	}
	want := "Your final message was rejected: " + noJSON +
		". Reply with only the JSON object the brief asks for, nothing else."
	if got := readTextCapture(t, capture, 2, "stdin"); got != want {
		t.Fatalf("default rejection changed: %q, want %q", got, want)
	}
}

func textOverrideResume(t *testing.T) {
	green := filepath.Join(t.TempDir(), "green")
	t.Setenv("ATM_TEXT_GREEN", green)
	root := repo(t, "local", atmYAML+"e2e: 'test -f \"$ATM_TEXT_GREEN\"'\n")
	call := filepath.Join(t.TempDir(), "call")
	t.Setenv("FAKE_AGENT_CALL", call)
	t.Setenv("FAKE_AGENT_STREAM", `{"type":"system","session_id":"text-session"}`+"\n")
	received := filepath.Join(t.TempDir(), "received-skill")
	t.Setenv("ATM_TEXT_SKILL", received)
	t.Setenv("FAKE_AGENT_WORK", `cat .claude/skills/ponytail/SKILL.md > "$ATM_TEXT_SKILL"; `+fixWork)
	paths, texts := map[string]string{}, map[string]string{}
	args := []string{issueFile(t, issue)}
	for _, name := range overrideFlags {
		paths[name] = filepath.Join(root, name+".md")
		texts[name] = "Saved " + name + ".\n"
		if name == "ponytail-brief-template" {
			texts[name] = "Saved `ponytail-review` {{.Title}}.\n"
		}
		writeFixture(t, paths[name], texts[name])
		args = append(args, "--"+name, paths[name])
	}
	bin := followUpsServer(t, root)
	label, end := promptRun(t, bin, root, args...)
	if end.FailedNode != "checks" {
		t.Fatalf("first run: %+v", end)
	}
	stopCheckRun(t, root, label)
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, green, "")
	resumed, end := promptRun(t, bin, root, "--resume", label)
	if end.Outcome != "passed" {
		t.Fatalf("resume: %+v", end)
	}
	if b, err := os.ReadFile(received); err != nil || string(b) != texts["ponytail-skill"] {
		t.Fatalf("resumed worker skill: %q, %v", b, err)
	}
	if got := recordedSystemPrompt(t, call+".ponytail"); got != texts["system-md"] {
		t.Fatalf("resumed system prompt: %q", got)
	}
	if got := agentCall(t, call+".ponytail").Stdin; got != "Saved `ponytail-review` Retry on timeout.\n" {
		t.Fatalf("resumed brief: %q", got)
	}
	assertTextOverridesReport(t, root, resumed, paths, texts)
	textOverrideReplaceOnResume(t, bin, root, label, call, paths, texts)
}

func textOverrideReplaceOnResume(t *testing.T, bin, root, label, call string, paths, texts map[string]string) {
	t.Helper()
	paths["system-md"] = filepath.Join(root, "new-system.md")
	texts["system-md"] = "A new rule on resume."
	writeFixture(t, paths["system-md"], texts["system-md"])
	resumed, end := promptRun(t, bin, root, "--resume", label, "--system-md", paths["system-md"])
	if end.Outcome != "passed" || recordedSystemPrompt(t, call+".ponytail") != texts["system-md"] {
		t.Fatalf("replacement on resume: %+v", end)
	}
	assertTextOverridesReport(t, root, resumed, paths, texts)
}

func textOverrideWorkerOnlySkill(t *testing.T) {
	root, capture := textOverrideSetup(t, false, false)
	worker, review := filepath.Join(root, "worker.md"), filepath.Join(root, "review.md")
	writeFixture(t, worker, "Custom worker skill.")
	writeFixture(t, review, "")
	bin := followUpsServer(t, root)
	_, end := promptRun(t, bin, root, "--ponytail-skill", worker, "--ponytail-review-skill", review, issueFile(t, issue))
	if end.Outcome != "passed" {
		t.Fatalf("run: %+v", end)
	}
	if got := readTextCapture(t, capture, 1, "ponytail"); got != "Custom worker skill." {
		t.Fatalf("worker skill: %q", got)
	}
	for _, skill := range []string{"ponytail", "ponytail-review"} {
		if got := readTextCapture(t, capture, 2, skill); got != "" {
			t.Fatalf("review received disabled skill %s: %q", skill, got)
		}
	}
}

func textOverrideErrors(t *testing.T, bin string) {
	for _, name := range overrideFlags {
		for _, kind := range []string{"missing", "unreadable", "directory", "removed after preflight"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				cfg := atmYAML
				if kind == "removed after preflight" {
					cfg += "compile: 'rm \"$ATM_TEXT_FILE\"'\n"
				}
				root := repo(t, "local", cfg)
				stopServing(t, root)
				path := filepath.Join(root, "override.md")
				switch kind {
				case "unreadable":
					writeFixture(t, path, "secret", 0o000)
				case "directory":
					path = t.TempDir()
				case "removed after preflight":
					writeFixture(t, path, "replacement")
					t.Setenv("ATM_TEXT_FILE", path)
				}
				out, code := preflightLaunch(t, bin, root, "--follow", "--"+name, path, issueFile(t, issue))
				if kind == "removed after preflight" {
					if code != 2 || !strings.Contains(out, "--"+name+": open "+path) {
						t.Fatalf("exit %d:\n%s", code, out)
					}
				} else {
					assertStopped(t, root, out, code, "worker files", "--"+name+":")
				}
			})
		}
	}
	for _, name := range []string{"brief-template", "ponytail-brief-template"} {
		for _, text := range []string{"{{", "{{.Missing}}"} {
			t.Run(name+"/invalid="+text, func(t *testing.T) {
				root := repo(t, "local", atmYAML)
				path := filepath.Join(root, "template.md")
				writeFixture(t, path, text)
				bin := followUpsServer(t, root)
				out, code := preflightLaunch(t, bin, root, "--follow", "--"+name, path, issueFile(t, issue))
				if code != 2 || !strings.Contains(out, "template:") {
					t.Fatalf("invalid template exited %d:\n%s", code, out)
				}
				if name == "ponytail-brief-template" {
					stopCheckRun(t, root, lastRun(t, root, "waiting"))
				}
			})
		}
	}
}

func textOverrideACP(t *testing.T, systemText string) {
	root := repo(t, "local", atmYAML)
	call := textOverrideHermes(t)
	args := []string{"--harness", "hermes", issueFile(t, issue)}
	systemPath := filepath.Join(root, "system.md")
	writeFixture(t, systemPath, systemText)
	args = append(args, "--system-md", systemPath)
	for _, name := range []string{"ponytail-skill", "ponytail-review-skill"} {
		path := filepath.Join(root, name+".md")
		writeFixture(t, path, "ACP replacement "+name)
		args = append(args, "--"+name, path)
	}
	bin := followUpsServer(t, root)
	_, end := promptRun(t, bin, root, args...)
	if end.Outcome != "passed" {
		t.Fatalf("ACP run: %+v", end)
	}
	for suffix, name := range map[string]string{"": "ponytail-skill", ".ponytail": "ponytail-review-skill"} {
		_, text := promptText(hermesCall(t, call+suffix)["session/prompt"])
		if !strings.HasPrefix(text, systemText) || strings.Contains(text, system.Prompt) {
			t.Fatalf("ACP system text was not replaced: %q", text)
		}
		if !strings.HasSuffix(text, "ACP replacement "+name) || strings.Contains(text, "Forces the laziest") {
			t.Fatalf("ACP skill was not replaced: %q", text)
		}
	}
}

func textOverrideHermes(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hermes")
	if err := os.Symlink(os.Args[0], bin); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	call := filepath.Join(t.TempDir(), "call")
	t.Setenv("FAKE_ACP_CALL", call)
	return call
}

func textOverrideFixSkill(t *testing.T, harness string) {
	marker := filepath.Join(t.TempDir(), "lint")
	t.Setenv("ATM_TEXT_LINT", marker)
	cfg := atmSet(atmYAML, "lint", `test -f "$ATM_TEXT_LINT" || { touch "$ATM_TEXT_LINT"; exit 1; }`)
	root := repo(t, "local", cfg)
	call := filepath.Join(t.TempDir(), "call")
	t.Setenv("FAKE_AGENT_CALL", call)
	if harness == "hermes" {
		call = textOverrideHermes(t)
	}
	skill := filepath.Join(root, "skill.md")
	text := "The replacement skill survives a fix.\n"
	writeFixture(t, skill, text)
	received := filepath.Join(t.TempDir(), "received-skill")
	t.Setenv("ATM_TEXT_SKILL", received)
	if harness == "pi" {
		t.Setenv("FAKE_AGENT_WORK", `cat .atm/skills/ponytail/SKILL.md > "$ATM_TEXT_SKILL"; `+fixWork)
	}
	bin := followUpsServer(t, root)
	_, end := promptRun(t, bin, root, "--harness", harness, "--ponytail-skill", skill, issueFile(t, issue))
	if end.Outcome != "passed" {
		t.Fatalf("fix run: %+v", end)
	}
	if harness == "pi" {
		if b, err := os.ReadFile(received); err != nil || string(b) != text {
			t.Fatalf("fix skill: %q, %v", b, err)
		}
		args := agentCall(t, call).Args
		if !slices.Contains(args, "--skill") || !strings.Contains(args[len(args)-1], "Fix this failure") {
			t.Fatalf("fix was not sent the skill: %q", args)
		}
	} else {
		_, prompt := promptText(hermesCall(t, call)["session/prompt"])
		if !strings.Contains(prompt, "Fix this failure") || !strings.HasSuffix(prompt, text) {
			t.Fatalf("ACP fix was not sent the skill: %q", prompt)
		}
	}
}
