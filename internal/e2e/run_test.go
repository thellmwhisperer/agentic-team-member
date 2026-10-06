package e2e

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The #150 step that builds a piece unskips the tests that name it.
const (
	theRun   = "unskipped by #150, the step that ports the run: clone, agent, red/green, gates, follow-ups"
	delivery = "unskipped by #150, the step that ports the slop detector, delivery and the clone sweep"
	screen   = "unskipped by #150 step 6, the screen"
	axi      = "unskipped by #150, the step that ports atm axi"
)

// steps is the step graph of #130, in the order a green run with delivery walks it.
var steps = []string{"Prepare", "Agent · unit 1", "Without the fix", "With the fix", "Quality", "Gate · Scope",
	"Full suite", "Typecheck", "Follow-ups", "Slop detector", "Delivery", "Clone sweep"}

type verdict struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type unitReport struct {
	Unit        int     `json:"unit"`
	Passed      bool    `json:"passed"`
	BaseSHA     string  `json:"base_sha"`
	CommittedAs string  `json:"committed_as"`
	TestFile    string  `json:"test_file"`
	Verified    verdict `json:"verified"`
	GateOK      verdict `json:"gate_ok"`
	ScopeOK     verdict `json:"scope_ok"`
}

type runReport struct {
	unitReport
	Units    []unitReport `json:"units"`
	TimedOut bool         `json:"timed_out"`
	Ponytail struct {
		Kept bool `json:"kept"`
	} `json:"ponytail"`
}

func runDir(repo, label string) string { return filepath.Join(repo, ".atm", "runs", label) }

func readReport(t *testing.T, repo, label string) runReport {
	t.Helper()
	var r runReport
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(runDir(repo, label), "report.json"))), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// run is `atm run` of the e2e issue in a fresh target whose agent plays scenario.
func run(t *testing.T, scenario, label string, args ...string) (repo, out string, exit int) {
	t.Helper()
	repo = target(t)
	env, _ := fakes(t, scenario)
	args = append([]string{"run", "--label", label}, args...)
	out, exit = atm(t, repo, env, append(args, issueFile(t, issue))...)
	return repo, out, exit
}

// withDelivery commits a .atm.yaml whose delivery is command.
func withDelivery(t *testing.T, repo, command string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, ".atm.yaml"), "delivery:\n  command: "+command+"\n")
	git(t, repo, "add", ".atm.yaml")
	git(t, repo, "commit", "-q", "-m", "delivery")
}

type stepEvent struct {
	Type  string `json:"type"`
	Step  string `json:"step"`
	State string `json:"state"`
}

func stepEvents(t *testing.T, dir string) []stepEvent {
	t.Helper()
	logs, _ := filepath.Glob(filepath.Join(dir, "worker-*.jsonl"))
	if len(logs) != 1 {
		t.Fatalf("worker logs %v, want one", logs)
	}
	var events []stepEvent
	lines := bufio.NewScanner(strings.NewReader(readFile(t, logs[0])))
	lines.Buffer(nil, 1<<20)
	for lines.Scan() {
		var entry struct{ Event stepEvent }
		if json.Unmarshal(lines.Bytes(), &entry) == nil && entry.Event.Type == "atm.step" {
			events = append(events, entry.Event)
		}
	}
	return events
}

func TestRunWritesEveryStepStartBeforeItsEnd(t *testing.T) {
	t.Skip(delivery)
	repo := target(t)
	withDelivery(t, repo, "git status")
	env, _ := fakes(t, "fixing")
	if out, exit := atm(t, repo, env, "run", "--label", "graph", issueFile(t, issue)); exit != 0 {
		t.Fatalf("exit %d:\n%s", exit, out)
	}
	var open, started []string
	for _, e := range stepEvents(t, runDir(repo, "graph")) {
		if e.State == "started" {
			if len(open) != 0 {
				t.Fatalf("%s starts before %v ended", e.Step, open)
			}
			open, started = append(open, e.Step), append(started, e.Step)
			continue
		}
		if len(open) == 0 || open[len(open)-1] != e.Step || (e.State != "passed" && e.State != "failed") {
			t.Fatalf("%s %s ends while %v are open: no step runs without its start first", e.Step, e.State, open)
		}
		open = open[:len(open)-1]
	}
	if len(open) != 0 {
		t.Errorf("steps %v never ended", open)
	}
	if !slices.Equal(started, steps) {
		t.Fatalf("started steps %v, want exactly %v", started, steps)
	}
}

