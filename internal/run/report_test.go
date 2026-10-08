package run

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHuman(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0.0 s", 900 * time.Millisecond: "0.9 s", 59999 * time.Millisecond: "59.9 s", time.Minute: "1 min 00 s",
		4*time.Minute + 28*time.Second: "4 min 28 s", time.Hour + 2*time.Minute + 59*time.Second: "1 h 02 min",
	} {
		if got := Human(d); got != want {
			t.Errorf("human(%v) = %q, want %q", d, got, want)
		}
	}
}

// reportNode is the node name of report.json.
func reportNode(t *testing.T, rep obj, name string) obj {
	t.Helper()
	for _, n := range rep["nodes"].([]any) {
		if n := n.(obj); n["name"] == name {
			return n
		}
	}
	t.Fatalf("no node %s in %v", name, rep)
	return nil
}

// nodes is every node of a run, in order.
var nodes = []string{"issue", "clone", "contract", "agent", "checks", "ponytail", "delivery"}
var startedAtPattern = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)

func TestRunStopsAndReports(t *testing.T) {
	cases := []struct {
		name, atm, agent, failed, why string
		args                          []string
		code                          int
		results                       []string // each of nodes'
	}{
		{name: "passes without delivery", results: []string{"passed", "passed", "passed", "passed", "passed", "skipped"}},
		{name: "unreadable issue", args: []string{filepath.Join(t.TempDir(), "missing.md")}, code: 2,
			failed: "issue", results: []string{"failed", "not run", "not run", "not run", "not run", "not run"}},
		{name: "preparation fails", args: []string{"--base-ref", "nope"}, code: 2, failed: "clone", why: "nope",
			results: []string{"passed", "failed", "not run", "not run", "not run", "not run"}},
		{name: "agent fails", agent: "fail", code: 1, failed: "agent",
			results: []string{"passed", "passed", "passed", "failed", "not run", "not run"}},
		{name: "checks fail", atm: atmSet(atmYAML, "typecheck", "seq 100; exit 3"), code: 1, failed: "checks",
			why: "typecheck", results: []string{"passed", "passed", "passed", "passed", "failed", "not run"}},
		{name: "delivery passes", atm: atmSet(atmYAML, "delivery", `cp "$ATM_REPORT" "$ATM_TEST_OUT"`),
			results: []string{"passed", "passed", "passed", "passed", "passed", "passed"}},
		{name: "delivery fails", atm: atmSet(atmYAML, "delivery", `cp "$ATM_REPORT" "$ATM_TEST_OUT"; exit 5`),
			code: 4, failed: "delivery", why: "exit status 5",
			results: []string{"passed", "passed", "passed", "passed", "passed", "failed"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			runStopCase(t, c)
		})
	}
}

func runStopCase(t *testing.T, c struct {
	name, atm, agent, failed, why string
	args                          []string
	code                          int
	results                       []string
}) {
	t.Helper()
	results := append(append([]string{}, c.results[:5]...),
		cmp.Or(map[string]string{"passed": "passed"}[c.results[4]], "not run"), c.results[5])
	root := repo(t, "https://example.com/owner/repo.git", cmp.Or(c.atm, atmYAML))
	pre := filepath.Join(t.TempDir(), "pre.json")
	t.Setenv("ATM_TEST_OUT", pre)
	if c.agent != "" {
		t.Setenv("FAKE_AGENT", c.agent)
	}
	args := c.args
	if len(args) != 1 {
		args = append(args, issueFile(t, issue))
	}
	var out, summary bytes.Buffer
	err := Run("t", args, &out, &summary)
	if code := ExitCode(err); code != c.code || (c.code == 0) != (err == nil) {
		t.Fatalf("exit %d, %v; want exit %d", code, err, c.code)
	}
	rep := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
	checkStopReport(t, c, rep, err, results)
	checkEvents(t, &out, results)
	checkSummary(t, summary.String(), rep, results)
	checkCommands(t, c.name, rep, pre)
}

func checkStopReport(t *testing.T, c struct {
	name, atm, agent, failed, why string
	args                          []string
	code                          int
	results                       []string
}, rep obj, err error, results []string) {
	t.Helper()
	if rep["failed_node"] != c.failed || c.failed != "" && rep["reason"] != err.Error() ||
		!strings.Contains(rep["reason"].(string), c.why) {
		t.Fatalf("want %s failed naming %q, got %v: %v", c.failed, c.why, rep["failed_node"], rep["reason"])
	}
	if c.failed != "issue" && rep["type"] != "fix" {
		t.Fatalf("want the task type, got %v", rep["type"])
	}
	for i, name := range nodes {
		n := reportNode(t, rep, name)
		checkStopNode(t, n, name, results[i])
	}
}

func checkStopNode(t *testing.T, n obj, name, result string) {
	t.Helper()
	if _, ok := n["duration_ms"].(float64); n["result"] != result || !ok {
		t.Fatalf("node %s: %v, want %s with its duration_ms", name, n, result)
	}
	startedAt, ok := n["started_at"].(string)
	running := result == "passed" || result == "failed"
	if !running && ok || running && (!ok || !startedAtPattern.MatchString(startedAt)) {
		t.Fatalf("node %s: want started_at only when it ran, with UTC milliseconds, got %v", name, n)
	}
}

