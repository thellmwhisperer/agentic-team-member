//go:build unix

package e2e

import (
	"bufio"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The sleeping agent never finishes and its grandchild shares its stdout: killing the agent alone would
// leave ATM reading a stream that stays open. Killing the process group closes it.
func TestSleepingAgentHoldsStdoutThroughAGrandchild(t *testing.T) {
	env, _ := fakes(t, "sleeping")
	cmd := exec.Command(filepath.Join(fakeDir, "pi"), "brief")
	cmd.Dir, cmd.Env = target(t), env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines, grandchild := bufio.NewScanner(out), false
	for !grandchild && lines.Scan() {
		grandchild = strings.Contains(lines.Text(), `"grandchild"`)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, out) // returns once every holder of the stream is dead
	_ = cmd.Wait()
	if !grandchild {
		t.Fatal("no grandchild wrote to the agent's stdout")
	}
	if cmd.ProcessState.Success() {
		t.Fatal("the sleeping agent finished on its own")
	}
}
