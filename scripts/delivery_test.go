package scripts

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"gopkg.in/yaml.v3"
)

// fakeNoMistakes answers like no-mistakes v1.99.0: init passes; axi run with --intent, --launch-nonce and
// --validation-generation first claims the nonce: one already claimed replays the run it binds, a new one binds
// FAKE_NM_RUN, and with an empty FAKE_NM_RUN nothing is bound or replayed, as when the launch fails before it. A
// bound claim prints the launch_receipt document on stdout before driving, like emitLaunchReceipt. Then axi run
// prints progress on stderr, waits for the
// test's release on stdin, prints FAKE_NM_HELD and exits FAKE_NM_HELD_EXIT, which says nothing of the run's result.
// axi status, bare or --run with an unquoted ID, prints the next TOON document in FAKE_NM_STATES and exits 0
// whatever the run's state, or fails like a query error when that document is "!" or missing, or --run names
// another run than the one it shows; anything else exits 2.
const fakeNoMistakes = `#!/bin/sh
echo "$*" >>"$FAKE_NM_LOG"
case "$*" in
init) exit 0 ;;
"axi status" | "axi status --run "*)
	n=$(($(cat "$FAKE_NM_STATES/n") + 1))
	echo "$n" >"$FAKE_NM_STATES/n"
	doc=$FAKE_NM_STATES/$n
	if [ ! -f "$doc" ] || [ "$(cat "$doc")" = "!" ]; then echo 'error: "daemon unreachable"'; exit 1; fi
	if [ $# -eq 4 ] && grep -q '^run:' "$doc" && ! grep -qxF "  id: \"$4\"" "$doc"; then
		echo "error: \"run $4 not found\""; exit 1
	fi
	cat "$doc" ;;
"axi run "*"--intent "*)
	if [ "$3" = --launch-nonce ]; then
		bound=$FAKE_NM_RUNS/$4
		[ -f "$bound" ] || [ -z "$FAKE_NM_RUN" ] || echo "$FAKE_NM_RUN" >"$bound"
		if [ -n "$FAKE_NM_RUN" ] && [ -f "$bound" ]; then
			printf 'launch_receipt:\n  run_id: "%s"\n  disposition: claimed\n  launch_nonce: "%s"\n' \
				"$(cat "$bound")" "$4"
			printf '  validation_generation: "%s"\n  branch: atm/the-unit\n' "$6"
			printf '  head_sha: 433ecbac6608d232cdff1840b111dec3ecfc23ea\n'
			printf '  submitted_head_sha: 433ecbac6608d232cdff1840b111dec3ecfc23ea\n  intent_digest: sha256:1f2e\n'
		fi
	fi
	echo "step test running" >&2
	read -r _
	printf '%s\n' "$FAKE_NM_HELD"
	exit "$FAKE_NM_HELD_EXIT" ;;
*) exit 2 ;;
esac
`

// fakeSleep stands in for sleep so the delivery's pause between observations is logged instead of waited.
const fakeSleep = "#!/bin/sh\necho \"sleep $*\" >>\"$FAKE_NM_LOG\"\n"

// oldWait is the elapsed-wait diagnostic no-mistakes printed when ATM 522-161 failed delivery.
const oldWait = "error: wait elapsed after 8m0s; the run is still active\n" +
	"help: inspect with `no-mistakes axi status` and re-run `no-mistakes axi run` to reattach"

// newRun is the ID of the run 534-181 left, and oldRun and otherRun two more: axi status quotes all of them.
const (
	newRun   = "01M5CGTG389YNJEQYX75NEP7TE"
	oldRun   = "01M5C9W2Q7KX3RDY0ZB4N8HJTA"
	otherRun = "01M5CH0A4V6T8R1Q3P5N7M9KZB"
)

// noRun is axi status of no-mistakes 1.99 on a branch without runs: a successful query with no run object.
const noRun = "current_branch: atm/the-unit\nruns_on_current_branch: 0\nruns: 0 runs yet in this repository\n"

