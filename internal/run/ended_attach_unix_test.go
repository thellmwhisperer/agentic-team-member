//go:build unix

package run

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const endedGateNM = `#!/bin/sh
echo "$PWD $*" >>"$FAKE_NM/log"
case "$*" in
"axi status --run archived-gate"|"axi status")
	if [ -f "$FAKE_NM/absent" ]; then printf 'run: null\n'; exit 0; fi
	cat "$FAKE_NM/status" ;;
"axi logs --step "*" --full --run archived-gate"|"axi logs --step "*" --full")
	printf 'log[2]{line}:\n  %s summary\n  %s details\n' "$4" "$4" ;;
*) exit 2 ;;
esac
`

// A real atm attach restores the whole ended run after its clone is removed, using only the saved report and
// no-mistakes' explicit run id. Its gate logs remain clickable and the same results also work without a terminal.
func TestRealAttachEndedRunWithoutClone(t *testing.T) {
	bin := builtATM(t)
	for _, mode := range []string{"completed", "missing run", "missing id", "missing output"} {
		t.Run(mode, func(t *testing.T) {
			top, reportPath, nm := endedAttachFixture(t, mode)
			a := &attached{t: t, chunks: make(chan string, 64), rows: 55}
			a.attach(bin, top, 120, 55, "ended-run")
			state := "passed"
			if mode != "completed" {
				state = "unknown"
			}
			s := a.drawn(func(s string) bool { return strings.Contains(s, "q close") && strings.Contains(s, state) })
			assertEndedAttachRows(t, s, state, true)
			if mode == "completed" {
				openEndedGateLogs(t, a)
			}
			a.key("q")
			a.exits()

			plain := exec.Command(bin, "attach", "ended-run")
			plain.Dir = top
			out, err := plain.CombinedOutput()
			if err != nil {
				t.Fatalf("plain attach: %v\n%s", err, out)
			}
			assertEndedAttachRows(t, string(out), state, false)
			if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); len(lines) != 18 ||
				lines[16] != "RESULT  passed" || lines[17] != "report  "+reportPath {
				t.Fatalf("plain attach must print sixteen steps, RESULT and report path:\n%s", out)
			}
			assertEndedGateCalls(t, nm, filepath.Dir(reportPath), mode)
			if _, err := os.Stat(socket(top)); !os.IsNotExist(err) {
				t.Fatalf("reading an ended run must not need a daemon: %v", err)
			}
			t.Logf("ended screen after clone removal:\n%s\nplain attach:\n%s", s, out)
		})
	}
}

func TestRealAttachEndedRunFitsNarrowScreen(t *testing.T) {
	bin := builtATM(t)
	for _, mode := range []string{"completed", "missing id"} {
		t.Run(mode, func(t *testing.T) {
			top, _, _ := endedAttachFixture(t, mode)
			a := &attached{t: t, chunks: make(chan string, 64), rows: 50}
			a.attach(bin, top, 60, 50, "ended-run")
			state := "passed"
			if mode != "completed" {
				state = "unknown"
			}
			s := a.drawn(func(s string) bool { return strings.Contains(s, "q close") })
			assertEndedAttachRows(t, s, state, true)
			a.key("q")
			a.exits()
		})
	}
}

func TestRealAttachEndedATMRunKeepsItsGateLive(t *testing.T) {
	bin := builtATM(t)
	top, reportPath, nm := endedAttachFixture(t, "completed")
	clone := t.TempDir()
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report obj
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	reportNode(t, report, "clone")["clone"] = clone
	data, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, reportPath, string(data))
	status, err := os.ReadFile(filepath.Join(nm, "status"))
	if err != nil {
		t.Fatal(err)
	}
	live := strings.Replace(string(status), "status: completed", "status: running", 1)
	live = strings.Replace(live, "review,completed", "review,running", 1)
	writeFixture(t, filepath.Join(nm, "status"), live)
	a := &attached{t: t, chunks: make(chan string, 64), rows: 55}
	a.attach(bin, top, 120, 55, "ended-run")
	a.drawn(func(s string) bool { return strings.Contains(s, "review summary") && strings.Contains(s, "running") })
	until(t, "the ongoing gate is polled in its clone", func() bool {
		log, _ := os.ReadFile(filepath.Join(nm, "log"))
		return strings.Contains(string(log), clone+" axi status\n") &&
			strings.Contains(string(log), clone+" axi logs --step review --full\n")
	})
	writeFixture(t, filepath.Join(nm, "status"), string(status))
	a.exits()
}

