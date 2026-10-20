//go:build unix

package run

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"gopkg.in/yaml.v3"
)

// ownDeliveryNM answers like no-mistakes for ATM's own delivery line: init passes, the branch has no run, axi run
// prints the receipt binding FAKE_NM_RUN to its nonce and generation and returns, as at a gate, and axi status --run
// FAKE_NM_RUN prints the next document in $FAKE_NM. Anything else, another run among them, exits 2.
const ownDeliveryNM = `#!/bin/sh
echo "$*" >>"$FAKE_NM/log"
case "$*" in
init) ;;
"axi status") printf 'current_branch: atm/the-unit\nruns_on_current_branch: 0\n' ;;
"axi run --launch-nonce "*" --validation-generation "*" --intent "*)
	printf 'launch_receipt:\n  run_id: "%s"\n  disposition: claimed\n  launch_nonce: "%s"\n' "$FAKE_NM_RUN" "$4"
	printf '  validation_generation: "%s"\n' "$6" ;;
"axi status --run $FAKE_NM_RUN")
	n=$(($(cat "$FAKE_NM/n") + 1))
	echo "$n" >"$FAKE_NM/n"
	cat "$FAKE_NM/$n" ;;
*) exit 2 ;;
esac
`

// ownDeliverySleep stands in for sleep: it returns at once, except while the last document shown awaits a decision,
// when it waits for the external operator's answer on $FAKE_NM/answer.
const ownDeliverySleep = `#!/bin/sh
echo "sleep $*" >>"$FAKE_NM/log"
if grep -q '^gate:' "$FAKE_NM/$(cat "$FAKE_NM/n")"; then read -r _ <"$FAKE_NM/answer"; fi
`

// ownRun is the run the fake no-mistakes binds.
const ownRun = "01M5D0SWRMZ44NM79H2NNC3YZ8"

// ownStatus is axi status of ownRun in status, awaiting a decision at gate when set, with outcome once it ended.
func ownStatus(status, gate, outcome string) string {
	doc := "run:\n  id: \"" + ownRun + "\"\n  branch: atm/the-unit\n  status: " + status + "\n"
	if gate != "" {
		doc += "gate:\n  step: " + gate + "\n  status: awaiting_approval\n"
	}
	if outcome != "" {
		doc += "outcome: " + outcome + "\n"
	}
	return doc
}

// A real atm run with ATM's own delivery line keeps its delivery on the same receipt-bound no-mistakes run while a
// gate awaits its operator's answer, and ends only on that run's terminal result.
func TestOwnDeliveryLineWaitsForApprovalThroughRealBinary(t *testing.T) {
	bin := builtATM(t)
	// go test runs in the package directory: ATM's own .atm.yaml is two levels up.
	b, err := os.ReadFile("../../.atm.yaml")
	var own, atm map[string]any
	if err == nil {
		err = yaml.Unmarshal(b, &own)
	}
	if err == nil {
		err = yaml.Unmarshal([]byte(atmYAML), &atm)
	}
	delivery, _ := own["delivery"].(string)
	if err != nil || !strings.Contains(delivery, "axi status --run") {
		t.Fatalf("want ATM's .atm.yaml with its delivery line, got %q, %v", delivery, err)
	}
	atm["delivery"] = delivery
	conf, err := yaml.Marshal(atm)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, gate, outcome string
		states              []string
	}{
		{"a review decision, answered, then the run passes", "review", "passed", []string{
			ownStatus("running", "", ""), ownStatus("running", "review", ""), ownStatus("running", "", ""),
			ownStatus("completed", "", "passed")}},
		{"a document decision, answered, then the run fails", "document", "failed", []string{
			ownStatus("running", "", ""), ownStatus("running", "document", ""), ownStatus("failed", "", "failed")}},
	} {
		t.Run(c.name, func(t *testing.T) { ownDeliveryWaits(t, bin, string(conf), c.gate, c.outcome, c.states) })
	}
}

