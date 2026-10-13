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
		name, kind string
		args       obj
		used       bool
	}{
		{"read", "read", obj{"path": target}, true},
		{"absolute read", "read", obj{"path": "<clone>/" + target}, true},
		{"normalized read", "read", obj{"path": "./.cursor/skills/ponytail/../ponytail/SKILL.md"}, true},
		{"read suffix", "read", obj{"path": target + ".bak"}, false},
		{"read display prefix", "read", obj{"path": target + "\n.bak"}, false},
		{"wrong installation", "read", obj{"path": ".agents/skills/ponytail/SKILL.md"}, false},
		{"echo only", "shell", obj{"command": "echo " + target}, false},
		{"cat suffix", "shell", obj{"command": "cat " + target + ".bak"}, false},
		{"cat", "shell", obj{"command": "cat " + target}, true},
		{"quoted absolute cat", "shell", obj{"command": "cat -- '<clone>/" + target + "'"}, true},
		{"normalized cat", "shell", obj{"command": "cat ./.cursor/skills/ponytail/../ponytail/SKILL.md"}, true},
		{"continued cat", "shell", obj{"command": "cat \\\n '" + target + "'"}, true},
		{"conditional cat", "shell", obj{"command": "false && cat " + target + "; true"}, false},
		{"nested cat", "shell", obj{"command": "echo 'cat " + target + "'"}, false},
		{"cat input replaced", "shell", obj{"command": "cat <<'EOF'\n" + target + "\nEOF"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo(t, "https://example.com/owner/repo.git", atmYAML)
			t.Setenv("FAKE_AGENT", "no-skill")
			t.Setenv("FAKE_AGENT_STREAM", cursorCall(c.kind, "started", "s2", c.args, nil)+
				cursorCall(c.kind, "completed", "s2", c.args, obj{"success": obj{}}))
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
