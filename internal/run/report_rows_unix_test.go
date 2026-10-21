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
	checks := filepath.Join(t.TempDir(), "checks")
	cfg := atmSet(atmYAML, "typecheck", fmt.Sprintf(`cat "%s" >/dev/null`, checks))
	top := gitT(t, repo(t, "https://example.com/o/r.git", cfg), "rev-parse", "--show-toplevel")
	stopServing(t, top)
	agent := filepath.Join(t.TempDir(), "agent")
	held(t, agent)
	held(t, checks)
	call := filepath.Join(t.TempDir(), "call")
	t.Setenv("FAKE_AGENT_CALL", call)
	report, _ := json.Marshal(obj{"test_file": "a_test.sh", "changed_files": []string{"a.txt", "b.txt", "c.txt"},
		"summary":      "Arreglo de **a.txt**.\n\n| Fichero | Cambio |\n|---|---|\n| `a.txt` | **fixed** |",
		"commands_run": []string{"sh a_test.sh"},
		"findings":     []obj{{"severity": "warning", "file": "a.txt", "description": "rompe si falta"}}})
	says, _ := json.Marshal(obj{"type": "assistant", "message": obj{"role": "assistant",
		"content": []obj{{"type": "text", "text": "Hecho.\n" + string(report)}}}})
	t.Setenv("FAKE_AGENT_FINAL_STREAM", string(says)+"\n")
	final := "Hecho.\n" + string(report)
	t.Setenv("FAKE_AGENT_WORK", fmt.Sprintf(`cat "%s" >/dev/null; `, agent)+fixWork+
		"; mkdir -p .atm; cat > .atm/fake-report.json <<'REPORT'\n"+final+"\nREPORT\n")
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

	a.drawn(func(s string) bool { return strings.Contains(s, "▸ · 1 tool: Skill") })
	if brief := agentCall(t, call).Stdin; !strings.Contains(brief, "Write `summary` in Markdown, not HTML.") {
		t.Fatalf("the delivered worker brief does not require Markdown: %s", brief)
	}
	free(t, agent)
	s := a.drawn(func(s string) bool {
		return strings.Contains(s, "Hallazgos (1)") && strings.Contains(s, "▸ · 1 tool: Skill") && typecheckRuns.MatchString(s)
	})
	for _, want := range []string{`✎ Hecho\.`, `▾ Informe`, `Arreglo de a\.txt\.`, `Fichero +Cambio`, `a\.txt +fixed`,
		`▸ Ficheros cambiados \(3\)`, `▸ Comandos ejecutados \(1\)`, `▸ Test a_test\.sh`, `▸ Hallazgos \(1\)`} {
		if !regexp.MustCompile(want).MatchString(s) {
			t.Fatalf("the Agent box lacks %s:\n%s", want, s)
		}
	}
	previous := -1
	for _, title := range []string{"▸ · 1 tool: Skill", "✎ Hecho.", "▾ Informe", "Arreglo de a.txt.", "Ficheros cambiados (3)", "Comandos ejecutados (1)", "Test a_test.sh", "Hallazgos (1)"} {
		at := strings.Index(s, title)
		if at <= previous || strings.Count(s, title) != 1 {
			t.Fatalf("final report rows are duplicated or out of order at %q:\n%s", title, s)
		}
		previous = at
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

	for _, title := range []string{"Ficheros cambiados", "Comandos ejecutados", "Test a_test.sh"} {
		a.click(rowOf(a.drawn(func(s string) bool { return strings.Contains(s, "▸ Informe") }), "▸ Informe"))
		s = a.drawn(func(s string) bool { return strings.Contains(s, "▸ "+title) })
		a.click(rowOf(s, "▸ "+title))
		s = a.drawn(func(s string) bool { return strings.Contains(s, "▾ "+title) })
		a.click(rowOf(s, "▾ Informe"))
	}
	paths, err := filepath.Glob(filepath.Join(top, ".atm", "runs", "*", "worker.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("worker final message paths = %v, %v", paths, err)
	}
	var gotFinal string
	for _, line := range logLines(t, paths[0]) {
		var ev event
		_ = json.Unmarshal([]byte(line), &ev)
		if text, ends := ev.final("claude"); ends {
			gotFinal = strings.TrimSpace(text)
		}
	}
	if gotFinal != final {
		t.Fatalf("worker final message = %s; want %s", gotFinal, final)
	}
	if err := pty.Setsize(a.p, &pty.Winsize{Cols: 80, Rows: 50}); err != nil {
		t.Fatal(err)
	}
	a.drawn(func(s string) bool { return strings.Contains(s, "▸ Informe") && strings.Contains(s, "Log") })
	free(t, checks)
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

func TestRealScreenReportScopeAndMarkdown(t *testing.T) {
	for _, tc := range []struct {
		name, harness, summary string
		want, absent           []string
	}{
		{"split-blocks-escaped-pipe", "claude", "Split summary.\n\n| Value | Change |\n|---|---|\n| a\\|b | **fixed** |",
			[]string{`Split summary\.`, `Value +Change`, `a\|b +fixed`}, []string{`a\|b`, "|---|", "**"}},
		{"code-span-escaped-pipes", "claude", "Code cells.\n\n| `a\\|b` | Change |\n|---|---|\n| `a\\|b` | **fixed** |",
			[]string{`a\|b +Change`, `a\|b +fixed`}, []string{"`", `a\|b`, "|---|", "**"}},
		{"optional-outer-pipes", "codex", "Optional pipes.\n\nFichero | Cambio\n--- | ---\na.txt | fixed",
			[]string{`Optional pipes\.`, `Fichero +Cambio`, `a\.txt +fixed`}, []string{"Fichero | Cambio", "--- | ---"}},
		{"fenced-table", "opencode", "Literal table.\n\n```\n| Literal | Code |\n|---|---|\n| a\\|b | **fixed** |\n```\n\nAfter **code**.\n\n| Real | Table |\n|---|---|\n| yes | rendered |",
			[]string{`\| Literal \| Code \|`, `\|---\|---\|`, `\| a\\\|b \| \*\*fixed\*\* \|`, `After code\.`, `Real +Table`, `yes +rendered`}, []string{"```", "| Real | Table |"}},
		{"empty-table", "pi", "| Header | Empty |\n|---|---|",
			[]string{`Header +Empty`}, []string{"| Header | Empty |", "|---|---|"}},
		{"prose", "cursor", "Only **prose**.", []string{`Only prose\.`}, []string{"**"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := builtATM(t)
			dir := t.TempDir()
			checks, worker, ponytail := filepath.Join(dir, "checks"), filepath.Join(dir, "worker"), filepath.Join(dir, "ponytail")
			cfg := atmSet(atmYAML, "typecheck", fmt.Sprintf(`cat "%s" >/dev/null`, checks))
			top := gitT(t, repo(t, "https://example.com/o/r.git", cfg), "rev-parse", "--show-toplevel")
			stopServing(t, top)
			for _, path := range []string{checks, worker, ponytail} {
				held(t, path)
			}
			report, _ := json.Marshal(obj{"test_file": "a_test.sh", "changed_files": []string{"a.txt"},
				"summary": tc.summary, "commands_run": []string{"sh a_test.sh"}})
			final := "All green.\n" + string(report)
			t.Setenv("FAKE_AGENT_WORK", fmt.Sprintf(`cat "%s" >/dev/null; `, worker)+fixWork+
				"; mkdir -p .atm; cat > .atm/fake-report.json <<'REPORT'\n"+final+"\nREPORT\n")
			t.Setenv("FAKE_PONYTAIL_WORK", fmt.Sprintf(`cat "%s" >/dev/null`, ponytail))
			message := `Config is {"summary":"cache state","enabled":true}; next I will test it`
			intermediate := func(text string) string {
				var ev obj
				switch tc.harness {
				case "codex":
					ev = obj{"type": "item.completed", "item": obj{"type": "agent_message", "text": text}}
				case "opencode":
					ev = obj{"type": "text", "part": obj{"text": text}}
				case "pi":
					ev = obj{"type": "message_end", "message": obj{"role": "assistant", "content": []obj{{"type": "text", "text": text}}}}
				default:
					ev = obj{"type": "assistant", "message": obj{"role": "assistant", "content": []obj{{"type": "text", "text": text}}}}
				}
				b, _ := json.Marshal(ev)
				return string(b) + "\n"
			}
			ponytailReport := `{"summary":"Lean already.","findings":[]}`
			t.Setenv("FAKE_PONYTAIL_REPORT", ponytailReport)
			t.Setenv("FAKE_AGENT_STREAM", intermediate(message)+intermediate(ponytailReport))
			if tc.harness == "claude" {
				blocks, _ := json.Marshal(obj{"type": "assistant", "message": obj{"role": "assistant", "content": []obj{
					{"type": "text", "text": "All green."}, {"type": "text", "text": string(report)},
				}}})
				t.Setenv("FAKE_AGENT_FINAL_STREAM", string(blocks)+"\n")
			}
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("NO_COLOR", "")
			a := &attached{t: t, cmd: exec.Command(bin, "run", "--harness", tc.harness, issueFile(t, issue)), chunks: make(chan string, 64), rows: 60}
			a.cmd.Dir = top
			var err error
			if a.p, err = pty.StartWithSize(a.cmd, &pty.Winsize{Cols: 180, Rows: 60}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.cmd.Process.Kill(); _ = a.p.Close() })
			go a.read()
			s := a.drawn(func(s string) bool { return strings.Contains(s, message) && strings.Contains(s, "1 tool:") })
			if !strings.Contains(s, ponytailReport) || strings.Contains(s, "Informe") {
				t.Fatalf("intermediate JSON was converted or truncated:\n%s", s)
			}
			free(t, worker)
			s = a.drawn(func(s string) bool { return strings.Contains(s, "Hallazgos (0)") && typecheckRuns.MatchString(s) })
			for _, want := range tc.want {
				if !regexp.MustCompile(want).MatchString(s) {
					t.Fatalf("report lacks %s:\n%s", want, s)
				}
			}
			for _, raw := range append(tc.absent, `"test_file"`, `"commands_run"`) {
				if strings.Contains(s, raw) {
					t.Fatalf("report shows raw markup %q:\n%s", raw, s)
				}
			}
			if tc.harness == "claude" && (strings.Count(s, "✎ All green.") != 1 || strings.Count(s, "▾ Informe") != 1) {
				t.Fatalf("split final blocks lost prose or duplicated the report:\n%s", s)
			}
			if !strings.Contains(s, message) || !strings.Contains(s, ponytailReport) {
				t.Fatalf("final report replaced intermediate text:\n%s", s)
			}
			free(t, checks)
			s = a.drawn(func(s string) bool {
				return strings.Contains(s, "Slop detector") && strings.Contains(s, ponytailReport) && !strings.Contains(s, "Informe")
			})
			if strings.Contains(s, "Ficheros cambiados") || strings.Contains(s, "Comandos ejecutados") || strings.Contains(s, "Hallazgos") {
				t.Fatalf("ponytail received worker report rows:\n%s", s)
			}
			free(t, ponytail)
			timeout := time.After(2 * time.Minute)
			for open := true; open; {
				select {
				case _, open = <-a.chunks:
				case <-timeout:
					t.Fatal("atm run stayed open after ponytail ended")
				}
			}
			if err := a.cmd.Wait(); err != nil {
				t.Fatalf("atm run: %v", err)
			}
		})
	}
}

func TestRealScreenFinalReportKeyboardScrollDuringChecks(t *testing.T) {
	bin := builtATM(t)
	checks := filepath.Join(t.TempDir(), "checks")
	cfg := atmSet(atmYAML, "typecheck", fmt.Sprintf(`cat "%s" >/dev/null`, checks))
	top := gitT(t, repo(t, "https://example.com/o/r.git", cfg), "rev-parse", "--show-toplevel")
	stopServing(t, top)
	held(t, checks)
	var files, summary []string
	for i := 1; i <= 32; i++ {
		files = append(files, fmt.Sprintf("file-%02d.txt", i))
		if i <= 16 {
			summary = append(summary, fmt.Sprintf("summary-row-%02d", i))
		}
	}
	report, _ := json.Marshal(obj{"test_file": "a_test.sh", "changed_files": files,
		"summary": strings.Join(summary, "\n\n"), "commands_run": []string{"sh a_test.sh"}})
	t.Setenv("FAKE_AGENT_WORK", fixWork+"; mkdir -p .atm; cat > .atm/fake-report.json <<'REPORT'\n"+string(report)+"\nREPORT\n")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	a := &attached{t: t, cmd: exec.Command(bin, "run", issueFile(t, issue)), chunks: make(chan string, 64), rows: 24}
	a.cmd.Dir = top
	var err error
	if a.p, err = pty.StartWithSize(a.cmd, &pty.Winsize{Cols: 120, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.cmd.Process.Kill(); _ = a.p.Close() })
	go a.read()
	s := a.drawn(func(s string) bool { return strings.Contains(s, "Hallazgos (0)") && typecheckRuns.MatchString(s) })
	if strings.Contains(s, "summary-row-01") || strings.Contains(s, "▾ Informe") {
		t.Fatalf("report must overflow the constrained terminal:\n%s", s)
	}
	a.key("\t")
	a.drawn(func(s string) bool { return strings.Contains(s, "Log*") })
	a.key("\t")
	a.drawn(func(s string) bool { return strings.Contains(s, "ATM*") })
	a.key("\t")
	s = a.drawn(func(s string) bool { return strings.Contains(s, "Agent · claude default*") })
	a.click(rowOf(s, "▸ Ficheros cambiados (32)"))
	s = a.drawn(func(s string) bool { return strings.Contains(s, "file-32.txt") })
	if strings.Contains(s, "file-01.txt") {
		t.Fatalf("expanded files must overflow the constrained terminal:\n%s", s)
	}
	visible := regexp.MustCompile(`summary-row-\d+|file-\d+\.txt|▾ Informe|Hallazgos \(0\)`)
	seen := map[string]bool{}
	record := func(s string) {
		for _, item := range append(append([]string{}, files...), summary...) {
			if strings.Contains(s, item) {
				seen[item] = true
			}
		}
	}
	record(s)
	for i := 0; i < 20 && !strings.Contains(s, "▾ Informe"); i++ {
		before := strings.Join(visible.FindAllString(s, -1), "\n")
		a.key("\x1b[5~")
		s = a.drawn(func(s string) bool {
			return strings.Join(visible.FindAllString(s, -1), "\n") != before && strings.Contains(s, "Agent · claude default*")
		})
		record(s)
	}
	if !strings.Contains(s, "▾ Informe") || !strings.Contains(s, "summary-row-01") {
		t.Fatalf("PageUp cannot reach the report's beginning:\n%s", s)
	}
	for _, item := range append(append([]string{}, files...), summary...) {
		if !seen[item] {
			t.Errorf("PageUp skipped report content %q", item)
		}
	}
	clear(seen)
	record(s)
	for i := 0; i < 20 && !strings.Contains(s, "Hallazgos (0)"); i++ {
		before := strings.Join(visible.FindAllString(s, -1), "\n")
		a.key("\x1b[6~")
		s = a.drawn(func(s string) bool {
			return strings.Join(visible.FindAllString(s, -1), "\n") != before && strings.Contains(s, "Agent · claude default*")
		})
		record(s)
	}
	if !strings.Contains(s, "file-32.txt") || !strings.Contains(s, "Hallazgos (0)") {
		t.Fatalf("PageDown cannot reach the report's end:\n%s", s)
	}
	for _, item := range append(append([]string{}, files...), summary...) {
		if !seen[item] {
			t.Errorf("PageDown skipped report content %q", item)
		}
	}
	free(t, checks)
	a.exits()
}
