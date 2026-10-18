//go:build unix

package run

import (
	"bytes"
	"encoding/json"
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
	lockFile, err := os.OpenFile(filepath.Join(top, ".atm", "atm.lock"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	old := exec.Command(atm, "serve")
	old.Dir = top
	log, err := os.OpenFile(filepath.Join(top, ".atm", "serve.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	old.Stdout, old.Stderr = log, log
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	_ = log.Close()
	var runStarted bool
	t.Cleanup(func() {
		cleanupReplacementServers(t, atm, top, old, runStarted, release)
	})
	until(t, "old server ready", func() bool {
		b, e := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
		return e == nil && strings.Contains(string(b), "atm serve version: old-build")
	})
	_ = lockFile.Close()
	if err := os.Rename(atm+".new", atm); err != nil {
		t.Fatal(err)
	}
	runThroughReplacement(t, atm, top, issueFile(t, issue), &runStarted)
	wait()
	serveLog, err := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
	if err != nil || !strings.Contains(string(serveLog), "atm serve version: new-build") {
		t.Fatalf("replacement server identity = %q, %v", serveLog, err)
	}
	release()
	if runs, err := Runs(top); err != nil || len(runs) != 1 || runs[0].Outcome != "running" {
		t.Fatalf("new server run = %+v, %v", runs, err)
	}
}

func runThroughReplacement(t *testing.T, atm, top, issuePath string, runStarted *bool) {
	t.Helper()
	cmd := exec.Command(atm, "run", issuePath)
	cmd.Dir, cmd.Stdout = top, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	*runStarted = true
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		serveLog, _ := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
		t.Fatalf("atm run did not return; serve log: %s", serveLog)
	}
}

func cleanupReplacementServers(t *testing.T, atm, top string, old *exec.Cmd, runStarted bool, release func()) {
	t.Helper()
	release()
	if runStarted {
		waitForRunToFinish(t, atm, top)
	}
	killServeProcesses(t, top, old.Process.Pid)
	_ = old.Wait()
}

func waitForRunToFinish(t *testing.T, atm, top string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		cmd := exec.Command(atm, "axi", "runs", "--json")
		cmd.Dir = top
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("atm axi runs --json: %v", err)
		}
		var runs struct {
			Runs []struct {
				Outcome string `json:"outcome"`
			}
		}
		if err := json.Unmarshal(out, &runs); err != nil {
			t.Fatalf("decode atm axi runs --json: %v", err)
		}
		if len(runs.Runs) == 1 && runs.Runs[0].Outcome != "running" {
			return
		}
	}
	t.Fatal("the run did not finish after releasing its fake agent")
}

func killServeProcesses(t *testing.T, top string, pids ...int) {
	t.Helper()
	log, err := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(log), "\n") {
		pidText, ok := strings.CutPrefix(line, "atm serve pid:")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidText))
		if err == nil {
			pids = append(pids, pid)
		}
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// Under GOCOVERDIR (scripts/test.sh) the binary buildATM makes is instrumented: an atm --version run
// leaves its counters behind, so cover-diff can see which lines an end-to-end run executes. Without
// it the build stays plain: byte-identical to go build and writing nothing at run time.
func TestBuildATMInstrumentsOnlyUnderGoCoverDir(t *testing.T) {
	dir := t.TempDir()
	counters := filepath.Join(dir, "with-gocoverdir")
	if err := os.Mkdir(counters, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "atm")
	t.Setenv("GOCOVERDIR", counters)
	version := "cover-check"
	buildATM(t, bin, version)
	if out, err := exec.Command(bin, "--version").CombinedOutput(); err != nil {
		t.Fatalf("atm --version: %v\n%s", err, out)
	}
	if files, _ := filepath.Glob(filepath.Join(counters, "covcounters.*")); len(files) == 0 {
		t.Fatal("atm --version under GOCOVERDIR left no covcounters file: the build is not instrumented")
	}

	t.Setenv("GOCOVERDIR", "")
	plain := filepath.Join(dir, "plain")
	buildATM(t, plain, version)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path is unavailable")
	}
	reference := filepath.Join(dir, "reference")
	ldflags := fmt.Sprintf("-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Version=%s", version)
	direct := exec.Command("go", "build", "-ldflags", ldflags, "-o", reference, "./cmd/atm")
	direct.Dir = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if out, err := direct.CombinedOutput(); err != nil {
		t.Fatalf("plain go build: %v\n%s", err, out)
	}
	plainBytes, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	referenceBytes, err := os.ReadFile(reference)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plainBytes, referenceBytes) {
		t.Fatal("the build without GOCOVERDIR is not the plain go build binary")
	}
	untouched := filepath.Join(dir, "without-gocoverdir")
	if err := os.Mkdir(untouched, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCOVERDIR", untouched)
	if out, err := exec.Command(plain, "--version").CombinedOutput(); err != nil {
		t.Fatalf("plain atm --version: %v\n%s", err, out)
	}
	if files, _ := filepath.Glob(filepath.Join(untouched, "covcounters.*")); len(files) != 0 {
		t.Fatal("the build without GOCOVERDIR wrote coverage counters")
	}
}

func buildATM(t *testing.T, path, version string) {
	t.Helper()
	ldflags := fmt.Sprintf("-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Version=%s", version)
	args := []string{"build", "-ldflags", ldflags}
	// Under GOCOVERDIR (scripts/test.sh) the built atm writes coverage counters on exit, so
	// cover-diff can see which changed lines an end-to-end run executes.
	if os.Getenv("GOCOVERDIR") != "" {
		args = append(args, "-cover", "-coverpkg=./...")
	}
	args = append(args, "-o", path, "./cmd/atm")
	cmd := exec.Command("go", args...)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path is unavailable")
	}
	cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build atm %s: %v\n%s", version, err, out)
	}
}