// ownDeliveryWaits runs one issue through bin with the delivery of conf against the fake no-mistakes showing states,
// answers its gate once ATM is seen waiting on it, and checks the run ends with outcome, its delivery having followed
// the one run it launched.
func ownDeliveryWaits(t *testing.T, bin, conf, gate, outcome string, states []string) {
	top := gitT(t, repo(t, t.TempDir(), conf), "rev-parse", "--show-toplevel")
	t.Setenv("FAKE_AGENT_CALL", filepath.Join(t.TempDir(), "call.json"))
	nm := ownDeliveryFakes(t, top, states)
	server := startVersionServe(t, bin, top)
	out, err := versionLaunch(bin, top, issueFile(t, issue))
	label := strings.TrimSpace(string(out))
	if err != nil || label == "" {
		t.Fatalf("atm run: %v\n%s", err, out)
	}
	runDir := filepath.Join(top, ".atm", "runs", label)
	ended := make(chan Outcome, 1)
	go func() { end, _ := Attach(top, label, io.Discard); ended <- end }()
	reply := ownDeliveryAtGate(t, nm, runDir, gate, ended)
	if _, err := reply.WriteString("answered\n"); err != nil {
		t.Fatal(err)
	}
	_ = reply.Close()
	end := <-ended
	log, _ := os.ReadFile(filepath.Join(nm, "log"))
	if end.Outcome != outcome || (outcome == "failed") != (end.FailedNode == "delivery") {
		t.Fatalf("run ended %+v, want its no-mistakes run's %s\nno-mistakes calls:\n%s", end, outcome, log)
	}
	shown, _ := os.ReadFile(filepath.Join(runDir, "delivery-output.txt"))
	if !strings.Contains(string(shown), "outcome: "+outcome) {
		t.Fatalf("delivery does not show its run's outcome %s:\n%s", outcome, shown)
	}
	calls := string(log)
	if strings.Count(calls, "axi run") != 1 || strings.Count(calls, "axi status --run "+ownRun+"\n") != len(states) ||
		strings.Count(calls, "axi status") != len(states)+1 {
		t.Fatalf("delivery does not follow the one run it launched to its end:\n%s", calls)
	}
	t.Logf("no-mistakes calls:\n%s\ndelivery output:\n%s", calls, shown)
	endDeliveryServer(t, server, top)
}

// ownDeliveryFakes puts the fake no-mistakes and sleep first on PATH, with states as the documents axi status --run
// shows in order, makes top's .atm for its serve, and returns their directory, where the operator's answer is a FIFO.
func ownDeliveryFakes(t *testing.T, top string, states []string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools, nm := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{"no-mistakes": ownDeliveryNM, "sleep": ownDeliverySleep} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"n": "0"}
	for i, doc := range states {
		files[strconv.Itoa(i+1)] = doc
	}
	for name, doc := range files {
		if err := os.WriteFile(filepath.Join(nm, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(nm, "answer"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_NM", nm)
	t.Setenv("FAKE_NM_RUN", ownRun)
	return nm
}

// ownDeliveryAtGate waits until the delivery of the run in runDir waits on the operator's answer in nm, its gate
// pending, failing t when the run ended first, checks the run is still delivering and shows that gate, and returns
// the answer to write.
func ownDeliveryAtGate(t *testing.T, nm, runDir, gate string, ended <-chan Outcome) *os.File {
	t.Helper()
	// Opening the answer for writing returns once the delivery waits on it, the gate still pending.
	waiting := make(chan *os.File, 1)
	go func() {
		if f, err := os.OpenFile(filepath.Join(nm, "answer"), os.O_WRONLY, 0); err == nil {
			waiting <- f
		}
	}()
	var reply *os.File
	select {
	case end := <-ended:
		log, _ := os.ReadFile(filepath.Join(nm, "log"))
		shown, _ := os.ReadFile(filepath.Join(runDir, "delivery-output.txt"))
		t.Fatalf("ATM ended delivery at a pending %s gate: %+v\nno-mistakes calls:\n%s\ndelivery output:\n%s", gate,
			end, log, shown)
	case reply = <-waiting:
	}
	report := readReport(t, filepath.Join(runDir, "report.json"))
	if node := reportNode(t, report, "delivery"); node["result"] != "running" {
		t.Fatalf("delivery is %v while its %s gate awaits an answer, want running", node["result"], gate)
	}
	until(t, "the pending gate shown to its operator", func() bool {
		shown, _ := os.ReadFile(filepath.Join(runDir, "delivery-output.txt"))
		return strings.Contains(string(shown), "gate:\n  step: "+gate+"\n  status: awaiting_approval")
	})
	select {
	case end := <-ended:
		t.Fatalf("ATM ended while its %s gate awaits an answer: %+v", gate, end)
	default:
	}
	return reply
}
