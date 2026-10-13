//go:build unix

package run

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestKilledBackgroundKeepsCloneForStartedAgent(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "agent-pid")
	ready := filepath.Join(t.TempDir(), "agent-started")
	t.Setenv("FAKE_AGENT", "grandchild")
	t.Setenv("FAKE_AGENT_PID", pidPath)
	t.Setenv("FAKE_AGENT_READY", ready)
	t.Setenv("FAKE_AGENT_KILL_PARENT", "1")
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmYAML), "rev-parse", "--show-toplevel")
	t.Cleanup(func() { _ = os.Remove(socket(top)) })
	done := make(chan error, 1)
	go func() {
		_, err := Start(top, []string{issueFile(t, issue)})
		done <- err
	}()
	until(t, "agent start", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
	clone := theClone(t, top)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the background request did not end after its process was killed")
	}
	waitForDeadServer(t, top)
	proc := recordedProcess(t, pidPath)
	t.Setenv("FAKE_AGENT", "fail")
	t.Setenv("FAKE_AGENT_KILL_PARENT", "")
	assertCloneKept(t, top, clone, "the sweep removed the clone while its agent was alive")
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	assertCloneSwept(t, top, clone, "the clone lease to be released", "the sweep kept the clone after the agent exited")
}

func TestAgentExitKeepsCloneUntilOrphanChildExits(t *testing.T) {
	pidFifo, _ := fifos(t)
	t.Setenv("FAKE_AGENT", "orphan")
	t.Setenv("FAKE_AGENT_PID", pidFifo)
	top := backgroundRepo(t)
	o := startBackgroundRun(t, top)
	if end, err := Attach(top, o.Run, &bytes.Buffer{}); err != nil || end.Outcome != "passed" {
		t.Fatalf("Attach = %+v, %v", end, err)
	}
	clone := theClone(t, top)
	proc := recordedProcess(t, pidFifo)
	t.Setenv("FAKE_AGENT", "fail")
	assertCloneKept(t, top, clone, "the sweep removed a clone whose agent child was alive")
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	assertCloneSwept(t, top, clone, "the child lease to be released", "the sweep kept the clone after its child exited")
}

func TestKilledBackgroundDuringDeliveryKeepsCloneAndFailsAtDelivery(t *testing.T) {
	pidFifo, _ := fifos(t)
	t.Setenv("FAKE_AGENT", "")
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atmSet(atmYAML, "delivery",
		`echo $$ > "$ATM_TEST_PID"; cat "$ATM_TEST_GATE"`)), "rev-parse", "--show-toplevel")
	t.Cleanup(func() { _ = os.Remove(socket(top)) })
	o := startBackgroundRun(t, top)
	delivery := recordedPID(t, pidFifo)
	t.Cleanup(func() { _ = syscall.Kill(-delivery, syscall.SIGKILL) })
	killDeliveryServer(t, top)
	clone := theClone(t, top)
	t.Setenv("FAKE_AGENT", "fail")
	assertInterruptedDeliveryRun(t, top, o.Run)
	assertCloneKept(t, top, clone, "the sweep removed the clone while delivery was alive")
	if _, err := os.Stat(clone + ".delivered"); !os.IsNotExist(err) {
		t.Fatalf("the killed delivery marked the clone delivered: %v", err)
	}
	if err := syscall.Kill(-delivery, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		t.Fatal(err)
	}
	assertCloneSwept(t, top, clone, "the delivery lease to be released", "the sweep kept the clone after delivery exited")
}

func recordedPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func startBackgroundRun(t *testing.T, top string) Outcome {
	t.Helper()
	o, err := Start(top, []string{issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func recordedProcess(t *testing.T, path string) *os.Process {
	t.Helper()
	proc, err := os.FindProcess(recordedPID(t, path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Kill() })
	return proc
}

func assertCloneKept(t *testing.T, top, clone, message string) {
	t.Helper()
	nextRun(t, top)
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("%s: %v", message, err)
	}
}

func assertCloneSwept(t *testing.T, top, clone, lease, message string) {
	t.Helper()
	until(t, lease, func() bool {
		f, locked, err := tryCloneExclusive(clone)
		if f != nil {
			_ = f.Close()
		}
		return err == nil && locked
	})
	nextRun(t, top)
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("%s: %v", message, clones(t, top))
	}
}

func killDeliveryServer(t *testing.T, top string) {
	t.Helper()
	log, err := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	var server int
	for _, line := range lines {
		pid, ok := strings.CutPrefix(line, "atm serve pid:")
		if !ok {
			continue
		}
		server, err = strconv.Atoi(strings.TrimSpace(pid))
	}
	if err != nil || server <= 0 {
		t.Fatalf("serve log %q: %v", log, err)
	}
	if err := syscall.Kill(server, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitForDeadServer(t, top)
}

func waitForDeadServer(t *testing.T, top string) {
	t.Helper()
	until(t, "the killed background server to stop answering", func() bool {
		c, err := net.Dial("unix", short(socket(top)))
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	})
}

func assertInterruptedDeliveryRun(t *testing.T, top, runID string) {
	t.Helper()
	runs, err := Runs(top)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, run := range runs {
		if run.Run == runID {
			if run.Outcome != "failed" || run.FailedNode != "delivery" || run.Step != "delivery" ||
				!run.Ended.After(run.Started) {
				t.Fatalf("the interrupted run's history = %+v; want failure at delivery with a duration", run)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("the interrupted run %q is missing from history: %+v", runID, runs)
	}
}

// theClone is the one clone under root.
func theClone(t *testing.T, root string) string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(root, ".atm", "clones", "atm-run-*[0-9]"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("clones = %v, %v; want one", dirs, err)
	}
	return dirs[0]
}

// nextRun runs a run in the background, through to its end at its agent.
func nextRun(t *testing.T, root string) {
	t.Helper()
	o, err := Start(root, []string{issueFile(t, issue)})
	if err == nil {
		o, err = Attach(root, o.Run, &bytes.Buffer{})
	}
	if err != nil || o.FailedNode != "agent" {
		t.Fatalf("next run = %+v, %v", o, err)
	}
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// until fails t unless cond holds within 5 s: a killed process is not yet reaped, its new parent reaps it soon
// after.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("not within 5 s: %s", what)
		}
	}
}

// An idle background process whose file atm update replaced ends at the next atm run, which one started from
// the new file serves.
func TestARunGoesToABackgroundProcessOfTheNewFile(t *testing.T) {
	atm := filepath.Join(t.TempDir(), "atm")
	buildATM(t, atm, "old-build")
	buildATM(t, atm+".new", "new-build")
	top := atmRepo(t)
	wait, release := holdFakeAgents(t, 1)
	old := exec.Command(atm, "serve")
	old.Dir = top
	stderr, err := old.StderrPipe()
	if err == nil {
		err = old.Start()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Process.Kill() })
	awaitOldServer(t, stderr)
	exited := make(chan error, 1)
	go func() { exited <- old.Wait() }()
	if err := os.Rename(atm+".new", atm); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(atm, "run", issueFile(t, issue))
	cmd.Dir = top
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("atm run did not return")
	}
	wait()
	log, err := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
	if err != nil || !strings.Contains(string(log), "atm serve version: new-build") {
		t.Fatalf("replacement server identity = %q, %v", log, err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the background process of the old file still serves")
	}
	release()
	if runs, err := Runs(top); err != nil || len(runs) != 1 || runs[0].Outcome != "running" {
		t.Fatalf("new server run = %+v, %v", runs, err)
	}
}

func awaitOldServer(t *testing.T, stderr io.Reader) {
	t.Helper()
	ready := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(stderr)
		lines := make([]string, 2)
		for i := range lines {
			line, err := reader.ReadString('\n')
			if err != nil {
				ready <- err
				return
			}
			lines[i] = line
		}
		if !strings.HasPrefix(lines[0], "atm serve pid:") || !strings.Contains(lines[1], "old-build") {
			ready <- fmt.Errorf("atm serve identity = %q", lines)
			return
		}
		ready <- nil
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
}

func buildATM(t *testing.T, path, version string) {
	t.Helper()
	ldflags := fmt.Sprintf("-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Version=%s", version)
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", path, "./cmd/atm")
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path is unavailable")
	}
	cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build atm %s: %v\n%s", version, err, out)
	}
}
