package run

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Run 343-48's contract reviewer read its skill with `cat AGENTS.md <skill>/SKILL.md <other>/SKILL.md`: the clone
// had no AGENTS.md, so cat exited 1 with the whole SKILL.md in its output. A codex command that names the
// skill's SKILL.md proves the skill once its output holds the skill's `name: <skill>` line, whatever its exit
// code; one whose output lacks that line proves nothing, even when it exits 0.
func TestCodexSkillReadInOutputProvesTheSkillWhateverItsExit(t *testing.T) {
	const item = `{"type":"item.completed","item":{"id":"s1","type":"command_execution",` +
		`"command":"cat AGENTS.md .agents/skills/ponytail-review/SKILL.md .agents/skills/ponytail/SKILL.md",` +
		`"exit_code":%d,"aggregated_output":%s}}`
	for _, tc := range []struct {
		name   string
		exit   int
		output string
		used   bool
	}{
		{"failed cat that printed the skill", 1,
			"cat: AGENTS.md: No such file or directory\n---\nname: ponytail-review\n---\n\nQuestion the task.\n", true},
		{"failed cat that printed the skill with CRLF line ends", 1,
			"cat: AGENTS.md: No such file or directory\r\n---\r\nname: ponytail-review\r\n", true},
		{"passing cat that printed the skill", 0,
			"---\nname: ponytail-review\n---\n", true},
		{"passing cat that printed no skill", 0,
			"cat: AGENTS.md: No such file or directory\n", false},
		{"failed cat that printed no skill", 1,
			"cat: AGENTS.md: No such file or directory\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := json.Marshal(tc.output)
			if err != nil {
				t.Fatal(err)
			}
			s := &stream{skill: "ponytail-review", live: func(map[string]any) {}}
			s.see("codex", []byte(fmt.Sprintf(item, tc.exit, output)))
			s.finish()
			if s.used != tc.used {
				t.Fatalf("exit %d with output %q: used = %v, want %v", tc.exit, tc.output, s.used, tc.used)
			}
		})
	}
}
