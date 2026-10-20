//go:build unix

package run

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A real run whose agent runs process substitutions ends with its own outcome, and its background process stays up.
func TestRealRunProcessSubstitutionDoesNotCrashBackground(t *testing.T) {
	bin := builtATM(t)
	top := atmRepo(t)
	stopServing(t, top)
	t.Setenv("FAKE_AGENT_STREAM", bashEvent("claude", "cat <(printf x)", "")+bashEvent("claude", "printf x > >(cat)", ""))
	f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
	f.exit(0)
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) != 1 || runs[0].Outcome != "passed" {
		t.Fatalf("process substitution run outcome: %+v, %v", runs, err)
	}
	c, err := net.Dial("unix", short(socket(top)))
	if err != nil {
		t.Fatalf("the background process is gone after the run: %v", err)
	}
	_ = c.Close()
	t.Logf("atm run %s outcome=%s; atm serve socket answered after both process substitutions",
		runs[0].Run, runs[0].Outcome)
	// Let it end normally, at its next tick without a socket, so it flushes its coverage.
	log, err := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
	_, after, _ := strings.Cut(string(log), "atm serve pid:")
	pidText, _, _ := strings.Cut(after, "\n")
	pid, _ := strconv.Atoi(strings.TrimSpace(pidText))
	if err != nil || pid == 0 {
		t.Fatalf("no background pid in serve.log: %v\n%s", err, log)
	}
	if err := os.Remove(socket(top)); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); alive(pid); {
		if time.Now().After(deadline) {
			t.Fatal("the background process did not end after its socket was removed")
		}
	}
}

// Two real runs under one atm serve whose agents run shell commands at the same time each end with their own
// outcome, and the background process stays up.
func TestRealRunsCheckShellCommandsAtOnce(t *testing.T) {
	bin := builtATM(t)
	top := atmRepo(t)
	stopServing(t, top)
	wait, release := holdFakeAgents(t, 2)
	t.Setenv("FAKE_AGENT_WORK", fixWork)
	command := `cat > "$PWD/out.txt" && tee a "$TMPDIR/b"`
	t.Setenv("FAKE_AGENT_STREAM", strings.Repeat(bashEvent("claude", command, ""), 200))
	fix := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, "# Retry\nType: fix\nRetry once.\n"))
	docs := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, "# Retry\nType: docs\nRetry once.\n"))
	wait()
	release()
	fix.exit(0)
	docs.exit(0)
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) != 2 || runs[0].Outcome != "passed" || runs[1].Outcome != "passed" {
		t.Fatalf("concurrent shell commands outcomes: %+v, %v", runs, err)
	}
	c, err := net.Dial("unix", short(socket(top)))
	if err != nil {
		t.Fatalf("the background process is gone after both runs: %v", err)
	}
	_ = c.Close()
	t.Logf("atm run %s outcome=%s and atm run %s outcome=%s in one atm serve; its socket answered",
		runs[0].Run, runs[0].Outcome, runs[1].Run, runs[1].Outcome)
}

// Writes outside the clone are rejected with and without process substitution.
func TestRealRunProcessSubstitutionKeepsCloneBoundary(t *testing.T) {
	bin := builtATM(t)
	for _, nested := range []bool{false, true} {
		name := "direct write"
		if nested {
			name = "write inside process substitution"
		}
		t.Run(name, func(t *testing.T) {
			top := atmRepo(t)
			stopServing(t, top)
			command := "printf x > " + filepath.Join(top, "outside.txt")
			if nested {
				command = "printf x > >(cat > " + filepath.Join(top, "outside.txt") + ")"
			}
			t.Setenv("FAKE_AGENT_STREAM", bashEvent("claude", command, ""))
			f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
			f.exit(1)
			runs, err := RecordedRuns(top)
			if err != nil || len(runs) != 1 || runs[0].Outcome != "failed" || runs[0].FailedNode != "agent" ||
				!strings.Contains(runs[0].Reason, "the agent reached outside its clone") {
				t.Fatalf("clone boundary outcome: %+v, %v", runs, err)
			}
			c, err := net.Dial("unix", short(socket(top)))
			if err != nil {
				t.Fatalf("the background process is gone after rejecting the write: %v", err)
			}
			_ = c.Close()
			t.Logf("atm run %s outcome=%s: %s; atm serve socket answered", runs[0].Run, runs[0].Outcome, runs[0].Reason)
		})
	}
}
