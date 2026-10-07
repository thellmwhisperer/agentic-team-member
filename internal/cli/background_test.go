package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// TestMain doubles as atm serve, the background process atm starts as this binary.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		Execute()
	}
	os.Exit(m.Run())
}

// toonFields is a TOON object's keys and values, or a TOON table's rows as objects, the way JSON has them.
func toonFields(t *testing.T, text string) any {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if head, ok := strings.CutPrefix(lines[0], "runs["); ok {
		_, keys, _ := strings.Cut(strings.TrimSuffix(head, "}:"), "{")
		rows := []any{}
		for _, l := range lines[1:] {
			r := csv.NewReader(strings.NewReader(strings.TrimPrefix(l, "  ")))
			values, err := r.Read()
			if err != nil {
				t.Fatal(err)
			}
			row := map[string]any{}
			for i, k := range strings.Split(keys, ",") {
				row[k] = values[i]
			}
			rows = append(rows, row)
		}
		return map[string]any{"runs": rows}
	}
	fields := map[string]any{}
	for _, l := range lines {
		k, v, _ := strings.Cut(l, ": ")
		if strings.HasPrefix(v, `"`) {
			var err error
			if v, err = strconv.Unquote(v); err != nil {
				t.Fatalf("%q: %v", l, err)
			}
		}
		fields[k] = v
	}
	return fields
}

func TestAxiPrintsTheSameOutcomeAsTOONAndJSON(t *testing.T) {
	end := time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC)
	cases := map[string]func(w io.Writer, asJSON bool) error{
		"outcome-failed": func(w io.Writer, asJSON bool) error {
			return outcome(w, asJSON, run.Outcome{Outcome: "failed", Run: "retry-on-timeout-2", FailedNode: "checks",
				Reason: "test: exit status 1", Report: "/repo/.atm/runs/retry-on-timeout-2/report.json", Issue: "7",
				NextStep: "read why checks failed in the report, fix it, then run it again: atm run 7"})
		},
		// Empty fields are printed all the same: compaction hides none.
		"outcome-passed": func(w io.Writer, asJSON bool) error {
			return outcome(w, asJSON, run.Outcome{Outcome: "passed", Run: "7-1", Report: "/repo/.atm/runs/7-1/report.json",
				NextStep: "review the branch the delivery command got"})
		},
		"runs": func(w io.Writer, asJSON bool) error {
			return list(w, asJSON, false, []run.Outcome{
				{Outcome: "passed", Run: "7-1", Issue: "7", Report: "/repo/.atm/runs/7-1/report.json",
					Started: end.Add(-268 * time.Second), Ended: end},
				{Outcome: "failed", Run: "retry-2", Issue: "/repo/a, b.md", FailedNode: "checks",
					Report: "/repo/.atm/runs/retry-2/report.json", Started: end.Add(-900 * time.Millisecond), Ended: end},
			})
		},
		"axi-status": func(w io.Writer, asJSON bool) error {
			return list(w, asJSON, true, nil)
		},
	}
	for name, print := range cases {
		t.Run(name, func(t *testing.T) {
			var toonOut, jsonOut bytes.Buffer
			if err := errors.Join(print(&toonOut, false), print(&jsonOut, true)); err != nil {
				t.Fatal(err)
			}
			for out, golden := range map[*bytes.Buffer]string{&toonOut: name + ".toon", &jsonOut: name + ".json"} {
				if want := strings.ReplaceAll(read(t, filepath.Join("testdata", golden)), "\r\n", "\n"); out.String() != want {
					t.Fatalf("%s: got\n%s\nwant\n%s", golden, out, want)
				}
			}
			var fromJSON any
			if err := json.Unmarshal(jsonOut.Bytes(), &fromJSON); err != nil {
				t.Fatal(err)
			}
			if fromTOON := toonFields(t, toonOut.String()); !reflect.DeepEqual(fromTOON, fromJSON) {
				t.Fatalf("TOON and JSON differ:\n%v\n%v", fromTOON, fromJSON)
			}
		})
	}
}

func TestAxiRunPreservesJSONHarnessArgument(t *testing.T) {
	args, asJSON := axiRunArgs([]string{"--harness-arg", "--json", "issue.md"})
	if asJSON || !reflect.DeepEqual(args, []string{"--harness-arg", "--json", "issue.md"}) {
		t.Fatalf("axiRunArgs = %v, %t", args, asJSON)
	}
	args, asJSON = axiRunArgs([]string{"--harness-arg", "--json", "--json", "issue.md"})
	if !asJSON || !reflect.DeepEqual(args, []string{"--harness-arg", "--json", "issue.md"}) {
		t.Fatalf("axiRunArgs with format flag = %v, %t", args, asJSON)
	}
}