func TestRunExitCodes(t *testing.T) {
	t.Skip(delivery)
	if _, out, exit := run(t, "fixing", "zero"); exit != 0 {
		t.Errorf("a green unit exits %d, want 0:\n%s", exit, out)
	}
	if _, out, exit := run(t, "helper-only", "one"); exit != 1 {
		t.Errorf("a failed unit exits %d, want 1:\n%s", exit, out)
	}
	repo, _, _ := run(t, "fixing", "taken")
	env, _ := fakes(t, "fixing")
	if out, exit := atm(t, repo, env, "run", "--label", "taken", issueFile(t, issue)); exit != 2 {
		t.Errorf("a taken label exits %d, want 2:\n%s", exit, out)
	}
	repo = target(t)
	withDelivery(t, repo, "exit 3")
	if out, exit := atm(t, repo, env, "run", "--label", "four", issueFile(t, issue)); exit != 4 {
		t.Errorf("a failed delivery exits %d, want 4:\n%s", exit, out)
	}
}

func TestRunLeavesItsArtifactsOnDisk(t *testing.T) {
	t.Skip(delivery)
	repo := target(t)
	withDelivery(t, repo, "git status")
	env, _ := fakes(t, "follow-up")
	// The issue without the criterion: the follow-up is kept, not chained, so follow-ups.json is written.
	noCriterion := issueFile(t, "# add returns the difference\n")
	if out, exit := atm(t, repo, env, "run", "--label", "disk", noCriterion); exit != 0 {
		t.Fatalf("exit %d:\n%s", exit, out)
	}
	dir := runDir(repo, "disk")
	for _, name := range []string{"command.txt", "brief.md", "brief-ponytail.md", "report.json", "follow-ups.json",
		"delivery-output.txt", "atm"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("the run left no %s: %v", name, err)
		}
	}
	if logs, _ := filepath.Glob(filepath.Join(dir, "worker-*.jsonl")); len(logs) == 0 {
		t.Error("the run left no worker-*.jsonl")
	}
}

func TestRunVerdicts(t *testing.T) {
	for _, tc := range []struct {
		scenario string
		exit     int
		check    func(r runReport) bool
		later    string // the step that ports what the check needs
	}{
		{"fixing", 0, func(r runReport) bool { return r.Verified.OK && !r.Ponytail.Kept }, ""},
		{"new-module", 1, func(r runReport) bool {
			return strings.Contains(r.Units[0].Verified.Message, "INVALID RED")
		}, ""},
		{"helper-only", 1, func(r runReport) bool { return !r.GateOK.OK }, theRun},
		{"scope-breaking", 1, func(r runReport) bool {
			return !r.ScopeOK.OK && strings.Contains(r.ScopeOK.Message, "other.py")
		}, theRun},
		{"follow-up", 0, func(r runReport) bool {
			return len(r.Units) == 2 && r.Units[1].Passed && r.Units[1].TestFile == "tests/test_mul_neg.py" &&
				r.Units[0].CommittedAs != "" && r.Units[1].BaseSHA == r.Units[0].CommittedAs
		}, theRun},
		{"ponytail-cuts", 0, func(r runReport) bool { return r.Verified.OK && r.Ponytail.Kept }, theRun},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			if tc.later != "" {
				t.Skip(tc.later)
			}
			var args []string
			if tc.scenario == "scope-breaking" {
				args = []string{"--scope", "calc.py"}
			}
			repo, out, exit := run(t, tc.scenario, "verdict", args...)
			if r := readReport(t, repo, "verdict"); exit != tc.exit || !tc.check(r) {
				t.Errorf("exit %d (want %d), report %+v\n%s", exit, tc.exit, r, out)
			}
		})
	}
}

func TestAgentTimeoutKillsTheProcessGroup(t *testing.T) {
	start := time.Now()
	repo, out, exit := run(t, "sleeping", "slow", "--timeout", "2")
	// The grandchild holds the agent's stdout for a minute unless the whole group dies.
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("the run took %s: the agent's process group outlived its timeout", elapsed)
	}
	if r := readReport(t, repo, "slow"); exit != 1 || !r.TimedOut {
		t.Errorf("exit %d, timed_out %v, want 1 and true:\n%s", exit, r.TimedOut, out)
	}
}

func TestRunFromAFileNeedsNoGh(t *testing.T) {
	repo := target(t)
	env, log := fakes(t, "fixing")
	if out, exit := atm(t, repo, env, "run", "--label", "file", issueFile(t, issue)); exit != 0 {
		t.Fatalf("exit %d:\n%s", exit, out)
	}
	for _, call := range invocations(t, log) {
		if call.Name == "gh" {
			t.Errorf("a run from a file called gh %v", call.Args)
		}
	}
}

