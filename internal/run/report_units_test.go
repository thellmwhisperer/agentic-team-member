package run

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type capturedRun struct {
	bytes.Buffer
	events []obj
}

func (c *capturedRun) watch(b []byte) {
	var ev obj
	if json.Unmarshal(b, &ev) == nil {
		c.events = append(c.events, ev)
	}
}

func (c *capturedRun) Write(p []byte) (int, error) {
	var ev obj
	if json.Unmarshal(bytes.TrimSpace(p), &ev) == nil {
		c.events = append(c.events, ev)
	}
	return c.Buffer.Write(p)
}

// checkProof checks a unit's recorded result without the removed task classification or trials.
func checkProof(t *testing.T, what string, got any, n int, base, test, typ string) {
	t.Helper()
	u, ok := got.(obj)
	if !ok {
		t.Fatalf("%s %d: expected unit object, got %v", what, n, got)
	}
	if u["unit"] != float64(n) || u["base"] != base || u["test_file"] != test || u["result"] != "passed" {
		t.Fatalf("%s %d: want it on %s with %s, passed, got %v", what, n, base, test, u)
	}
	for _, removed := range []string{"type", "red", "green"} {
		if _, exists := u[removed]; exists {
			t.Fatalf("%s %d: removed %s retained for legacy type %s: %v", what, n, removed, typ, u)
		}
	}
	if _, ok := u["duration_ms"].(float64); !ok {
		t.Fatalf("unit duration missing: %v", u)
	}
	if started, ok := u["started_at"].(string); !ok || !startedAtPattern.MatchString(started) {
		t.Fatalf("unit start missing: %v", u)
	}
}

// chainedCut runs chainWork, which once chained two units and now runs one, under .atm.yaml atm, whose cut is
// kept, and returns root and its report.
func chainedCut(t *testing.T, atm string) (string, obj, []obj) {
	t.Helper()
	root := repo(t, "https://example.com/owner/repo.git", atm)
	chainedCutEnv(t)
	var out capturedRun
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	return root, cutKept(t, root, "t", &out.Buffer), out.events
}

// chainedCutEnv sets chainedCut's agents up: chainWork, which declares a follow-up, and a cut that is kept.
func chainedCutEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	t.Setenv("FAKE_PONYTAIL_WORK", "grep -v extra a.txt > cut; mv cut a.txt")
	t.Setenv("FAKE_AGENT_WORK", chainWork)
	t.Setenv("ATM_TEST_CRITERION", "A retry runs once after a timeout.")
	t.Setenv("ATM_TEST_EDIT", "ATM_TEST_CRITERION=none") // a unit 2's follow-up would quote no criterion
}

// cutKept is the report of run label of root, after checking its events, out, kept the cut and hold unit 1's
// alone, and its directory no brief of a unit 2.
func cutKept(t *testing.T, root, label string, out *bytes.Buffer) obj {
	t.Helper()
	if p := ends(t, bytes.NewBuffer(out.Bytes()), "ponytail"); len(p) != 1 || p[0]["kept"] != true {
		t.Fatalf("want the cut kept, got %v", p)
	}
	for _, ev := range events(t, bytes.NewBuffer(out.Bytes())) {
		if u, ok := ev["unit"]; ok && u != float64(1) {
			t.Fatalf("want unit 1's events alone, got %v", ev)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".atm", "runs", label, "brief-unit-2.md")); !os.IsNotExist(err) {
		t.Fatalf("want no brief of a unit 2, got %v", err)
	}
	return readReport(t, filepath.Join(root, ".atm", "runs", label, "report.json"))
}

func TestReportKeepsWhenEveryNodeTrialAndCommandStartedAndTook(t *testing.T) {
	atm := atmSet(atmSet(atmInstall("true"), "typecheck", "true"), "delivery", "true")
	_, rep, events := chainedCut(t, atm)
	timed := reportTimeline(t, rep)
	checkTimeline(t, rep, events, timed)
}

