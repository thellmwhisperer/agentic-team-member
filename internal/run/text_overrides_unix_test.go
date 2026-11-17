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
		if name == "ponytail-skill" {
			continue
		}
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
	t.Run("relative from a subdirectory", textOverrideRelative)
	t.Run("default rejection", textOverrideDefaultRejection)
	t.Run("resume", textOverrideResume)
	t.Run("errors", func(t *testing.T) { textOverrideErrors(t, bin) })
	t.Run("refused", func(t *testing.T) { textOverrideRefused(t, bin) })
	t.Run("ACP review skill", textOverrideACP)
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
	for _, n := range []int{worker, review} {
		if got := readTextCapture(t, capture, n, "ponytail"); got != "" {
			t.Fatalf("call %d received a ponytail skill: %q", n, got)
		}
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
	args, want := []string{"--harness", harness, "--system-md", path}, text
	if harness == "opencode" || harness == "cursor" {
		extra := filepath.Join(t.TempDir(), "extra.md")
		writeFixture(t, extra, "Appendix.")
		args, want = append(args, "--system-prompt", extra), text+"\n\nAppendix."
	}
	label, end := promptRun(t, bin, root, append(args, issueFile(t, issue))...)
	if end.Outcome != "passed" {
		t.Fatalf("run: %+v", end)
	}
	for _, path := range []string{call, call + ".ponytail"} {
		if got := recordedSystemPrompt(t, path); got != want {
			t.Fatalf("system override received %q, want %q", got, want)
		}
	}
	assertTextOverridesReport(t, root, label, map[string]string{"system-md": path}, map[string]string{"system-md": text})
}

func textOverrideAppend(t *testing.T) {
	root, capture := textOverrideSetup(t, false, false)
	base, extra := filepath.Join(root, "base.md"), filepath.Join(root, "extra.md")
	writeFixture(t, base, "Replacement.")
	writeFixture(t, extra, "Appendix.")
	_, args, got := appendedSystemRun(t, root, root, capture, "--system-md", base, "--system-prompt", extra)
	if got != "Replacement.\n\nAppendix." {
		t.Fatalf("append did not follow the replacement: %q", args)
	}
}

// appendedSystemRun runs atm from dir on the repository at root with args and an issue, and returns the run's
// label, the worker's argv captured in capture, and the system prompt that argv appends, empty when none.
func appendedSystemRun(t *testing.T, root, dir, capture string, args ...string) (string, []string, string) {
	t.Helper()
	bin := followUpsServer(t, root)
	label, end := promptRun(t, bin, dir, append(args, issueFile(t, issue))...)
	if end.Outcome != "passed" {
		t.Fatalf("run: %+v", end)
	}
	argv := strings.Split(readTextCapture(t, capture, 1, "args"), "\x00")
	if i := slices.Index(argv, "--append-system-prompt"); i >= 0 && i+1 < len(argv) {
		return label, argv, argv[i+1]
	}
	return label, argv, ""
}

func textOverrideRelative(t *testing.T) {
	root, capture := textOverrideSetup(t, false, false)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "rules.md"), "Root rules.")
	writeFixture(t, filepath.Join(sub, "rules.md"), "Sub rules.")
	label, args, got := appendedSystemRun(t, root, sub, capture, "--system-md", "rules.md")
	if got != "Sub rules." {
		t.Fatalf("relative --system-md was not read from the launching directory: %q", args)
	}
	assertTextOverridesReport(t, root, label, map[string]string{"system-md": filepath.Join(sub, "rules.md")},
		map[string]string{"system-md": "Sub rules."})
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
	t.Setenv("FAKE_AGENT_WORK", `if [ -e .claude/skills/ponytail ]; then touch "$ATM_TEXT_SKILL"; fi; `+fixWork)
	paths, texts := map[string]string{}, map[string]string{}
	args := []string{issueFile(t, issue)}
	for _, name := range overrideFlags {
		if name == "ponytail-skill" {
			continue
		}
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
	if _, err := os.Stat(received); !os.IsNotExist(err) {
		t.Fatalf("resumed worker received a ponytail skill: %v", err)
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

func textOverrideErrors(t *testing.T, bin string) {
	for _, name := range overrideFlags {
		for _, kind := range []string{"missing", "unreadable", "directory", "removed after preflight"} {
			if name == "ponytail-skill" && kind == "removed after preflight" {
				continue
			}
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
	textOverrideTemplateErrors(t)
}

// textOverrideTemplateErrors checks that an invalid template override fails the run.
func textOverrideTemplateErrors(t *testing.T) {
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
					top := gitT(t, root, "rev-parse", "--show-toplevel")
					t.Chdir(top) // keep the Unix socket name below the platform's path limit
					stopCheckRun(t, top, lastRun(t, top, "waiting"))
				}
			})
		}
	}
}

func textOverrideACP(t *testing.T) {
	root := repo(t, "local", atmYAML)
	call := textOverrideHermes(t)
	path := filepath.Join(root, "review.md")
	writeFixture(t, path, "ACP replacement ponytail-review-skill")
	args := []string{"--harness", "hermes", "--ponytail-review-skill", path, issueFile(t, issue)}
	bin := followUpsServer(t, root)
	_, end := promptRun(t, bin, root, args...)
	if end.Outcome != "passed" {
		t.Fatalf("ACP run: %+v", end)
	}
	for _, suffix := range []string{"", ".ponytail"} {
		_, text := promptText(hermesCall(t, call+suffix)["session/prompt"])
		if strings.Contains(text, system.Prompt) {
			t.Fatalf("ACP agent received a system text: %q", text)
		}
		if strings.Contains(text, "Forces the laziest") {
			t.Fatalf("ACP agent received the ponytail skill: %q", text)
		}
		if suffix == ".ponytail" && !strings.HasSuffix(text, "ACP replacement ponytail-review-skill") {
			t.Fatalf("ACP review skill was not replaced: %q", text)
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

func textOverrideRefused(t *testing.T, bin string) {
	for _, tc := range []struct {
		harness, cfg, why, flag string
	}{
		{"claude", "", "--ponytail-skill: atm run sends no ponytail skill", "--ponytail-skill"},
		{"hermes", "", "--system-md: harness hermes takes no SYSTEM.md", "--system-md"},
		{"opencode", "", "--system-md: harness opencode takes SYSTEM.md only with --system-prompt",
			"--system-md"},
		{"cursor", "", "--system-md: harness cursor takes SYSTEM.md only with --system-prompt", "--system-md"},
		{"claude", "ponytail: false\n", "--ponytail-brief-template: ponytail: false runs no ponytail step",
			"--ponytail-brief-template"},
		{"claude", "ponytail: false\n", "--ponytail-review-skill: ponytail: false runs no ponytail step",
			"--ponytail-review-skill"},
	} {
		t.Run(tc.why, func(t *testing.T) {
			root := repo(t, "local", atmYAML+tc.cfg)
			stopServing(t, root)
			path := filepath.Join(root, "override.md")
			writeFixture(t, path, "replacement")
			out, code := preflightLaunch(t, bin, root, "--follow", "--harness", tc.harness, tc.flag, path,
				issueFile(t, issue))
			assertStopped(t, root, out, code, "worker files", tc.why)
		})
	}
}
