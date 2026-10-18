package run

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// noJSON is the validation error of a final message that is not a JSON object.
const noJSON = "agent left no report: no JSON object in its final message"

// validWorker is a worker report check accepts.
const validWorker = `{"test_file":"a_test.sh","changed_files":["a.txt"],"summary":"s",` +
	`"commands_run":[],"follow_ups":[]}`

// reportWork writes the nth final report from $FAKE_REPORT_1 and $FAKE_REPORT_2, and fixes a.txt.
const reportWork = `mkdir -p .atm
n=0
[ -f .atm/calls ] && n=$(cat .atm/calls)
n=$((n+1))
echo "$n" > .atm/calls
report=$FAKE_REPORT_1
[ "$n" != 1 ] && report=$FAKE_REPORT_2
printf '%s' "$report" > .atm/fake-report.json
echo fixed > a.txt
printf '%s\n' 'grep -q fixed a.txt && ! grep -q broken a.txt' > a_test.sh
`

func TestRejectedReportIsReturnedOnce(t *testing.T) {
	cases := []struct {
		name, first, second, wantErr string
	}{
		{name: "valid"},
		{name: "returned", first: "not json", second: validWorker},
		{name: "twice", first: "not json", second: `{"summary":"s"}`},
	}
	for _, harness := range []string{"claude", "codex", "pi", "cursor"} {
		for _, c := range cases {
			t.Run(harness+" "+c.name, func(t *testing.T) {
				assertReturnedReport(t, harness, c.name, c.first, c.second, c.wantErr)
			})
		}
	}
}

func assertReturnedReport(t *testing.T, harness, name, first, second, wantErr string) {
	t.Helper()
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	call := filepath.Join(t.TempDir(), "call.json")
	order := filepath.Join(t.TempDir(), "order")
	t.Setenv("FAKE_AGENT_CALL", call)
	t.Setenv("FAKE_AGENT_ORDER", order)
	t.Setenv("FAKE_AGENT_STREAM", `{"type":"session","id":"sess-1","session_id":"sess-1","thread_id":"sess-1"}`+"\n")
	if first != "" {
		t.Setenv("FAKE_AGENT_WORK", reportWork)
		t.Setenv("FAKE_REPORT_1", first)
		t.Setenv("FAKE_REPORT_2", second)
	}
	args := []string{"--harness", harness, "--model", "m"}
	if harness != "cursor" {
		args = append(args, "--effort", "e")
	}
	var out bytes.Buffer
	err := Run("t", append(args, issueFile(t, issue)), &out, io.Discard)
	workers := workerCalls(t, order)
	sent := sentPrompt(harness, agentCall(t, call))
	switch name {
	case "valid":
		assertValidReport(t, harness, err, &out, workers, sent, agentCall(t, call).Args)
	case "twice":
		assertMissingReport(t, err, &out, workers, 2)
	default:
		assertOneReturn(t, harness, err, &out, workers, sent, agentCall(t, call).Args)
	}
}

func assertValidReport(t *testing.T, harness string, err error, out *bytes.Buffer, workers int, sent string,
	args []string) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	end, _, _ := agentStep(t, out)
	if end["state"] != "passed" || end["rejected"] != nil || workers != 1 ||
		strings.Contains(sent, "Your final message was rejected") {
		t.Fatalf("a valid report was sent back: workers %d, sent %q, end %v", workers, sent, end)
	}
	if harness == "pi" && slices.Contains(args, "--no-session") {
		t.Fatalf("pi dropped its session: %v", args)
	}
}

// assertMissingReport: a report still missing after its return does not end the run at its agent: the unit
// goes to Checks with no test file, and report.json says report: missing.
func assertMissingReport(t *testing.T, err error, out *bytes.Buffer, workers, want int) {
	t.Helper()
	end, _, _ := agentStep(t, out)
	root := gitT(t, ".", "rev-parse", "--show-toplevel")
	rep := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
	n := reportNode(t, rep, "agent")
	units, _ := rep["units"].([]any)
	unit, _ := units[0].(map[string]any)
	if end["state"] != "passed" || end["report"] != "missing" || n["report"] != "missing" || n["result"] != "passed" ||
		unit["test_file"] != "" || rep["failed_node"] != "checks" || err == nil || workers != want {
		t.Fatalf("want a missing report to reach Checks: workers %d, err %v, end %v, report %v", workers, err, end, rep)
	}
}

func TestMissingReportWithoutAReturnReachesChecks(t *testing.T) {
	repo(t, "https://example.com/owner/repo.git", atmYAML)
	order := filepath.Join(t.TempDir(), "order")
	t.Setenv("FAKE_AGENT_ORDER", order)
	t.Setenv("FAKE_AGENT", "no-report")
	var out bytes.Buffer
	err := Run("t", []string{"--harness", "opencode", issueFile(t, issue)}, &out, io.Discard)
	assertMissingReport(t, err, &out, workerCalls(t, order), 1)
}

func assertOneReturn(t *testing.T, harness string, err error, out *bytes.Buffer, workers int, sent string,
	args []string) {
	t.Helper()
	msg := "Your final message was rejected: " + noJSON +
		". Reply with only the JSON object the brief asks for, nothing else."
	if err != nil {
		t.Fatal(err)
	}
	end, _, _ := agentStep(t, out)
	report, _ := end["report"].(map[string]any)
	if end["state"] != "passed" || end["rejected"] != noJSON || report["test_file"] != "a_test.sh" ||
		workers != 2 || sent != msg || !slices.Contains(args, "sess-1") {
		t.Fatalf("return: workers %d, sent %q, args %v, end %v", workers, sent, args, end)
	}
	assertResumeArgv(t, harness, args)
}

func assertResumeArgv(t *testing.T, harness string, args []string) {
	t.Helper()
	switch harness {
	case "codex":
		if !slices.Contains(args, "resume") || slices.Contains(args, "-C") || slices.Contains(args, "--sandbox") ||
			!slices.Contains(args, "--json") || !slices.Contains(args, `sandbox_mode="workspace-write"`) ||
			!slices.Contains(args, `model="m"`) || !slices.Contains(args, `model_reasoning_effort="e"`) {
			t.Fatalf("codex resume argv: %v", args)
		}
	case "pi":
		if slices.Contains(args, "--no-session") || !slices.Contains(args, "--session") {
			t.Fatalf("pi resume argv: %v", args)
		}
	case "claude", "cursor":
		if !slices.Contains(args, "--resume") {
			t.Fatalf("%s resume argv: %v", harness, args)
		}
	}
}

func sentPrompt(harness string, call struct {
	Args  []string
	Stdin string
	Env   []string
}) string {
	if harness == "claude" || len(call.Args) == 0 {
		return call.Stdin
	}
	return call.Args[len(call.Args)-1]
}

func workerCalls(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "worker" {
			n++
		}
	}
	return n
}
