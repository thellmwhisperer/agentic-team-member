//go:build unix

package run

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// hangBuild is the atm build whose agents are hung after 2s of silence with no child process working.
const hangBuild = "hang-test"

// busy works, silent, for at least 3s: longer than hangBuild's 2s and the old check_timeout of 2s.
const busy = `end=$(($(date +%s) + 4)); while [ "$(date +%s)" -lt "$end" ]; do :; done; `

// TestHungAgentAsks: a worker with no output whose one child waits on a fifo nobody writes is hung. The real atm
// stops it and asks approve, fix or stop, as on a failed check, and goes on from each answer.
func TestHungAgentAsks(t *testing.T) {
	sharedATM(t, hangBuild) // before repo's HOME, as builtATM
	for _, c := range []struct {
		answer string
		code   int
	}{{"approve", 0}, {"fix", 0}, {"stop", 1}} {
		t.Run(c.answer, func(t *testing.T) {
			fifo := filepath.Join(t.TempDir(), "never")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			call := filepath.Join(t.TempDir(), "call")
			t.Setenv("FAKE_AGENT_CALL", call)
			t.Setenv("FAKE_AGENT_WORK", "if test -f first-turn; then "+fixWork+"; else touch first-turn; read x < "+fifo+"; fi")
			top := gitT(t, repo(t, "local", atmYAML), "rev-parse", "--show-toplevel")
			bin := versionServer(t, top, hangBuild)
			label := hungQuestion(t, bin, top)
			answerFailedCheck(t, bin, top, label, c.answer, c.code)
			rep := readReport(t, filepath.Join(runDir(top, label), "report.json"))
			decisions, _ := rep["decisions"].([]any)
			if len(decisions) != 1 || decisions[0].(obj)["answer"] != c.answer {
				t.Fatalf("want one %s decision, got %v", c.answer, rep)
			}
			if c.answer == "fix" && !strings.Contains(agentCall(t, call).Stdin, "hung: no output") {
				t.Fatal("the fix turn did not receive the hang")
			}
			if c.answer != "stop" && reportNode(t, rep, "checks")["result"] != "passed" {
				t.Fatalf("want Checks after %s, got %v", c.answer, rep)
			}
		})
	}
}

// hungQuestion is the label of the run in top that asks about its hung worker.
func hungQuestion(t *testing.T, bin, top string) string {
	t.Helper()
	f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-f.lines:
			if !ok {
				t.Fatalf("run ended without asking: %s", f.errors())
			}
			var ev obj
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatal(err)
			}
			if ev["step"] == "decision" && ev["state"] == "waiting" {
				if finding, _ := ev["finding"].(string); !strings.HasPrefix(finding, "hung: no output and no child "+
					"process working for 2s") || ev["choices"] != "approve, fix, stop" {
					t.Fatalf("want a question on the hang, got %v", ev)
				}
				f.exit(1)
				return ev["run"].(string)
			}
		case <-timer.C:
			t.Fatal("no question on the hung worker")
		}
	}
}

// TestWorkingAgentIsNotCut: a worker silent past the hang limit and the retired agent_timeout while its child
// works is never stopped.
func TestWorkingAgentIsNotCut(t *testing.T) {
	sharedATM(t, hangBuild)
	t.Setenv("FAKE_AGENT_WORK", busy+fixWork)
	top := gitT(t, repo(t, "local", atmYAML+"agent_timeout: 1s\n"), "rev-parse", "--show-toplevel")
	followedRun(t, versionServer(t, top, hangBuild), top, 0, issueFile(t, issue))
	lastRun(t, top, "passed")
}