// #150 step 3: one unit in the foreground, from a clone, with the step lines on stdout and in the log.
func TestRunWorksAUnitInAClone(t *testing.T) {
	repo, out, exit := run(t, "fixing", "unit")
	if exit != 0 {
		t.Fatalf("exit %d:\n%s", exit, out)
	}
	unit := steps[:4]
	var started []string
	for i, e := range stepEvents(t, runDir(repo, "unit")) {
		if want := []string{"started", "passed"}[i%2]; e.State != want || e.Step != unit[i/2] {
			t.Fatalf("event %d is %s %s, want %s %s", i, e.Step, e.State, unit[i/2], want)
		}
		if e.State == "started" {
			started = append(started, e.Step)
		}
	}
	if !slices.Equal(started, unit) {
		t.Errorf("started steps %v, want %v", started, unit)
	}
	for _, step := range unit {
		step := regexp.QuoteMeta(step)
		if !regexp.MustCompile(`(?m)^▶ ` + step + `\n(?s:.*)^✓ ` + step + ` \d+(\.\d)? (s|min|h)\b`).MatchString(out) {
			t.Errorf("no ▶ and ✓ lines with a human duration for %s:\n%s", step, out)
		}
	}
	clones, _ := filepath.Glob(filepath.Join(repo, ".atm", "clones", "atm-run-*"))
	if len(clones) != 1 || git(t, clones[0], "rev-parse", "HEAD") != git(t, repo, "rev-parse", "main") ||
		!strings.Contains(readFile(t, filepath.Join(clones[0], ".git", "info", "exclude")), "/.atm/") {
		t.Fatalf("clones %v, want one at main that excludes /.atm/", clones)
	}
}

func TestRunHandsTheContractToTheAgent(t *testing.T) {
	repo := target(t)
	env, log := fakes(t, "fixing")
	args := []string{"run", "--label", "brief", "--scope", "calc.py", "--model", "m", "--effort", "low"}
	if out, exit := atm(t, repo, env, append(args, issueFile(t, issue))...); exit != 0 {
		t.Fatalf("exit %d:\n%s", exit, out)
	}
	brief := readFile(t, filepath.Join(runDir(repo, "brief"), "brief.md"))
	for _, section := range []string{"## GOAL", strings.TrimSpace(issue)[2:], "## SCOPE", "Allowed paths: `calc.py`",
		"## ACCEPTANCE", "## VERIFY", "python3 -m pytest", "## STYLE: ponytail", "## FORBIDDEN", "`noqa`",
		"## FOLLOW-UPS", "## REPORT"} {
		if !strings.Contains(brief, section) {
			t.Errorf("brief.md lacks %q:\n%s", section, brief)
		}
	}
	if calls := invocations(t, log); len(calls) != 1 || calls[0].Brief != brief ||
		!strings.Contains(strings.Join(calls[0].Args, " "), "--model m --effort low") {
		t.Errorf("agent calls %+v, want one claude call with the model, the effort and brief.md", calls)
	}
}

func TestInitWritesConfigAndIgnoresAtm(t *testing.T) {
	repo := target(t)
	env, _ := fakes(t, "fixing")
	if out, exit := atm(t, repo, env, "init"); exit != 0 {
		t.Fatalf("exit %d:\n%s", exit, out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".atm.yaml")); err != nil {
		t.Error("atm init wrote no .atm.yaml")
	}
	git(t, repo, "check-ignore", "-q", ".atm/runs/x/report.json") // exits 1, and fails the test, unless ignored
}

// #150 point 12: the summary says "14 min 01 s", not 841.27.
func TestSummaryDurationsAreHumanReadable(t *testing.T) {
	t.Skip(screen)
	_, out, exit := run(t, "fixing", "human")
	if exit != 0 || !regexp.MustCompile(`duration\s+(\d+ min )?\d+ s\b`).MatchString(out) {
		t.Errorf("exit %d, no human duration in the summary:\n%s", exit, out)
	}
	if raw := regexp.MustCompile(`\d+\.\d+ ?s\b`).FindString(out); raw != "" {
		t.Errorf("raw seconds %q in the summary:\n%s", raw, out)
	}
}

// #150 point 13: `atm axi run` prints TOON by default and JSON with --json.
func TestAxiRunPrintsToonByDefaultAndJSONWithFlag(t *testing.T) {
	t.Skip(axi)
	for golden, flags := range map[string][]string{"axi-run.toon": nil, "axi-run.json": {"--json"}} {
		repo := target(t)
		env, _ := fakes(t, "fixing")
		args := append(append([]string{"axi", "run", "--label", "golden"}, flags...), issueFile(t, issue))
		out, exit := atm(t, repo, env, args...)
		if want := readFile(t, filepath.Join("testdata", golden)); exit != 0 || out != want {
			t.Errorf("atm %v: exit %d\n%s\nwant testdata/%s:\n%s", args, exit, out, golden, want)
		}
	}
}
