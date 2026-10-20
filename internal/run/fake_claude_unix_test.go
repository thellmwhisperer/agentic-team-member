//go:build unix

package run

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHeldFakeClaudeExitsWhenDirectoryRemoved(t *testing.T) {
	dir := t.TempDir()
	wait, _ := holdFakeAgents(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(fakes, "claude"), "-p")
	cmd.Dir, cmd.Stdin = dir, strings.NewReader("Task type: fix.")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	wait()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("held fake claude did not exit on its own: %v (deadline: %v)", err, ctx.Err())
	}
}

func TestHeldFakeClaudeExitsWhenOwnerDies(t *testing.T) {
	if os.Getenv("ATM_TEST_CLAUDE_OWNER") == "1" {
		root := repo(t, "git@github.com:owner/repo.git", atmYAML)
		fakeGH(t, `[{"workflowName":"Go","conclusion":"success"}]`, false) // main is green
		wait, _ := holdFakeAgents(t, 1)
		if _, err := Start(root, []string{issueFile(t, issue)}); err != nil {
			log, _ := os.ReadFile(filepath.Join(root, ".atm", "serve.log"))
			t.Fatalf("start: %v\n%s", err, log)
		}
		wait()
		if err := json.NewEncoder(os.Stdout).Encode([]string{root, os.Getenv("FAKE_AGENT_BARRIER_READY")}); err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdin.Read(make([]byte, 1)) // the supervisor keeps this pipe open until SIGKILL
		return
	}
	// macOS rejects a unix socket path of 104 bytes or more. Nesting under the runner's
	// temp directory makes that path too long when it cannot be made relative to the cwd.
	tmp, err := os.MkdirTemp("/tmp", "atm-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	owner := exec.Command(os.Args[0], "-test.run=^TestHeldFakeClaudeExitsWhenOwnerDies$")
	owner.Env = append(os.Environ(), "ATM_TEST_CLAUDE_OWNER=1", "TMPDIR="+tmp)
	input, err := owner.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	output, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	owner.Stderr = os.Stderr
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill(); _ = owner.Wait() })
	var paths []string
	ready := make(chan error, 1)
	decoder := json.NewDecoder(output)
	go func() { ready <- decoder.Decode(&paths) }()
	select {
	case err := <-ready:
		if err != nil {
			log, _ := io.ReadAll(io.MultiReader(decoder.Buffered(), output))
			t.Fatalf("owner: %v\n%s", err, log)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("owning test did not start its held fake claude")
	}
	assertHeldClaudeOwnerExit(t, owner, paths[0], paths[1])
}

func assertHeldClaudeOwnerExit(t *testing.T, owner *exec.Cmd, root, ready string) {
	t.Helper()
	t.Chdir(root) // keep the Unix socket name below the platform's path limit
	t.Cleanup(func() { killServeProcesses(t, root) })
	entries, err := os.ReadDir(ready)
	if err != nil || len(entries) != 1 {
		t.Fatalf("held fake claude: %v, %v", entries, err)
	}
	pid, err := strconv.Atoi(entries[0].Name())
	if err != nil || !alive(pid) {
		t.Fatalf("fake claude is not alive: pid %d, %v", pid, err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = proc.Kill()
		until(t, "the server to reap fake claude", func() bool { return !alive(pid) })
	})
	// Keep the original server connection open: reconnecting could start a replacement.
	server, err := net.Dial("unix", short(socket(root)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait() // reap the owner before checking its agents
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for alive(pid) {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("fake claude survived its owning test for 10 seconds")
		}
	}
	if _, err := os.Stat(filepath.Dir(ready)); err != nil {
		t.Fatalf("owner's temporary directory was removed: %v", err)
	}
	if err := server.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := exchange(server, request{Cmd: "runs"}, nil, func(reply) {}); err != nil {
		t.Fatalf("detached server did not survive its owning test: %v", err)
	}
}