func reportTimeline(t *testing.T, rep obj) []obj {
	t.Helper()
	var timed []obj // in the order they ran
	for _, n := range rep["nodes"].([]any) {
		timed = append(timed, n.(obj))
	}
	trials := func(key string, n int) {
		u := rep[key].([]any)[n].(obj)
		timed = append(timed, u)
		for _, removed := range []string{"red", "green", "type"} {
			if _, ok := u[removed]; ok {
				t.Fatalf("%s still contains %s: %v", key, removed, u)
			}
		}
	}
	var groups [][]obj // the commands of the clone, each unit's checks, the re-run checks and the delivery
	for _, c := range rep["commands"].([]any) {
		if c := c.(obj); c["name"] == "install" || len(groups) == 0 {
			groups = append(groups, []obj{c})
		} else {
			groups[len(groups)-1] = append(groups[len(groups)-1], c)
		}
	}
	if len(groups) != 3 || len(groups[2]) != 4 || groups[2][3]["name"] != "delivery" {
		t.Fatalf("want the clone's install, each unit's checks, the re-run checks and the delivery, got %v", groups)
	}
	timed = append(timed, groups[0]...)
	// A unit now spans all its checks, starting before install.
	trials("units", 0)
	timed = append(timed, groups[1]...)
	// Ponytail checks recorded test content, then runs configured checks once.
	trials("reproofs", 0)
	timed = append(timed, groups[2]...)
	return timed
}

func checkTimeline(t *testing.T, rep obj, events []obj, timed []obj) {
	t.Helper()
	checkTimedSpans(t, timed, len(rep["nodes"].([]any)))
	checkProofScreenSpans(t, rep, events)
	checkCommandScreenSpans(t, rep, events)
}

func checkTimedSpans(t *testing.T, timed []obj, nodeCount int) {
	t.Helper()
	var last time.Time
	for i, x := range timed {
		s, _ := x["started_at"].(string)
		at, err := time.Parse(time.RFC3339, s)
		if _, ok := x["duration_ms"].(float64); !ok || err != nil || !startedAtPattern.MatchString(s) {
			t.Fatalf("want a started_at in UTC with milliseconds and a duration_ms, got %v", x)
		}
		if i >= nodeCount { // the nodes are listed by name, the rest as they ran
			if at.Before(last) {
				t.Fatalf("want %v to start no earlier than %s", x, last)
			}
			last = at
		}
	}
}

func checkProofScreenSpans(t *testing.T, rep obj, events []obj) {
	t.Helper()
	var checkSteps []obj
	for _, ev := range events {
		switch ev["check"] {
		case "red/green", "suite on the base", "test on the base":
			t.Fatalf("removed proof emitted a screen row: %v", ev)
		}
		if ev["step"] == "checks" && ev["check"] == nil && ev["state"] == "passed" {
			checkSteps = append(checkSteps, ev)
		}
	}
	units := rep["units"].([]any)
	if len(checkSteps) != len(units) {
		t.Fatalf("want one completed Checks step per unit: %v, %v", checkSteps, units)
	}
	for n, got := range units {
		unit := got.(obj)
		if unit["duration_ms"] != checkSteps[n]["duration_ms"] {
			t.Fatalf("unit %d span differs from Checks: %v vs %v", n+1, unit, checkSteps[n])
		}
		if _, ok := unit["started_at"].(string); !ok {
			t.Fatalf("missing unit start: %v", unit)
		}
	}
	for _, got := range rep["reproofs"].([]any) {
		if unit := got.(obj); unit["red"] != nil || unit["green"] != nil || unit["type"] != nil {
			t.Fatalf("ponytail retained task proof: %v", unit)
		}
	}
}

func checkCommandScreenSpans(t *testing.T, rep obj, events []obj) {
	t.Helper()
	commands := rep["commands"].([]any)
	rows := append([]obj{cloneInstallEvent(events)}, configuredCommandEvents(events)...)
	checkReportedCommandSpans(t, commands[:len(commands)-1], rows)
	checkDeliverySpan(t, commands[len(commands)-1].(obj), events)
}

