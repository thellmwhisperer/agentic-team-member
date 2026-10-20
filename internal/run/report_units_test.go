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

// checkProof checks rep's proof of unit n, on base, with test and type typ: red, then green, then passed.
func checkProof(t *testing.T, what string, got any, n int, base, test, typ string) {
	t.Helper()
	u, _ := got.(obj)
	red, _ := u["red"].(obj)
	green, _ := u["green"].(obj)
	if u["unit"] != float64(n) || u["base"] != base || u["test_file"] != test || u["type"] != typ ||
		u["result"] != "passed" || !strings.Contains(red["command"].(string), test) || red["exit_code"] != float64(1) ||
		green["command"] != red["command"] || green["exit_code"] != float64(0) {
		t.Fatalf("%s %d: want it on %s with %s, %s, red then green and passed, got %v", what, n, base, test, typ, u)
	}
	if _, ok := red["tail"].(string); !ok {
		t.Fatalf("%s %d: want the red trial's output, got %v", what, n, red)
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

// resumedCut is chainedCut on a run that first failed at lint, then resumed, its resume point holding what a run
// that reached a unit 2 kept before follow-ups were retired: the follow-up unit 1 chained and unit 1's proof. It
// returns root, the base the failed run kept, and the resumed run's report and events.
func resumedCut(t *testing.T, atm string) (root, base string, rep obj, events []obj) {
	t.Helper()
	root = repo(t, "https://example.com/owner/repo.git", atmSet(atm, "lint", "false"))
	chainedCutEnv(t)
	base = gitT(t, root, "rev-parse", "main")
	if err := Run("failed", []string{issueFile(t, followUpIssue)}, io.Discard, io.Discard); err == nil {
		t.Fatal("want the unit's lint to fail")
	}
	commitConfig(t, root, atm)
	addLegacy(t, root, "failed", obj{
		"follow_up": obj{"title": "gap 1", "red_test": "c1_test.sh", "criterion": "A retry runs once after a timeout.",
			"test": "grep -q u2 a.txt\n", "verified": true},
		"proofs": [][4]string{{base, "a_test.sh", "fix", "grep -q fixed a.txt && ! grep -q broken a.txt\n"}}})
	var out capturedRun
	if err := Run("resumed", []string{"--resume", "failed", issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	return root, base, cutKept(t, root, "resumed", &out.Buffer), out.events
}

func TestReportKeepsWhenEveryNodeTrialAndCommandStartedAndTook(t *testing.T) {
	atm := atmSet(atmSet(atmInstall("true"), "typecheck", "true"), "delivery", "true")
	_, rep, events := chainedCut(t, atm)
	timed := reportTimeline(t, rep)
	checkTimeline(t, rep, events, timed)
	_, _, rep, events = resumedCut(t, atm)
	checkTimeline(t, rep, events, reportTimeline(t, rep))
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
		timed = append(timed, u["red"].(obj), u["green"].(obj))
	}
	var groups [][]obj // the commands of the clone, the unit's checks, the re-run checks and the delivery
	for _, c := range rep["commands"].([]any) {
		if c := c.(obj); c["name"] == "install" || len(groups) == 0 {
			groups = append(groups, []obj{c})
		} else {
			groups[len(groups)-1] = append(groups[len(groups)-1], c)
		}
	}
	if len(groups) != 3 || len(groups[2]) != 4 || groups[2][3]["name"] != "delivery" {
		t.Fatalf("want the clone's install, the unit's checks, the re-run checks and the delivery, got %v", groups)
	}
	timed = append(timed, groups[0]...)
	// Each checks' install runs before its proof, the rest after.
	timed = append(timed, groups[1][0])
	trials("units", 0)
	timed = append(timed, groups[1][1:]...)
	timed = append(timed, groups[2][0])
	trials("reproofs", 0)
	timed = append(timed, groups[2][1:]...)
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
	var proofRows []obj
	for _, ev := range events {
		if ev["check"] == "red/green" && ev["state"] == "passed" {
			proofRows = append(proofRows, ev)
		}
	}
	if len(proofRows) != 2 {
		t.Fatalf("want screen events for the unit's proof and its reproof, got %v", proofRows)
	}
	for i, key := range []string{"units", "reproofs"} {
		row := rep[key].([]any)[0].(obj)
		if row["duration_ms"] != proofRows[i]["duration_ms"] {
			t.Fatalf("%s 1 report span differs from screen row: %v vs %v", key, row, proofRows[i])
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
	_, base, rep, _ := resumedCut(t, atmInstall("true"))
	checkOneUnitReport(t, rep, base) // the base the failed run kept, not the config commit after it
	if units := rep["units"].([]any); units[0].(obj)["resumed_from"] != "failed" {
		t.Fatalf("want the unit resumed from the failed run, got %v", units[0])
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
	if u, r := units[0].(obj), reproofs[0].(obj); u["red"].(obj)["command"] != r["red"].(obj)["command"] {
		t.Fatalf("want the re-run proof on the unit's own test, got %v, %v", u, r)
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
