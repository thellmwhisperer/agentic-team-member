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
	c, err := net.Dial("unix", short(socket(top)))
	if err != nil {
		t.Fatalf("the background process is gone after the run: %v", err)
	}
	_ = c.Close()
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