// TestRunChecksHaveNoTimeCap: with the retired check_timeout: 2s, a checks command that outlives it completes,
// in the worker's Checks and again after a ponytail cut, and the run goes on to deliver.
func TestRunChecksHaveNoTimeCap(t *testing.T) {
	builtATM(t)
	delivered := filepath.Join(t.TempDir(), "delivered")
	t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest)
	t.Setenv("FAKE_PONYTAIL_WORK", "echo fixed > a.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	cfg := atmYAML + "e2e: '" + busy + "true'\n"
	for _, name := range []string{"install", "test", "typecheck", "lint"} {
		command := busy + "true"
		if name == "test" {
			command = busy + "sh a_test.sh"
		}
		cfg = atmSet(cfg, name, command)
	}
	cfg = atmSet(cfg, "delivery", "cat a.txt > "+delivered)
	top := gitT(t, repo(t, "local", cfg+"check_timeout: 2s\n"), "rev-parse", "--show-toplevel")
	followedRun(t, followUpsServer(t, top), top, 0, issueFile(t, issue))
	rep := readReport(t, filepath.Join(runDir(top, lastRun(t, top, "passed")), "report.json"))
	passed := map[string]int{}
	for _, raw := range rep["commands"].([]any) {
		command := raw.(obj)
		if command["result"] == "passed" {
			passed[command["name"].(string)]++
		}
	}
	for _, name := range []string{"install", "test", "e2e", "typecheck", "lint"} {
		if passed[name] < 2 {
			t.Fatalf("want %s to finish in both Checks passes: %v", name, rep["commands"])
		}
	}
	if reportNode(t, rep, "ponytail")["kept"] != true {
		t.Fatalf("want the cut kept after its Checks, got %v", rep)
	}
	if b, err := os.ReadFile(delivered); err != nil || string(b) != "fixed\n" {
		t.Fatalf("want the cut delivered, got %q, %v", b, err)
	}
}

// TestPartialOutputIsNotSilence: an agent writing to stdout or stderr without ending a line, past the hang limit
// and with no process under it, is not hung: each run delivers, and one background process serves both.
func TestPartialOutputIsNotSilence(t *testing.T) {
	sharedATM(t, hangBuild)
	partial, delivered := filepath.Join(t.TempDir(), "partial"), filepath.Join(t.TempDir(), "delivered")
	t.Setenv("FAKE_AGENT_PARTIAL", partial)
	cfg := atmSet(atmYAML, "delivery", "cat a.txt > "+delivered)
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	bin := versionServer(t, top, hangBuild)
	for _, stream := range []string{"stdout", "stderr"} {
		writeFixture(t, partial, stream)
		writeFixture(t, delivered, "")
		followedRun(t, bin, top, 0, issueFile(t, issue))
		lastRun(t, top, "passed")
		if b, err := os.ReadFile(delivered); err != nil || string(b) != "fixed\n" {
			t.Fatalf("%s: want the work delivered, got %q, %v", stream, b, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(top, ".atm", "serve.log")); strings.Count(string(b), "atm serve pid:") != 1 {
		t.Fatalf("want one background process for both runs, got %s", b)
	}
}

func TestLowCPUChildIsNotCut(t *testing.T) {
	sharedATM(t, hangBuild)
	modes := []string{"cpu", "io"}
	if runtime.GOOS == "linux" && os.Geteuid() != 0 {
		modes = append(modes, "private")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipt")
			work := fmt.Sprintf("%q --quiet-work %s %q", filepath.Join(fakes, "fakeagent"), mode, receipt)
			if mode == "private" {
				work += fmt.Sprintf(
					` & child=$!; while test ! -s %q; do kill -0 "$child" || exit 1; sleep 0.01; done; `,
					receipt+".pid",
				) +
					fmt.Sprintf(`cat /proc/$child/stat >/dev/null && ! LC_ALL=C cat /proc/$child/io >/dev/null 2>%q || exit 1; `+
						`grep -q 'Permission denied' %q || exit 1; wait "$child"`, receipt+".error", receipt+".error")
			}
			t.Setenv("FAKE_AGENT_WORK", work+" || exit 1; "+fixWork)
			top := gitT(t, repo(t, "local", atmYAML), "rev-parse", "--show-toplevel")
			events := followedRun(t, versionServer(t, top, hangBuild), top, 0, issueFile(t, issue))
			assertNoWorkingChildQuestion(t, events)
			lastRun(t, top, "passed")
			verifyLowCPUChildProof(t, mode, receipt)
		})
	}
}

