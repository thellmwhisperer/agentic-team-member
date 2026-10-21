//go:build unix

package run

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// The real atm run on a terminal: the report the agent's message ends with shows in the Agent box as an open
// Informe row, its summary's Markdown table rendered, and four folded rows with their counts, never as JSON; a click
// unfolds the findings, one row each, its severity first, and one on Informe folds it all.
func TestRealScreenShowsTheReportAsRows(t *testing.T) {
	bin := builtATM(t)
	top := gitT(t, repo(t, "https://example.com/o/r.git", atmYAML), "rev-parse", "--show-toplevel")
	stopServing(t, top)
	agent := filepath.Join(t.TempDir(), "agent")
	held(t, agent)
	report, _ := json.Marshal(obj{"test_file": "a_test.sh", "changed_files": []string{"a.txt", "b.txt", "c.txt"},
		"summary":      "Arreglo de **a.txt**.\n\n| Fichero | Cambio |\n|---|---|\n| `a.txt` | **fixed** |",
		"commands_run": []string{"sh a_test.sh"},
		"findings":     []obj{{"severity": "warning", "file": "a.txt", "description": "rompe si falta"}}})
	says, _ := json.Marshal(obj{"type": "assistant", "message": obj{"role": "assistant",
		"content": []obj{{"type": "text", "text": "Hecho.\n" + string(report)}}}})
	t.Setenv("FAKE_AGENT_STREAM", string(says)+"\n")
	t.Setenv("FAKE_AGENT_WORK", fmt.Sprintf(`cat "%s" >/dev/null; `, agent)+fixWork)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	a := &attached{t: t, cmd: exec.Command(bin, "run", issueFile(t, issue)), chunks: make(chan string, 64), rows: 50}
	a.cmd.Dir = top
	var err error
	if a.p, err = pty.StartWithSize(a.cmd, &pty.Winsize{Cols: 120, Rows: 50}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.cmd.Process.Kill(); _ = a.p.Close() })
	go a.read()

	// The fake agent's last row, its ended Skill call, comes before it waits: the rows hold still for the clicks.
	s := a.drawn(func(s string) bool {
		return strings.Contains(s, "Hallazgos (1)") && strings.Contains(s, "▸ · 1 tool: Skill")
	})
	for _, want := range []string{`✎ Hecho\.`, `▾ Informe`, `Arreglo de a\.txt\.`, `Fichero +Cambio`, `a\.txt +fixed`,
		`▸ Ficheros cambiados \(3\)`, `▸ Comandos ejecutados \(1\)`, `▸ Test a_test\.sh`, `▸ Hallazgos \(1\)`} {
		if !regexp.MustCompile(want).MatchString(s) {
			t.Fatalf("the Agent box lacks %s:\n%s", want, s)
		}
	}
	for _, raw := range []string{"{", `"summary"`, `"test_file"`, "|", "---", "**"} {
		if strings.Contains(s, raw) {
			t.Fatalf("the Agent box shows %q of the raw report:\n%s", raw, s)
		}
	}
	a.click(rowOf(s, "▸ Hallazgos"))
	s = a.drawn(regexp.MustCompile(`▾ Hallazgos \(1\) +│\n +│ {7}warning a\.txt rompe si falta`).MatchString)
	a.click(rowOf(s, "▾ Informe"))
	a.drawn(func(s string) bool { return strings.Contains(s, "▸ Informe") && !strings.Contains(s, "Hallazgos") })

	free(t, agent)
	timeout := time.After(2 * time.Minute)
	for open := true; open; {
		select {
		case _, open = <-a.chunks:
		case <-timeout:
			t.Fatal("atm run stayed open after the agent ended")
		}
	}
	if err := a.cmd.Wait(); err != nil {
		t.Fatalf("atm run: %v", err)
	}
}
