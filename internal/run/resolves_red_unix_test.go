//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A red lint row goes to the agent at once, and a red test row runs once more first: one turn, then a question.
func TestChecksResolvesRedBeforeAsking(t *testing.T) {
	builtATM(t)
	const twoTurns = "if test -f first-turn; then touch repaired; else " + fixWork + "; touch first-turn; fi"
	const lintRed = "test -f repaired || { echo failed-check-finding; exit 1; }"
	const redThenGreen = `echo x >> "$ATM_TEST_RUNS"; test "$(wc -l < "$ATM_TEST_RUNS")" -gt 1 || ` +
		`{ echo failed-check-finding; exit 1; }`
	for _, c := range []resolveCase{
		{"lint red once", "lint", lintRed, "", twoTurns, false, 2, 0},
		{"lint red after the turn", "lint", "echo failed-check-finding; exit 1", "", fixWork, true, 2, 0},
		{"test red then green", "test", redThenGreen, "", fixWork, false, 1, 2},
		{"test red then green with lint red", "test", redThenGreen, lintRed, twoTurns, false, 2, 3},
		{"test red twice", "test", `echo x >> "$ATM_TEST_RUNS"; echo failed-check-finding; exit 1`, "", fixWork, true,
			2, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := atmSet(atmYAML, c.row, c.line)
			if c.lint != "" {
				cfg = atmSet(cfg, "lint", c.lint)
			}
			top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
			call, order, runs := filepath.Join(t.TempDir(), "call"), filepath.Join(t.TempDir(), "order"),
				filepath.Join(t.TempDir(), "runs")
			t.Setenv("FAKE_AGENT_CALL", call)
			t.Setenv("FAKE_AGENT_ORDER", order)
			t.Setenv("FAKE_AGENT_WORK", c.work)
			t.Setenv("ATM_TEST_RUNS", runs)
			bin := followUpsServer(t, top)
			var label string
			if c.asks {
				_, label = failedCheckQuestion(t, bin, top, "run", "--follow", issueFile(t, issue))
				answerFailedCheck(t, bin, top, label, "stop", 1)
			} else {
				followedRun(t, bin, top, 0, issueFile(t, issue))
				label = lastRun(t, top, "passed")
				if runs, _ := RecordedRuns(top); runs[len(runs)-1].FailedNode != "" {
					t.Fatalf("a passed run stopped at no node: %+v", runs[len(runs)-1])
				}
			}
			assertResolved(t, c, e2eReport(t, top, label), runDir(top, label), call, order, runs)
		})
	}
}

type resolveCase struct {
	name, row, line, lint, work string
	asks                        bool
	workers, runs               int
}

// assertResolved checks the run of c, its report rep in dir, asked or not, with the worker turns and runs of the
// test row c wants.
func assertResolved(t *testing.T, c resolveCase, rep *verdict, dir, call, order, runs string) {
	t.Helper()
	if asked := len(rep.Decisions) == 1 && rep.Decisions[0].Answer == "stop"; asked != c.asks ||
		!c.asks && (len(rep.Decisions) != 0 || rep.FailedNode != "") {
		t.Fatalf("want asked=%t: %+v", c.asks, rep)
	}
	if got := workerCalls(t, order); got != c.workers {
		t.Fatalf("want %d worker turns, got %d", c.workers, got)
	}
	fix, err := filepath.Glob(filepath.Join(dir, "worker-fix-*.jsonl"))
	if err != nil || len(fix) != c.workers-1 {
		t.Fatalf("want %d automatic turn, got %v, %v", c.workers-1, fix, err)
	}
	if got := agentCall(t, call); c.workers == 2 &&
		!strings.Contains(got.Stdin+strings.Join(got.Args, " "), "failed-check-finding") {
		t.Fatalf("the turn did not receive the output: %+v", got)
	}
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "x") != c.runs {
		t.Fatalf("want the test row run %d times, got %q", c.runs, b)
	}
	flaky := map[bool]int{true: 1}[c.row == "test" && !c.asks] // the first red of a test row that passed again
	if got := strings.Join(rep.Flaky, "\n"); strings.Count(got, "failed-check-finding") != flaky {
		t.Fatalf("want %d flaky: %v", flaky, rep.Flaky)
	}
}
