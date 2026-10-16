//go:build unix

package run

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type skippedCICLICase struct {
	name, status, ci, gatePR, github, terminal string
	hold                                       bool
}

// Exercise the built CLI and its worker, with disposable external gate/GitHub responses.
// The skipped-CI contract is the oracle: only a terminal PR ends the run; gate
// pr_state remains authoritative, and neither the clone nor its commit is lost.
func TestSkippedCICLI(t *testing.T) {
	bin := builtATM(t)
	for _, c := range []skippedCICLICase{
		{"merged", "completed", "skipped", "", "MERGED", "MERGED", false},
		{"closed", "completed", "skipped", "", "CLOSED", "CLOSED", false},
		{"open_then_merged", "completed", "skipped", "", "OPEN", "MERGED", true},
		{"open_then_closed", "completed", "skipped", "", "OPEN", "CLOSED", true},
		{"github_error_then_merged", "completed", "skipped", "", "ERROR", "MERGED", true},
		{"gate_open_over_github_merged", "completed", "skipped", "open", "MERGED", "MERGED", true},
		{"gate_merged_over_github_closed", "completed", "skipped", "merged", "CLOSED", "MERGED", false},
		{"gate_closed_over_github_merged", "completed", "skipped", "closed", "MERGED", "CLOSED", false},
		{"running_gate", "running", "skipped", "", "MERGED", "MERGED", true},
		{"running_ci", "completed", "running", "", "MERGED", "MERGED", true},
		{"completed_ci", "completed", "completed", "", "MERGED", "MERGED", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			runSkippedCICLICase(t, bin, c)
		})
	}
}

func assertSkippedCICLIHeld(
	t *testing.T, c skippedCICLICase, current Outcome, nm, gh, calls, status string,
) {
	t.Helper()
	if current.Outcome != "running" || !current.Ended.IsZero() {
		t.Fatal("run ended prematurely")
	}
	if c.gatePR == "" && c.status == "completed" && c.ci == "skipped" {
		b, _ := os.ReadFile(calls)
		if strings.Count(string(b), "\n") < 2 {
			t.Fatal("PR was not polled again")
		}
		writeFixture(t, gh, c.terminal+"\n")
	} else {
		if b, _ := os.ReadFile(calls); len(b) != 0 {
			t.Fatalf("bypassed gate: %s", b)
		}
		writeFixture(t, nm, strings.TrimSuffix(status, "pr_state: "+c.gatePR+"\n")+"pr_state: merged\n")
	}
}

func finishSkippedCICLI(
	t *testing.T, c skippedCICLICase, read func() Outcome, bin, top, clone, head string,
) {
	t.Helper()
	var current Outcome
	deadline := time.Now().Add(30 * time.Second)
	for {
		current = read()
		if !current.Ended.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not end after terminal PR")
		}
		time.Sleep(50 * time.Millisecond)
	}
	want := "passed"
	if c.terminal == "CLOSED" {
		want = "failed"
	}
	if current.Outcome != want || c.terminal == "CLOSED" && current.Reason != "PR closed" {
		t.Fatalf("terminal run: %+v", current)
	}
	r := readReport(t, current.Report)
	if reportNode(t, r, "delivery")["result"] != "passed" {
		t.Fatal("delivery did not pass")
	}
	if c.ci == "skipped" && reportNode(t, r, "ci")["result"] != "skipped" {
		t.Fatal("CI skip was lost")
	}
	if got := gitT(t, clone, "rev-parse", "HEAD"); got != head || r["head_sha"] != head {
		t.Fatalf("clone commit changed: %s -> %s, report %v", head, got, r["head_sha"])
	}
	cmd := exec.Command(bin, "status")
	cmd.Dir = top
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("atm status: %v: %s", err, out)
	}
	b, _ := json.Marshal(current)
	t.Logf("atm status:\n%s\nterminal run: %s\nclone retained at commit %s", out, b, head)
	b, _ = os.ReadFile(current.Report)
	t.Logf("persisted report.json:\n%s", b)
}

func runSkippedCICLICase(t *testing.T, bin string, c skippedCICLICase) {
	t.Helper()
	top := repo(t, "https://github.com/o/r.git", atmSet(atmYAML, "delivery", ": no-mistakes; touch \"$ATM_LIVE_READY\""))
	top = gitT(t, top, "rev-parse", "--show-toplevel")
	stopServing(t, top)
	dir := t.TempDir()
	nm, gh := filepath.Join(dir, "status"), filepath.Join(dir, "state")
	calls := filepath.Join(dir, "gh-calls")
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	t.Setenv("ATM_LIVE_READY", ready)
	writeFixture(t, filepath.Join(dir, "no-mistakes"), "#!/bin/sh\n[ \"$*\" = 'axi status' ] || exit 99\n"+
		"while [ ! -f '"+release+"' ]; do sleep 0.05; done\ncat '"+nm+"'\n", 0o755)
	writeFixture(t, filepath.Join(dir, "gh"), "#!/bin/sh\necho \"$*\" >> '"+calls+"'\n"+
		"[ \"$(cat '"+gh+"')\" != ERROR ] || exit 1\ncat '"+gh+"'\n", 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	status := "run:\n  status: " + c.status +
		"\n  steps[1]{step,status,findings,duration_ms}:\n    ci," + c.ci + ",0,0\n"
	if c.gatePR != "" {
		status += "pr_state: " + c.gatePR + "\n"
	}
	writeFixture(t, nm, status)
	writeFixture(t, gh, c.github+"\n")
	watch := startFollow(t, bin, top, false, "watch")
	var id string
	select {
	case line := <-watch.lines:
		id = strings.TrimPrefix(line, "watching as ")
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not start")
	}
	cmd := exec.Command(bin, "run", issueFile(t, issue), "--watcher", id)
	cmd.Dir = top
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("atm run: %v: %s", err, out)
	}
	label := strings.TrimSpace(string(out))
	until(t, "delivery command", func() bool { _, err := os.Stat(ready); return err == nil })
	clone := theClone(t, top)
	head := gitT(t, clone, "rev-parse", "HEAD")
	// Keep a reader's lease so normal post-run cleanup cannot erase the evidence.
	// The run must finish while the clone is still present.
	lease, err := os.OpenFile(clone+".lock", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	if err := lockCloneShared(lease); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, release, "")
	checkSkippedCICLI(t, c, bin, top, clone, head, label, nm, gh, calls, status)
}

func checkSkippedCICLI(
	t *testing.T, c skippedCICLICase, bin, top, clone, head, label, nm, gh, calls, status string,
) {
	t.Helper()
	read := func() Outcome {
		runs, err := Runs(top)
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs: %v, %v", runs, err)
		}
		return runs[0]
	}
	until(t, "gate CI node", func() bool {
		b, _ := os.ReadFile(filepath.Join(top, ".atm", "runs", label, "report.json"))
		var r verdict
		if json.Unmarshal(b, &r) != nil {
			return false
		}
		for _, n := range r.Nodes {
			if n.Name == "ci" {
				return true
			}
		}
		return false
	})
	if c.hold {
		// More than one production poll, with no shortened test clock.
		time.Sleep(11 * time.Second)
		current := read()
		b, _ := json.Marshal(current)
		t.Logf("after 11 seconds: %s", b)
		assertSkippedCICLIHeld(t, c, current, nm, gh, calls, status)
	}
	finishSkippedCICLI(t, c, read, bin, top, clone, head)
}