// toon is axi status of run id in status: its id quoted and, once the run ended, its outcome.
func toon(id, status string) string {
	doc := "run:\n  id: \"" + id + "\"\n  branch: atm/the-unit\n  status: " + status +
		"\n  head_sha: 433ecbac6608d232cdff1840b111dec3ecfc23ea\n  steps[1]{step,status,findings,duration_ms}:\n" +
		"    ci," + status + ",0,0\nbranch_sync:\n  pipeline:\n    run: \"" + id + "\"\n    status: " + status + "\n"
	switch status {
	case "completed":
		return doc + "outcome: passed\n"
	case "failed":
		return doc + "outcome: failed\n"
	case "cancelled":
		return doc + "outcome: cancelled\nerror: \"cancelled: aborted by user\"\n"
	}
	return doc
}

// decision is axi status of run id parked at an approval: the run still says running, the gate says why it waits.
// The delivery keeps following the run and shows the gate.
func decision(id string) string {
	return strings.Replace(toon(id, "running"), "ci,running", "review,awaiting_approval", 1) +
		"gate:\n  step: review\n  status: awaiting_approval\n"
}

func TestOwnDeliveryLineOutlivesTheGateWait(t *testing.T) {
	data, err := os.ReadFile("../.atm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var conf struct{ Delivery string }
	if err := yaml.Unmarshal(data, &conf); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for name, body := range map[string]string{"no-mistakes": fakeNoMistakes, "sleep": fakeSleep} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, c := range gateWaits {
		t.Run(c.name, func(t *testing.T) { deliver(t, conf.Delivery, replay{gate: c}) })
	}
	for _, c := range replays {
		t.Run(c.name, func(t *testing.T) { deliver(t, conf.Delivery, c) })
	}
}

// gate is what the fake no-mistakes answers through one delivery, and what the delivery must do with it: run is
// the run a new nonce binds, empty when the launch fails before it, and states are the axi status documents in
// order, the first one asked before the launch. A delivery that must not launch is refused. An earlier attempt of
// the same delivery, when its states are set, first ran against the same daemon, launching run and failing.
type gate struct {
	name, run, held string
	heldExit        int
	earlier, states []string
	queries         int
	pass, refused   bool
	shows           string
}

var gateWaits = []gate{
	{"hold expires while the gate runs, then passes", newRun, oldWait, 1, nil,
		[]string{noRun, toon(newRun, "running"), toon(newRun, "completed")}, 3, true, false, "outcome: passed"},
	{"new wording, gate pending, then fails", newRun, "error: hold over\nhelp: later", 1, nil,
		[]string{noRun, toon(newRun, "pending"), toon(newRun, "failed")}, 3, false, false, "outcome: failed"},
	{"passes inside the hold", newRun, "outcome: passed", 0, nil,
		[]string{noRun, toon(newRun, "completed")}, 2, true, false, "outcome: passed"},
	{"hold expires and the run completes before the first query", newRun, oldWait, 1, nil,
		[]string{noRun, toon(newRun, "completed")}, 2, true, false, "outcome: passed"},
	{"completed with an override passes and shows it", newRun, oldWait, 1, nil,
		[]string{noRun, strings.Replace(toon(newRun, "completed"), "outcome: passed", "outcome: passed-with-override", 1)},
		2, true, false, "outcome: passed-with-override"},
	{"completed with skips passes and shows it", newRun, oldWait, 1, nil,
		[]string{noRun, strings.Replace(toon(newRun, "completed"), "outcome: passed", "outcome: passed-with-skips", 1)},
		2, true, false, "outcome: passed-with-skips"},
	{"completed with an unknown outcome is not success", newRun, oldWait, 1, nil,
		[]string{noRun, strings.Replace(toon(newRun, "completed"), "outcome: passed", "outcome: passed-somehow", 1)},
		2, false, false, "outcome: passed-somehow"},
	{"completed without a successful outcome is not success", newRun, oldWait, 1, nil,
		[]string{noRun, strings.Replace(toon(newRun, "completed"), "outcome: passed\n", "", 1)}, 2,
		false, false, "status: completed"},
	{"completed with a failed outcome is not success", newRun, oldWait, 1, nil,
		[]string{noRun, strings.Replace(toon(newRun, "completed"), "outcome: passed", "outcome: failed", 1)}, 2,
		false, false, "outcome: failed"},
	{"terminal failure quoting the old wait", newRun, "outcome: failed\n" + oldWait, 1, nil,
		[]string{noRun, toon(newRun, "failed")}, 2, false, false, "outcome: failed"},
	{"state query fails", newRun, oldWait, 1, nil,
		[]string{noRun, toon(newRun, "running"), "!"}, 3, false, false, "unreachable"},
	{"state query unrecognized", newRun, oldWait, 1, nil,
		[]string{noRun, "no-mistakes axi status: unknown output\n", toon(newRun, "completed")}, 2, false, false,
		"unknown"},
	{"the gate runs across queries, then the same run passes", newRun, oldWait, 1, nil, []string{noRun,
		toon(newRun, "pending"), toon(newRun, "running"), toon(newRun, "running"), toon(newRun, "completed")}, 5,
		true, false, "outcome: passed"},
	{"the gate runs across queries, then the same run is cancelled", newRun, oldWait, 1, nil,
		[]string{noRun, toon(newRun, "running"), toon(newRun, "running"), toon(newRun, "cancelled")}, 4, false,
		false, "outcome: cancelled"},
	{"hold expires and the branch's last run completed", "", oldWait, 1, nil,
		[]string{toon(oldRun, "completed"), toon(oldRun, "completed")}, 1, false, false, "receipt"},
	{"launch returns zero but only the branch's last completed run shows", "", "outcome: passed", 0, nil,
		[]string{toon(oldRun, "completed"), toon(oldRun, "completed")}, 1, false, false, "receipt"},
	{"launch shows no run at all", "", "outcome: passed", 0, nil, []string{noRun, noRun}, 1, false, false,
		"receipt"},
	{"an earlier failed run, then the new one passes", newRun, oldWait, 1, nil,
		[]string{toon(oldRun, "failed"), toon(newRun, "running"), toon(newRun, "completed")}, 3, true, false,
		"outcome: passed"},
	{"the branch's active run is left to its operator, not reattached", newRun, oldWait, 1, nil,
		[]string{toon(newRun, "running"), toon(newRun, "running"), toon(newRun, "completed")}, 1, false, true,
		"cannot prove"},
	{"a pending decision returns zero", newRun, decision(newRun), 0, nil,
		[]string{noRun, decision(newRun), toon(newRun, "completed")}, 3, true, false, "gate:"},
	{"a decision while observing", newRun, oldWait, 1, nil,
		[]string{noRun, toon(newRun, "running"), decision(newRun), toon(newRun, "completed")}, 4, true, false,
		"gate:"},
	{"a document decision across queries, then the same run fails", newRun, oldWait, 1, nil,
		[]string{noRun, toon(newRun, "running"), strings.ReplaceAll(decision(newRun), "review", "document"),
			strings.ReplaceAll(decision(newRun), "review", "document"), toon(newRun, "failed")}, 5, false, false,
		"step: document"},
	{"the observed run changes", newRun, oldWait, 1, nil,
		[]string{noRun, toon(newRun, "running"), toon(otherRun, "completed")}, 3, false, false, "not found"},
	{"checks pass inside the hold, the run waits for merge, then completes", newRun, "outcome: checks-passed", 0,
		nil, []string{noRun, toon(newRun, "running"), toon(newRun, "running"), toon(newRun, "completed")}, 4,
		true, false, "outcome: passed"},
	{"launch fails before it starts while an old run is active, which then completes", "", "", 1, nil,
		[]string{toon(oldRun, "running"), toon(oldRun, "completed")}, 1, false, true, "cannot prove"},
	{"the branch's state is unknown before the launch", "", "", 1, nil,
		[]string{"no-mistakes axi status: unknown output\n", toon(oldRun, "completed")}, 1, false, true, "unknown"},
	{"the branch's state query fails before the launch", newRun, oldWait, 1, nil,
		[]string{"!", toon(newRun, "completed")}, 1, false, true, "unreachable"},
	{"an active decision of another run is left to its operator", newRun, oldWait, 1, nil,
		[]string{decision(oldRun), toon(newRun, "completed")}, 1, false, true, "gate:"},
	{"launch fails before it starts, and the branch's last run completed", "", "error: daemon unreachable", 1,
		nil, []string{noRun, toon(oldRun, "completed")}, 1, false, false, "receipt"},
	{"a receipt of another nonce binds nothing", "", "launch_receipt:\n  run_id: \"" + newRun +
		"\"\n  launch_nonce: \"another\"\n  validation_generation: \"another\"", 0, nil,
		[]string{noRun, toon(newRun, "completed")}, 1, false, false, "receipt"},
	{"a receipt with no run binds nothing", "", "launch_receipt:\n  disposition: claimed", 0, nil,
		[]string{noRun, toon(newRun, "completed")}, 1, false, false, "receipt"},
	{"recovery replays its attempt's active run, which passes", newRun, oldWait, 1,
		[]string{noRun, toon(newRun, "running"), "!"},
		[]string{toon(newRun, "running"), toon(newRun, "running"), toon(newRun, "completed")}, 3, true, false,
		"outcome: passed"},
	{"recovery replays its attempt's run, completed in between", newRun, oldWait, 1,
		[]string{noRun, toon(newRun, "running"), "!"},
		[]string{toon(newRun, "completed"), toon(newRun, "completed")}, 2, true, false, "outcome: passed"},
	{"recovery leaves another active run to its operator", newRun, oldWait, 1,
		[]string{noRun, toon(newRun, "running"), "!"},
		[]string{toon(otherRun, "running"), toon(otherRun, "completed")}, 1, false, true, "cannot prove"},
	{"recovery after a launch with no receipt leaves the active run alone", "", oldWait, 1,
		[]string{noRun},
		[]string{toon(otherRun, "running"), toon(otherRun, "completed")}, 1, false, true, "cannot prove"},
	{"recovery after a receipt of another nonce leaves the active run alone", "", "launch_receipt:\n  run_id: \"" +
		newRun + "\"\n  launch_nonce: \"another\"\n  validation_generation: \"another\"", 0, []string{noRun},
		[]string{toon(newRun, "running"), toon(newRun, "completed")}, 1, false, true, "cannot prove"},
	{"recovery after an incomplete receipt leaves the active run alone", "", "launch_receipt:\n  run_id: \"" +
		newRun + "\"", 0, []string{noRun},
		[]string{toon(newRun, "running"), toon(newRun, "completed")}, 1, false, true, "cannot prove"},
}

// replay is a gate whose earlier attempt is stopped during its launch once what it printed is saved, when
// interrupted, and whose daemon then binds that attempt's nonce to rebind, when set. When again is set, a recovery
// against those states runs between both, its launch failing before any receipt, or stopped there when stopAgain.
type replay struct {
	gate
	interrupted bool
	rebind      string
	again       []string
	stopAgain   bool
}

var replays = []replay{
	{gate{"an interrupted launch's saved receipt replays its active run, which passes", newRun, oldWait, 1,
		[]string{noRun}, []string{toon(newRun, "running"), toon(newRun, "running"), toon(newRun, "completed")}, 3,
		true, false, "outcome: passed"}, true, "", nil, false},
	{gate{"an interrupted launch's saved receipt leaves another active run to its operator", newRun, oldWait, 1,
		[]string{noRun}, []string{toon(otherRun, "running"), toon(otherRun, "completed")}, 1, false, true,
		"cannot prove"}, true, "", nil, false},
	{gate{"an interrupted launch with no receipt leaves the active run alone", "", oldWait, 1, []string{noRun},
		[]string{toon(otherRun, "running"), toon(otherRun, "completed")}, 1, false, true, "cannot prove"}, true, "", nil,
		false},
	{gate{"a replayed receipt naming another run binds nothing", newRun, oldWait, 1,
		[]string{noRun, toon(newRun, "running"), "!"}, []string{toon(newRun, "running"), toon(otherRun, "completed")},
		1, false, false, "no launch receipt"}, false, otherRun, nil, false},
	{gate{"an interrupted launch's replayed receipt naming another run binds nothing", newRun, oldWait, 1,
		[]string{noRun}, []string{toon(newRun, "running"), toon(otherRun, "completed")}, 1, false, false,
		"no launch receipt"}, true, otherRun, nil, false},
	{gate{"an interrupted launch's binding outlives a replay failing before its receipt, then its run passes", newRun,
		oldWait, 1, []string{noRun}, []string{toon(newRun, "running"), toon(newRun, "running"),
			toon(newRun, "completed")}, 3, true, false, "outcome: passed"}, true, "",
		[]string{toon(newRun, "running")}, false},
	{gate{"an interrupted launch's binding outlives a replay interrupted again, then its run passes", newRun,
		oldWait, 1, []string{noRun}, []string{toon(newRun, "running"), toon(newRun, "running"),
			toon(newRun, "completed")}, 3, true, false, "outcome: passed"}, true, "",
		[]string{toon(newRun, "running")}, true},
}

// deliver runs the delivery line against the fake gate c, after its earlier attempt when there is one, with the
// same run directory and daemon.
func deliver(t *testing.T, delivery string, c replay) {
	dir := t.TempDir()
	report, runs := filepath.Join(dir, "run", "report.json"), filepath.Join(dir, "runs")
	for _, d := range []string{filepath.Dir(report), runs} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var first string
	if c.earlier != nil {
		pass, _, calls := attempt(t, delivery, c.gate, c.earlier, false, c.interrupted, report, runs,
			filepath.Join(dir, "earlier"))
		if pass {
			t.Fatalf("earlier attempt passed:\n%s", calls)
		}
		first = launch(calls)
		rebind(t, runs, c.rebind)
	}
	if c.again != nil {
		again := c.gate
		again.run = ""
		pass, _, calls := attempt(t, delivery, again, c.again, false, c.stopAgain, report, runs,
			filepath.Join(dir, "again"))
		if pass || launch(calls) != first {
			t.Fatalf("a recovery failing before its receipt passed or did not replay its attempt:\n%s\n%s", first, calls)
		}
	}
	pass, out, calls := attempt(t, delivery, c.gate, c.states, c.refused, false, report, runs,
		filepath.Join(dir, "attempt"))
	observed(t, c.gate, pass, out, calls)
	if c.earlier != nil && !c.refused && launch(calls) != first {
		t.Fatalf("recovery does not replay its attempt's nonce and generation:\n%s\n%s", first, launch(calls))
	}
}

// launch is the axi run call in calls.
func launch(calls string) string {
	for line := range strings.Lines(calls) {
		if strings.HasPrefix(line, "axi run") {
			return line
		}
	}
	return ""
}

// rebind makes the daemon answer every claimed nonce with run, when set, as if it had bound another run to it.
func rebind(t *testing.T, runs, run string) {
	if run == "" {
		return
	}
	claims, err := os.ReadDir(runs)
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range claims {
		if err := os.WriteFile(filepath.Join(runs, claim.Name()), []byte(run+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// interrupt stops the delivery's process group during its launch, once the delivery's output beside report holds
// the complete receipt the launch printed, when the daemon claimed its nonce and receipt says it replies, or holds
// none, while it still holds the gate.
func interrupt(t *testing.T, cmd *exec.Cmd, report, runs string, receipt bool) {
	t.Helper()
	claims, err := os.ReadDir(runs)
	if err != nil {
		t.Fatal(err)
	}
	for saved := filepath.Join(filepath.Dir(report), "delivery.out"); ; {
		data, _ := os.ReadFile(saved)
		if bytes.Contains(data, []byte("intent_digest:")) == (receipt && len(claims) > 0) {
			break
		}
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
}

// attempt runs the delivery line once against the documents states, releasing the held gate once its progress
// shows unless the delivery must be refused before launching or is stopped there, and returns whether it passed,
// what it printed and its calls to no-mistakes.
func attempt(t *testing.T, delivery string, c gate, docs []string, refused, stopped bool, report, runs,
	dir string) (bool, string, string) {
	log, states := filepath.Join(dir, "nm.log"), filepath.Join(dir, "states")
	if err := os.MkdirAll(states, 0o700); err != nil {
		t.Fatal(err)
	}
	// n is the last document shown, and 1, 2... each document in order.
	files := map[string]string{"n": "0"}
	for i, doc := range docs {
		files[strconv.Itoa(i+1)] = doc
	}
	for name, doc := range files {
		if err := os.WriteFile(filepath.Join(states, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", delivery)
	cmd.Dir = ".."
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: stopped}
	cmd.Env = append(os.Environ(), "ATM_INTENT=the unit", "ATM_REPORT="+report, "FAKE_NM_LOG="+log,
		"FAKE_NM_STATES="+states, "FAKE_NM_RUNS="+runs, "FAKE_NM_RUN="+c.run, "FAKE_NM_HELD="+c.held,
		fmt.Sprint("FAKE_NM_HELD_EXIT=", c.heldExit))
	release, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stderr)
	if !refused && (!lines.Scan() || lines.Text() != "step test running") {
		t.Fatalf("no live progress before the gate exits: %q", lines.Text())
	}
	if stopped {
		interrupt(t, cmd, report, runs, c.run != "")
	}
	_ = release.Close()
	for lines.Scan() {
	}
	err = cmd.Wait()
	raw, _ := os.ReadFile(log)
	return err == nil, out.String(), string(raw)
}

// observed checks what one delivery against c did: its result, its calls to no-mistakes and what it printed.
func observed(t *testing.T, c gate, pass bool, out, calls string) {
	t.Helper()
	if pass != c.pass {
		t.Fatalf("delivery passed=%v, want %v\n%s\ncalls:\n%s", pass, c.pass, out, calls)
	}
	launched(t, c, calls)
	if n := strings.Count(calls, "axi status"); n != c.queries {
		t.Fatalf("delivery asked axi status %d times, want %d\ncalls:\n%s", n, c.queries, calls)
	}
	runs := strings.Count(calls, "axi status --run ")
	if runs != c.queries-1 || strings.Contains(calls, "--run \"") ||
		(runs > 0 && !strings.Contains(calls, "axi status --run "+c.run+"\n")) ||
		strings.Count(calls, "sleep ") != max(runs-1, 0) {
		t.Fatalf("delivery does not observe its receipt's run by its unquoted ID, pausing between queries:\n%s",
			calls)
	}
	if !strings.Contains(out, c.shows) {
		t.Fatalf("delivery does not show the gate's last state %q:\n%s", c.shows, out)
	}
	t.Logf("delivery output:\n%s\nno-mistakes calls:\n%s", out, calls)
}

// launched checks that a delivery against c inits, asks the branch's state, then starts one run with its nonce and
// generation, or none when it must be refused, and never reattaches.
func launched(t *testing.T, c gate, calls string) {
	t.Helper()
	if c.refused {
		if calls != "init\naxi status\n" {
			t.Fatalf("delivery does not leave the branch's run untouched:\n%s", calls)
		}
	} else if !strings.HasPrefix(calls, "init\naxi status\naxi run --launch-nonce ") ||
		!strings.Contains(launch(calls), " --validation-generation ") ||
		!strings.HasSuffix(launch(calls), " --intent the unit\n") {
		t.Fatalf("delivery does not init, ask the branch's state, then start the run with its receipt:\n%s", calls)
	}
	if again := strings.Contains(calls, "axi run\n"); again {
		t.Fatalf("reattached=%v, want false\ncalls:\n%s", again, calls)
	}
	if launches := strings.Count(calls, "axi run"); launches > 1 || (launches == 0) != c.refused {
		t.Fatalf("delivery started other runs:\n%s", calls)
	}
}