func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if terminal(f) {
		t.Fatal("the null device was detected as a terminal")
	}
}

// atmIn runs atm with args, failing the test unless it exits with code.
func atmIn(t *testing.T, code int, args ...string) string {
	t.Helper()
	out, err := atm(t, args...)
	if run.ExitCode(err) != code {
		t.Fatalf("atm %v: exit %d (%v), want %d\n%s", args, run.ExitCode(err), err, code, out)
	}
	return out
}

// The background process is this test binary, as atm serve, which ends once its socket is gone with the test.
func cleanBackgroundServer(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.Remove(filepath.Join(dir, ".atm", "atm.sock")); err != nil && !os.IsNotExist(err) {
			t.Error(err)
		}
		log := filepath.Join(dir, ".atm", "serve.log")
		for deadline := time.Now().Add(7 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if err := os.Remove(log); err == nil || os.IsNotExist(err) {
				return
			}
		}
		t.Errorf("background process still holds %s", log)
	})
}

// Two atm run at once, with no background process yet, each start one: one serves both runs, and numbers them,
// the other connects to it.
func TestTwoRunsAtOnceShareOneBackgroundProcess(t *testing.T) {
	dir := repo(t, "https://example.com/o/r.git")
	cleanBackgroundServer(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "retry.md"), []byte("# Retry\nType: fix\nRetry.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outs, errs := make([]string, 2), make([]error, 2)
	var wg sync.WaitGroup
	for i := range outs {
		wg.Go(func() { outs[i], errs[i] = atm(t, "run", "retry.md") })
	}
	wg.Wait()
	slices.Sort(outs)
	if err := errors.Join(errs...); err != nil || !reflect.DeepEqual(outs, []string{"retry-1\n", "retry-2\n"}) {
		t.Fatalf("atm run twice at once: %q, %v", outs, err)
	}
	var runs struct{ Runs []map[string]string }
	if err := json.Unmarshal([]byte(atmIn(t, 0, "axi", "runs", "--json")), &runs); err != nil || len(runs.Runs) != 2 {
		t.Fatalf("atm axi runs --json: %+v, %v", runs, err)
	}
}

func TestRunGoesToTheBackgroundAndEveryCommandSeesIt(t *testing.T) {
	dir := repo(t, "https://example.com/o/r.git") // no .atm.yaml: every run fails at once
	cleanBackgroundServer(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "retry.md"), []byte("# Retry\nType: fix\nRetry.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := atmIn(t, 0, "run", "retry.md"); out != "retry-1\n" {
		t.Fatalf("atm run off a terminal prints the run's label, got %q", out)
	}
	atmIn(t, 2, "attach", "retry-1")
	if out := atmIn(t, 0, "status"); out != "nothing runs\n" {
		t.Fatalf("atm status: %q", out)
	}
	if out := atmIn(t, 0, "runs"); !strings.HasPrefix(out, "retry-1  failed  ") ||
		!strings.HasSuffix(out, " s  "+filepath.Join(dir, "retry.md")+"\n") {
		t.Fatalf("atm runs: %q", out)
	}
	out := atmIn(t, 2, "axi", "run", "retry.md")
	if !strings.HasPrefix(out, "outcome: failed\nrun: retry-2\nfailed_node: \"\"\nreason: \"config incomplete") ||
		!strings.Contains(out, "\nreport: \"\"\nnext_step: ") {
		t.Fatalf("atm axi run:\n%s", out)
	}
	var runs struct{ Runs []map[string]string }
	if err := json.Unmarshal([]byte(atmIn(t, 0, "axi", "runs", "--json")), &runs); err != nil || len(runs.Runs) != 2 ||
		runs.Runs[1]["run"] != "retry-2" || runs.Runs[1]["outcome"] != "failed" {
		t.Fatalf("atm axi runs --json: %+v, %v", runs, err)
	}
	if out := atmIn(t, 0, "axi", "status"); out != "runs[0]{run,issue,step,duration}:\n" {
		t.Fatalf("atm axi status: %q", out)
	}
}
