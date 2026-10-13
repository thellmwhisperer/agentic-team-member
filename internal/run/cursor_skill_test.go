package run

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestCursorSkillProofRequiresExactRead(t *testing.T) {
	const target = ".cursor/skills/ponytail/SKILL.md"
	for _, c := range []struct {
		name, kind     string
		args           obj
		used, succeeds bool
	}{
		{"read", "read", obj{"path": target}, true, true},
		{"absolute read", "read", obj{"path": "<clone>/" + target}, true, true},
		{"normalized read", "read", obj{"path": "./.cursor/skills/ponytail/../ponytail/SKILL.md"}, true, true},
		{"read suffix", "read", obj{"path": target + ".bak"}, false, true},
		{"read display prefix", "read", obj{"path": target + "\n.bak"}, false, true},
		{"wrong installation", "read", obj{"path": ".agents/skills/ponytail/SKILL.md"}, false, true},
		{"failed read tool", "read", obj{"path": target}, false, false},
		{"echo only", "shell", obj{"command": "echo " + target}, false, true},
		{"cat suffix", "shell", obj{"command": "cat " + target + ".bak"}, false, true},
		{"cat", "shell", obj{"command": "cat " + target}, true, true},
		{"quoted absolute cat", "shell", obj{"command": "cat -- '<clone>/" + target + "'"}, true, true},
		{"normalized cat", "shell", obj{"command": "cat ./.cursor/skills/ponytail/../ponytail/SKILL.md"}, true, true},
		{"continued cat", "shell", obj{"command": "cat \\\n '" + target + "'"}, true, true},
		{"sed", "shell", obj{"command": "sed -n '1,40p' " + target}, true, true},
		{"head", "shell", obj{"command": "head -n 40 " + target}, true, true},
		{"tail", "shell", obj{"command": "tail -40 " + target}, true, true},
		{"failed read", "shell", obj{"command": "sed -n '1,40p' " + target}, false, false},
		{"conditional cat", "shell", obj{"command": "false && cat " + target + "; true"}, false, true},
		{"nested cat", "shell", obj{"command": "echo 'cat " + target + "'"}, false, true},
		{"cat input replaced", "shell", obj{"command": "cat <<'EOF'\n" + target + "\nEOF"}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo(t, "https://example.com/owner/repo.git", atmYAML)
			t.Setenv("FAKE_AGENT", "no-skill")
			result := obj{"success": obj{}}
			if !c.succeeds {
				result = obj{"failure": obj{"exitCode": 1}}
			}
			t.Setenv("FAKE_AGENT_STREAM", cursorCall(c.kind, "started", "s2", c.args, nil)+
				cursorCall(c.kind, "completed", "s2", c.args, result))
			var out bytes.Buffer
			err := Run("t", []string{"--harness", "cursor", issueFile(t, issue)}, &out, io.Discard)
			if c.used {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "agent left no proof it used the ponytail skill") {
				t.Fatalf("non-read certified skill use: %v", err)
			}
		})
	}
}

func TestCursorErrorResultKeepsReachPrecedence(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	t.Setenv("FAKE_AGENT", "fail")
	t.Setenv("FAKE_AGENT_STREAM", cursorCall("shell", "started", "c1", obj{"command": "curl https://example.com"}, nil)+
		`{"type":"result","is_error":true,"result":"rate limited"}`+"\n")
	var out bytes.Buffer
	err := Run("t", []string{"--harness", "cursor", issueFile(t, issue)}, &out, io.Discard)
	want := "exit code 3; the agent reached outside its clone: shell curl https://example.com"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("want reach first, got %v", err)
	}
}