func cloneInstallEvent(events []obj) obj {
	for _, ev := range events {
		if ev["step"] == "clone" && ev["check"] == "install" && ev["duration_ms"] != nil && ev["state"] != "started" {
			return ev
		}
	}
	return nil
}

func configuredCommandEvents(events []obj) []obj {
	var commandRows []obj
	for _, ev := range events {
		if isConfiguredCommandEvent(ev) {
			commandRows = append(commandRows, ev)
		}
	}
	return commandRows
}

func checkReportedCommandSpans(t *testing.T, commands []any, rows []obj) {
	t.Helper()
	if len(rows) != len(commands) {
		t.Fatalf("want screen rows for all non-delivery commands, got %d rows for %d commands", len(rows), len(commands))
	}
	for i, c := range commands {
		command, row := c.(obj), rows[i]
		if command["name"] != row["check"] || command["duration_ms"] != row["duration_ms"] {
			t.Fatalf("command report differs from its screen row: %v vs %v", command, row)
		}
	}
}

func checkDeliverySpan(t *testing.T, last obj, events []obj) {
	t.Helper()
	var deliveryEvent obj
	for _, ev := range events {
		if ev["step"] == "delivery" && ev["state"] != "started" && ev["duration_ms"] != nil {
			deliveryEvent = ev
		}
	}
	if last["name"] != "delivery" ||
		last["duration_ms"] != deliveryEvent["commands"].([]any)[0].(obj)["duration_ms"] {
		t.Fatalf("delivery report differs from its screen row: %v vs %v", last, deliveryEvent)
	}
}

func isConfiguredCommandEvent(ev obj) bool {
	return ev["check"] != nil && ev["check"] != "red/green" && ev["state"] != "started" &&
		ev["step"] != "clone" && ev["step"] != "delivery"
}

func TestReportKeepsEveryUnitTheReproofsAndTheInstall(t *testing.T) {
	root, rep, _ := chainedCut(t, atmInstall("true"))
	checkOneUnitReport(t, rep, gitT(t, root, "rev-parse", "main"))
	if units := rep["units"].([]any); units[0].(obj)["resumed_from"] != nil {
		t.Fatalf("want a unit its agent made, got %v", units[0])
	}
}

// checkOneUnitReport checks rep holds unit 1 alone, on base, proven on a_test.sh, its proof re-run once alike, and
// the clone's install, then the unit's checks and the re-run checks, each install first.
func checkOneUnitReport(t *testing.T, rep obj, base string) {
	t.Helper()
	units, _ := rep["units"].([]any)
	if len(units) != 1 {
		t.Fatalf("want the issue's unit alone in the report, got %v", rep["units"])
	}
	checkProof(t, "unit", units[0], 1, base, "a_test.sh", "fix")
	reproofs, _ := rep["reproofs"].([]any)
	if len(reproofs) != 1 {
		t.Fatalf("want the one proof the slop detector re-ran, got %v", rep["reproofs"])
	}
	checkProof(t, "re-run proof of unit", reproofs[0], 1, base, "a_test.sh", "fix")
	if u, r := units[0].(obj), reproofs[0].(obj); u["test_file"] != r["test_file"] {
		t.Fatalf("want the same reported test checked after the cut, got %v, %v", u, r)
	}
	var names []string
	for _, c := range rep["commands"].([]any) {
		c := c.(obj)
		if c["name"] == "install" && (c["command"] != "true" || c["result"] != "passed") {
			t.Fatalf("want each install passed, got %v", c)
		}
		names = append(names, c["name"].(string))
	}
	if got := strings.Join(names, " "); got != "install install test install test" {
		t.Fatalf("want the clone's install, then the unit's and the re-run checks', got %v", rep["commands"])
	}
}
