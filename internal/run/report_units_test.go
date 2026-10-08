package run

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
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

// chainedCut runs two chained units under .atm.yaml atm, whose cut is kept, and returns root and its report.
func chainedCut(t *testing.T, atm string) (string, obj, []obj) {
	t.Helper()
	root := repo(t, "https://example.com/owner/repo.git", atm)
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	t.Setenv("FAKE_PONYTAIL_WORK", "grep -v extra a.txt > cut; mv cut a.txt")
	t.Setenv("FAKE_AGENT_WORK", chainWork)
	t.Setenv("ATM_TEST_CRITERION", "A retry runs once after a timeout.")
	t.Setenv("ATM_TEST_EDIT", "ATM_TEST_CRITERION=none") // unit 2's follow-up quotes no criterion: two units
	var out capturedRun
	if err := Run("t", []string{issueFile(t, followUpIssue)}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if p := ends(t, &out.Buffer, "ponytail"); len(p) != 1 || p[0]["kept"] != true {
		t.Fatalf("want the cut kept, got %v", p)
	}
	return root, readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json")), out.events
}

func TestReportKeepsWhenEveryNodeTrialAndCommandStartedAndTook(t *testing.T) {
	_, rep, events := chainedCut(t, atmSet(atmSet(atmInstall("true"), "typecheck", "true"), "delivery", "true"))
	var timed []obj // in the order they ran
	for _, n := range rep["nodes"].([]any) {
		timed = append(timed, n.(obj))
	}
	trials := func(key string, n int) {
		u := rep[key].([]any)[n].(obj)
		timed = append(timed, u)
		timed = append(timed, u["red"].(obj), u["green"].(obj))
	}
	var groups [][]obj // the commands of the clone, each unit's checks, the re-run checks and the delivery
	for _, c := range rep["commands"].([]any) {
		if c := c.(obj); c["name"] == "install" || len(groups) == 0 {
			groups = append(groups, []obj{c})
		} else {
			groups[len(groups)-1] = append(groups[len(groups)-1], c)
		}
	}
	if len(groups) != 4 || len(groups[3]) != 4 || groups[3][3]["name"] != "delivery" {
		t.Fatalf("want the clone's install, each unit's checks, the re-run checks and the delivery, got %v", groups)
	}
	timed = append(timed, groups[0]...)
	trials("units", 0)
	timed = append(timed, groups[1]...)
	trials("units", 1)
	timed = append(timed, groups[2]...)
	trials("reproofs", 0)
	trials("reproofs", 1)
	timed = append(timed, groups[3]...)
	ms := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)
	var last time.Time
	for i, x := range timed {
		s, _ := x["started_at"].(string)
		at, err := time.Parse(time.RFC3339, s)
		if _, ok := x["duration_ms"].(float64); !ok || err != nil || !ms.MatchString(s) {
			t.Fatalf("want a started_at in UTC with milliseconds and a duration_ms, got %v", x)
		}
		if i >= len(rep["nodes"].([]any)) { // the nodes are listed by name, the rest as they ran
			if at.Before(last) {
				t.Fatalf("want %v to start no earlier than %s", x, last)
			}
			last = at
		}
	}
	var proofRows []obj
	for _, ev := range events {
		if ev["check"] == "red/green" && ev["state"] == "passed" {
			proofRows = append(proofRows, ev)
		}
	}
	if len(proofRows) != 4 {
		t.Fatalf("want screen events for two unit proofs and two reproofs, got %v", proofRows)
	}
	for i, key := range []string{"units", "reproofs"} {
		for n := 0; n < 2; n++ {
			row := rep[key].([]any)[n].(obj)
			if row["duration_ms"] != proofRows[i*2+n]["duration_ms"] {
				t.Fatalf("%s %d report span differs from screen row: %v vs %v", key, n+1, row, proofRows[i*2+n])
			}
		}
	}
	var commandRows []obj
	for _, ev := range events {
		if ev["check"] != nil && ev["check"] != "red/green" && ev["state"] != "started" && ev["step"] != "clone" && ev["step"] != "delivery" {
			commandRows = append(commandRows, ev)
		}
	}
	var cloneInstall obj
	for _, ev := range events {
		if ev["step"] == "clone" && ev["check"] == "install" && ev["duration_ms"] != nil && ev["state"] != "started" {
			cloneInstall = ev
		}
	}
	commands := rep["commands"].([]any)
	rows := append([]obj{cloneInstall}, commandRows...)
	if len(rows)+1 != len(commands) {
		t.Fatalf("want screen rows for all non-delivery commands, got %d rows for %d commands", len(rows), len(commands))
	}
	for i, c := range commands[:len(commands)-1] {
		command, row := c.(obj), rows[i]
		if command["name"] != row["check"] || command["duration_ms"] != row["duration_ms"] {
			t.Fatalf("command report differs from its screen row: %v vs %v", command, row)
		}
	}
	var deliveryEvent obj
	for _, ev := range events {
		if ev["step"] == "delivery" && ev["state"] != "started" && ev["duration_ms"] != nil {
			deliveryEvent = ev
		}
	}
	if last := commands[len(commands)-1].(obj); last["name"] != "delivery" ||
		last["duration_ms"] != deliveryEvent["commands"].([]any)[0].(obj)["duration_ms"] {
		t.Fatalf("delivery report differs from its screen row: %v vs %v", last, deliveryEvent)
	}
}

func TestReportKeepsEveryUnitTheReproofsAndTheInstall(t *testing.T) {
	root, rep, _ := chainedCut(t, atmInstall("true"))
	units, _ := rep["units"].([]any)
	if len(units) != 2 {
		t.Fatalf("want both units in the report, got %v", rep["units"])
	}
	sha := gitT(t, root, "rev-parse", "main")
	unit2, _ := units[1].(obj)["base"].(string)
	if unit2 == "" || unit2 == sha {
		t.Fatalf("want unit 2 on unit 1's commit, got %v", units[1])
	}
	checkProof(t, "unit", units[0], 1, sha, "a_test.sh", "fix")
	checkProof(t, "unit", units[1], 2, unit2, "c1_test.sh", "feature")
	reproofs, _ := rep["reproofs"].([]any)
	if len(reproofs) != 2 {
		t.Fatalf("want both proofs the slop detector re-ran, got %v", rep["reproofs"])
	}
	checkProof(t, "re-run proof of unit", reproofs[0], 1, sha, "a_test.sh", "fix")
	checkProof(t, "re-run proof of unit", reproofs[1], 2, unit2, "c1_test.sh", "feature")
	var installs int
	for _, c := range rep["commands"].([]any) {
		if c := c.(obj); c["name"] == "install" && c["command"] == "true" && c["result"] == "passed" {
			installs++
		}
	}
	if installs != 4 {
		t.Fatalf("want the clone's install, then each unit's and the re-run checks', got %v", rep["commands"])
	}
}
