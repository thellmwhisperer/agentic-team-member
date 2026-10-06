package e2e

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type agentReport struct {
	TestFile     string   `json:"test_file"`
	ChangedFiles []string `json:"changed_files"`
	FollowUps    []struct {
		RedTest   string `json:"red_test"`
		Criterion string `json:"criterion"`
	} `json:"follow_ups"`
	Findings []struct {
		File string `json:"file"`
	} `json:"findings"`
}

type agentEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Item    struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Part struct {
		Text string `json:"text"`
	} `json:"part"`
	Message struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// agentOutput runs the fake under name in dir the way ATM calls that CLI.
func agentOutput(t *testing.T, name, dir string, env []string, brief string, args ...string) (agentEvent, string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(fakeDir, name+exe), args...)
	cmd.Dir, cmd.Env = dir, env
	if name == "claude" {
		cmd.Stdin = strings.NewReader(brief)
	} else {
		cmd.Args = append(cmd.Args, brief)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", name, err, out)
	}
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	var last agentEvent
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		t.Fatalf("%s: last line is not an event: %v\n%s", name, err, out)
	}
	text := last.Result + last.Item.Text + last.Part.Text
	for _, block := range last.Message.Content {
		text += block.Text
	}
	return last, text
}

// agent runs the fake under name in dir and returns the last message it streamed.
func agent(t *testing.T, name, dir string, env []string, brief string, args ...string) string {
	_, text := agentOutput(t, name, dir, env, brief, args...)
	return text
}

// reportOf is the JSON object at the end of the agent's last message.
func reportOf(t *testing.T, text string) agentReport {
	t.Helper()
	var r agentReport
	if err := json.Unmarshal([]byte(text[strings.Index(text, "{"):]), &r); err != nil {
		t.Fatalf("no report in %q: %v", text, err)
	}
	return r
}

func TestFakeAgentPlaysTheScenario(t *testing.T) {
	for _, tc := range []struct {
		scenario, testFile string
		files              map[string]string // path: what it holds after the agent ran
		then               func(t *testing.T, repo string, env []string, text string)
	}{
		{"fixing", "tests/test_add.py", map[string]string{"calc.py": "return a + b", "tests/test_add.py": "add(2, 3)"}, nil},
		{"helper-only", "", map[string]string{"helpers.py": "def helper", "calc.py": "return a - b"}, nil},
		{"new-module", "tests/test_add2.py", map[string]string{"calc2.py": "a + b", "tests/test_add2.py": "calc2"}, nil},
		{"scope-breaking", "tests/test_add.py", map[string]string{"other.py": "return 2", "calc.py": "a + b"}, nil},
		{"follow-up", "tests/test_add.py",
			map[string]string{".atm/follow-ups/1/tests/test_mul_neg.py": "mul(-1, 1) == -1"}, unitTwo},
		{"ponytail-cuts", "tests/test_add.py", map[string]string{"calc.py": "def unused(x):"}, ponytailCut},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			repo := target(t)
			env, _ := fakes(t, tc.scenario)
			text := agent(t, "claude", repo, env, issue)
			if tc.testFile != "" && reportOf(t, text).TestFile != tc.testFile {
				t.Errorf("test_file in %q, want %s", text, tc.testFile)
			}
			for path, want := range tc.files {
				if got := readFile(t, filepath.Join(repo, path)); !strings.Contains(got, want) {
					t.Errorf("%s = %q, want it to hold %q", path, got, want)
				}
			}
			if tc.then != nil {
				tc.then(t, repo, env, text)
			}
		})
	}
}

// unitTwo: the follow-up names a sentence of the issue, and unit 2 fixes mul on top of unit 1.
func unitTwo(t *testing.T, repo string, env []string, text string) {
	if fu := reportOf(t, text).FollowUps; len(fu) != 1 || !strings.Contains(issue, fu[0].Criterion) {
		t.Errorf("follow-ups %+v, want one whose criterion is in the issue", fu)
	}
	if r := reportOf(t, agent(t, "claude", repo, env, "# Follow-up unit 2\n")); r.TestFile != "tests/test_mul_neg.py" {
		t.Errorf("unit 2 reported %+v", r)
	}
	if calc := readFile(t, filepath.Join(repo, "calc.py")); !strings.Contains(calc, "return a * b") {
		t.Errorf("unit 2 left calc.py = %q", calc)
	}
}

func ponytailCut(t *testing.T, repo string, env []string, _ string) {
	r := reportOf(t, agent(t, "claude", repo, env, "# Ponytail pass\n"))
	if len(r.Findings) != 1 || r.Findings[0].File != "calc.py" {
		t.Errorf("findings %+v, want the one cut in calc.py", r.Findings)
	}
	if calc := readFile(t, filepath.Join(repo, "calc.py")); strings.Contains(calc, "unused") {
		t.Errorf("the ponytail pass left calc.py = %q", calc)
	}
}

func TestFakeAgentSpeaksTheProtocolOfItsName(t *testing.T) {
	for _, name := range []string{"claude", "codex", "opencode", "pi"} {
		t.Run(name, func(t *testing.T) {
			repo := target(t)
			env, log := fakes(t, "fixing")
			last := filepath.Join(t.TempDir(), "last.txt")
			event, text := agentOutput(t, name, repo, env, issue, "exec", "-o", last)
			if !validAgentEvent(name, event) {
				t.Errorf("%s emitted the wrong final event: %+v", name, event)
			}
			if reportOf(t, text).TestFile != "tests/test_add.py" {
				t.Errorf("no report in the last %s event: %q", name, text)
			}
			if name == "codex" && readFile(t, last) != text {
				t.Errorf("codex -o holds %q, want the last message", readFile(t, last))
			}
			calls := invocations(t, log)
			if len(calls) != 1 || calls[0].Name != name || calls[0].Brief != issue {
				t.Errorf("log %+v, want one %s call with the brief", calls, name)
			}
		})
	}
}

func validAgentEvent(name string, event agentEvent) bool {
	switch name {
	case "claude":
		return event.Type == "result" && event.Subtype == "success" && !event.IsError && event.Result != ""
	case "codex":
		return event.Type == "item.completed" && event.Item.Type == "agent_message" && event.Item.Text != ""
	case "opencode":
		return event.Type == "text" && event.Part.Text != ""
	case "pi":
		return validPiEvent(event)
	}
	return false
}

func validPiEvent(event agentEvent) bool {
	if event.Type != "message_end" || event.Message.Role != "assistant" {
		return false
	}
	for _, block := range event.Message.Content {
		if block.Type == "text" && block.Text != "" {
			return true
		}
	}
	return false
}

func TestGhFailsClosed(t *testing.T) {
	env, log := fakes(t, "fixing")
	cmd := exec.Command(filepath.Join(fakeDir, "gh"+exe), "auth", "status")
	cmd.Env = env
	if err := cmd.Run(); err == nil {
		t.Fatal("gh auth status succeeded: a test could reach github.com")
	}
	if calls := invocations(t, log); len(calls) != 1 || calls[0].Name != "gh" {
		t.Errorf("log %+v, want the gh call", calls)
	}
}