func TestRunningNodeIsPersistedWithStartedAt(t *testing.T) {
	dir := t.TempDir()
	r := newVerdict(dir, io.Discard)
	r.step("issue", func() (map[string]any, error) {
		b, err := os.ReadFile(r.path)
		if err != nil {
			return nil, err
		}
		var rep obj
		if err := json.Unmarshal(b, &rep); err != nil {
			return nil, err
		}
		var issue obj
		for _, n := range rep["nodes"].([]any) {
			if n := n.(obj); n["name"] == "issue" {
				issue = n
				break
			}
		}
		startedAt, ok := issue["started_at"].(string)
		if !ok || !startedAtPattern.MatchString(startedAt) || issue["result"] != "running" {
			return nil, fmt.Errorf("running node lacks its start time: %v", issue)
		}
		return nil, nil
	})
	if r.node("issue").Result != "passed" {
		t.Fatalf("want issue node to complete after its running report, got %s", r.node("issue").Result)
	}
}

// readReport is the report.json at path, after checking failed_node and reason come first.
func readReport(t *testing.T, path string) obj {
	t.Helper()
	raw, err := os.ReadFile(path)
	var rep obj
	if err != nil || json.Unmarshal(raw, &rep) != nil {
		t.Fatalf("report.json: %v\n%s", err, raw)
	}
	if !regexp.MustCompile(`^\{\s*"failed_node":[^\n]*\n\s*"reason":`).Match(raw) {
		t.Fatalf("want failed_node then reason at the top of report.json:\n%s", raw)
	}
	return rep
}

// checkEvents checks every node that ran wrote a start and then an end, with its raw duration, and no other.
func checkEvents(t *testing.T, out *bytes.Buffer, results []string) {
	t.Helper()
	evs := events(t, out)
	for i, name := range nodes {
		if results[i] != "passed" && results[i] != "failed" {
			continue
		}
		if len(evs) < 2 || evs[0]["step"] != name || evs[0]["state"] != "started" || evs[1]["step"] != name ||
			evs[1]["state"] != results[i] {
			t.Fatalf("want %s's start and end, got %v", name, evs)
		}
		if _, ok := evs[1]["duration_ms"].(float64); !ok {
			t.Fatalf("%s's end has no duration_ms: %v", name, evs[1])
		}
		evs = evs[2:]
	}
	if len(evs) != 0 {
		t.Fatalf("events after the last node: %v", evs)
	}
}

// checkSummary checks the summary names every node with its duration from the one formatter, never in ms.
func checkSummary(t *testing.T, summary string, rep obj, results []string) {
	t.Helper()
	for i, name := range nodes {
		want := results[i]
		if want == "passed" || want == "failed" {
			ms := reportNode(t, rep, name)["duration_ms"].(float64)
			want = Human(time.Duration(ms) * time.Millisecond)
		}
		if !regexp.MustCompile(`(?m)^` + name + ` +\S* *` + regexp.QuoteMeta(want) + `$`).MatchString(summary) {
			t.Fatalf("summary lacks %s %s:\n%s", name, want, summary)
		}
	}
	if regexp.MustCompile(`\d ?ms\b`).MatchString(summary) {
		t.Fatalf("summary in milliseconds:\n%s", summary)
	}
}

// checkCommands checks the commands' results in rep, of the run of case name, and the report.json delivery
// copied to pre.
func checkCommands(t *testing.T, name string, rep obj, pre string) {
	t.Helper()
	cmds, _ := rep["commands"].([]any)
	var tail []string
	for n := 41; n <= 100; n++ {
		tail = append(tail, strconv.Itoa(n))
	}
	switch name {
	case "checks fail":
		if last := cmds[len(cmds)-1].(obj); last["name"] != "typecheck" || last["result"] != "failed" ||
			last["tail"] != strings.Join(tail, "\n") {
			t.Fatalf("want typecheck failed with its last 60 lines, got %v", last)
		}
	case "passes without delivery":
		if len(cmds) != 1 || cmds[0].(obj)["name"] != "test" || cmds[0].(obj)["result"] != "passed" {
			t.Fatalf("want the test command's result, got %v", cmds)
		}
	case "delivery passes", "delivery fails":
		// report.json was on disk before delivery, with the checks passed and delivery running.
		before := readReport(t, pre)
		if reportNode(t, before, "checks")["result"] != "passed" ||
			reportNode(t, before, "delivery")["result"] != "running" {
			t.Fatalf("report.json before delivery: %v", before)
		}
		want := map[string]string{"delivery passes": "passed", "delivery fails": "failed"}[name]
		if last := cmds[len(cmds)-1].(obj); last["name"] != "delivery" || last["result"] != want {
			t.Fatalf("want the delivery command's result, got %v", last)
		}
	}
}

func TestTimeoutsUseTheFormatter(t *testing.T) {
	defer func(d time.Duration) { timeout = d }(timeout)
	timeout = 2 * time.Second
	if _, err := sh(t.TempDir(), "while :; do :; done"); err == nil || !strings.Contains(err.Error(), "after 2.0 s") {
		t.Fatalf("want the timeout in the formatter's words, got %v", err)
	}
}