func endedAttachFixture(t *testing.T, mode string) (top, reportPath, nm string) {
	t.Helper()
	top, nm = atmRepo(t), t.TempDir()
	dir := filepath.Join(top, ".atm", "runs", "ended-run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	clone := t.TempDir()
	if err := os.RemoveAll(clone); err != nil {
		t.Fatal(err)
	}
	var nodes []obj
	for i, name := range []string{"issue", "clone", "contract", "agent", "checks", "ponytail", "delivery"} {
		nodes = append(nodes, obj{"name": name, "result": "passed", "duration_ms": i * 1100, "clone": clone})
	}
	data, err := json.Marshal(obj{"nodes": nodes})
	if err != nil {
		t.Fatal(err)
	}
	reportPath = filepath.Join(dir, "report.json")
	writeFixture(t, reportPath, string(data))
	data, err = json.Marshal(Outcome{Run: "ended-run", Outcome: "passed", Report: reportPath,
		Started: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Ended: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(top, ".atm", "runs.jsonl"),
		"{\"run\":\"other-run\",\"outcome\":\"failed\",\"ended\":\"2025-01-01T00:00:00Z\"}\n"+string(data)+"\n")
	if mode != "missing output" {
		output := "delivery finished without an id\n"
		if mode != "missing id" {
			output = "delivery output\nrun:\n  id: \"archived-gate\"\n  status: completed\n"
		}
		writeFixture(t, filepath.Join(dir, "delivery-output.txt"), output)
	}
	status := "run:\n  id: \"archived-gate\"\n  status: completed\n  steps[9]{step,status,findings,duration_ms}:\n"
	for i, name := range strings.Fields("intent rebase review test document lint push pr ci") {
		status += fmt.Sprintf("    %s,completed,0,%d\n", name, i*1200)
	}
	writeFixture(t, filepath.Join(nm, "status"), status)
	writeFixture(t, filepath.Join(nm, "no-mistakes"), endedGateNM, 0o755)
	if mode == "missing run" {
		writeFixture(t, filepath.Join(nm, "absent"), "")
	}
	t.Setenv("FAKE_NM", nm)
	t.Setenv("PATH", nm+string(os.PathListSeparator)+os.Getenv("PATH"))
	return top, reportPath, nm
}

func assertEndedAttachRows(t *testing.T, shown, gateState string, terminal bool) {
	t.Helper()
	for i, label := range []string{"Issue", "Clone", "Contract", "Agent", "Checks", "Slop detector", "Delivery"} {
		assertEndedAttachRow(t, shown, label, "passed", Human(time.Duration(i)*1100*time.Millisecond), terminal)
	}
	for i, name := range strings.Fields("intent rebase review test document lint push pr ci") {
		took := ""
		if gateState == "passed" {
			took = Human(time.Duration(i) * 1200 * time.Millisecond)
		}
		assertEndedAttachRow(t, shown, name, gateState, took, terminal)
	}
	if strings.Contains(shown, "pending") {
		t.Fatalf("an ended run must not invent pending steps:\n%s", shown)
	}
}

func assertEndedAttachRow(t *testing.T, shown, label, state, took string, terminal bool) {
	t.Helper()
	icon := map[string]string{"passed": "✓", "unknown": "?"}[state]
	pattern := regexp.QuoteMeta(icon+" "+label) + ` +` + regexp.QuoteMeta(took) + ` +` + state + ` │`
	if !terminal {
		if took == "" {
			took = "-"
		}
		pattern = `(?m)^` + regexp.QuoteMeta(strings.ToLower(label)+"  "+icon+"  "+took+"  "+state) + `$`
	}
	if !regexp.MustCompile(pattern).MatchString(shown) {
		t.Fatalf("ended attach lacks %s (%s, %s):\n%s", label, state, took, shown)
	}
}

func openEndedGateLogs(t *testing.T, a *attached) {
	t.Helper()
	for _, name := range strings.Fields("intent rebase review test document lint push pr ci") {
		s := a.drawn(func(s string) bool { return strings.Contains(s, "▸ ✓ "+name) })
		if strings.Contains(s, name+" details") {
			t.Fatalf("%s log starts unfolded:\n%s", name, s)
		}
		a.click(rowOf(s, "▸ ✓ "+name))
		s = a.drawn(func(s string) bool { return strings.Contains(s, name+" details") })
		a.click(rowOf(s, "▾ ✓ "+name))
		a.drawn(func(s string) bool { return !strings.Contains(s, name+" details") })
	}
}

func assertEndedGateCalls(t *testing.T, nm, dir, mode string) {
	t.Helper()
	log, err := os.ReadFile(filepath.Join(nm, "log"))
	if mode == "missing id" || mode == "missing output" {
		if !os.IsNotExist(err) {
			t.Fatalf("without an id attach must not guess another gate: %v\n%s", err, log)
		}
		return
	}
	want := dir + " axi status --run archived-gate\n"
	if mode == "completed" {
		for _, name := range strings.Fields("intent rebase review test document lint push pr ci") {
			want += dir + " axi logs --step " + name + " --full --run archived-gate\n"
		}
	}
	if err != nil || string(log) != want+want {
		t.Fatalf("terminal and plain attach must ask from the run directory by id: %v\n%s", err, log)
	}
}

func assertUnknownGatePipeline(t *testing.T, screen string) {
	t.Helper()
	delivery := regexp.MustCompile(`(?m)^│ \S+ Delivery\b`).FindStringIndex(screen)
	if delivery == nil {
		t.Fatalf("the screen lacks Delivery:\n%s", screen)
	}
	previous := delivery[0]
	for _, name := range strings.Fields("intent rebase review test document lint push pr ci") {
		rows := regexp.MustCompile(`(?m)^│ \? `+name+` +unknown │`).FindAllStringIndex(screen, -1)
		if len(rows) != 1 || rows[0][0] <= previous {
			t.Fatalf("the screen lacks one unknown %s row after the previous step:\n%s", name, screen)
		}
		previous = rows[0][0]
	}
}