func assertNoWorkingChildQuestion(t *testing.T, events []obj) {
	t.Helper()
	for _, ev := range events {
		if ev["step"] == "decision" {
			t.Fatalf("working child caused a question: %v", ev)
		}
	}
}

func verifyLowCPUChildProof(t *testing.T, mode, receipt string) {
	t.Helper()
	var proof struct {
		Elapsed int64 `json:"elapsed_ns"`
		Samples []struct {
			PID   int
			CPU   int64 `json:"cpu_ns"`
			Bytes int
		}
	}
	b, err := os.ReadFile(receipt)
	if err != nil || json.Unmarshal(b, &proof) != nil ||
		proof.Elapsed < int64(5*time.Second) || len(proof.Samples) != 25 {
		t.Fatalf("child did not complete five seconds of work: %s, %v", b, err)
	}
	first, last := proof.Samples[0], proof.Samples[24]
	if last.CPU <= first.CPU || last.CPU >= int64(time.Second) {
		t.Fatalf("want cumulative positive subsecond CPU: %s", b)
	}
	for i, sample := range proof.Samples {
		if sample.PID != first.PID || sample.PID <= 0 {
			t.Fatalf("child PID changed: %s", b)
		}
		if mode != "io" && i > 0 && sample.CPU <= proof.Samples[i-1].CPU {
			t.Fatalf("child did not use CPU at every sample: %s", b)
		}
		if mode == "io" && sample.Bytes != (i+1)*2*1024*1024 {
			t.Fatalf("child did not transfer bytes at every sample: %s", b)
		}
	}
}

func TestActivityObservationFailuresDoNotStopWork(t *testing.T) {
	sharedATM(t, hangBuild)
	ps, err := exec.LookPath("ps")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, script, stream, work string }{
		{"ps stdout", "exit 42", "stdout", ""},
		{"ps stderr", "exit 42", "stderr", ""},
		{"ps silent", "exit 42", "", "sleep 4; "},
		{"native", `if test -s "$FAKE_AGENT_PID" && test ! -f "$ATM_ACTIVITY_USED"; then
: > "$ATM_ACTIVITY_USED"
printf '1 '
cat "$FAKE_AGENT_PID"
printf '\n'
` + fmt.Sprintf("%q", ps) + ` "$@"
else
exec ` + fmt.Sprintf("%q", ps) + ` "$@"
fi`, "", `echo "$PPID" > "$FAKE_AGENT_PID"; ` + busy},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "native" && os.Geteuid() == 0 {
				t.Skip("root can read PID 1 activity")
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKE_AGENT_WORK", c.work+fixWork)
			if c.stream != "" {
				partial := filepath.Join(dir, "partial")
				writeFixture(t, partial, c.stream)
				t.Setenv("FAKE_AGENT_PARTIAL", partial)
			}
			t.Setenv("FAKE_AGENT_PID", filepath.Join(dir, "pid"))
			t.Setenv("ATM_ACTIVITY_USED", filepath.Join(dir, "used"))
			top := gitT(t, repo(t, "local", atmYAML), "rev-parse", "--show-toplevel")
			events := followedRun(t, versionServer(t, top, hangBuild), top, 0, issueFile(t, issue))
			for _, ev := range events {
				if ev["step"] == "decision" {
					t.Fatalf("missing observation caused a question: %v", ev)
				}
			}
			rep := readReport(t, filepath.Join(runDir(top, lastRun(t, top, "passed")), "report.json"))
			if reportNode(t, rep, "checks")["result"] != "passed" {
				t.Fatalf("observation failure stopped the work: %v", rep)
			}
			if c.name == "native" {
				if _, err := os.Stat(filepath.Join(dir, "used")); err != nil {
					t.Fatal("native observation failure was not exercised", err)
				}
			}
		})
	}
}
