//go:build unix

package run

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// heldWork is a worker's work that says it started, at $STEER_READY, and waits until the test writes a line to the
// FIFO $STEER_GO, then fixes a.txt; release lets it go on.
func heldWork(t *testing.T) (ready string, release func()) {
	t.Helper()
	dir := t.TempDir()
	ready, fifo := filepath.Join(dir, "ready"), filepath.Join(dir, "go")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEER_READY", ready)
	t.Setenv("STEER_GO", fifo)
	t.Setenv("FAKE_AGENT_WORK", `: > "$STEER_READY"; read _ < "$STEER_GO"; `+fixWork)
	released := false
	release = func() {
		if !released {
			released = true
			if err := os.WriteFile(fifo, []byte("go\n"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	return ready, release
}

// waitFor fails t unless cond holds within a minute; it checks it every 10 ms.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(time.Minute)
	for !cond() {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("not within a minute: %s", what)
		}
	}
}

func exists(path string) func() bool {
	return func() bool { _, err := os.Stat(path); return err == nil }
}

// steer is the real atm steer on top, and what it printed.
func steer(bin, top string, args ...string) (string, error) {
	cmd := exec.Command(bin, append([]string{"steer"}, args...)...)
	cmd.Dir = top
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Each test's background process is its own atm serve, which ends cleanly when the test does, so a covered build
// leaves the counters of what it ran.

// A pi worker takes atm steer's message mid-run: the worker log has the steer, then pi's user message with it,
// then the worker's final message, and the run passes. A message pi refuses, a run with no agent working and a run
// that does not exist each fail atm steer, saying why.
func TestSteerDeliversToAPiWorker(t *testing.T) {
	top := atmRepo(t)
	ready, release := heldWork(t)
	t.Cleanup(release)
	t.Setenv("FAKE_PI_STEERS", "1")
	t.Setenv("FAKE_PI_REFUSE", "refuse this")
	bin := followUpsServer(t, top) // after the fake's environment, which the background process passes on
	const message = "stop repeating the grep for install.sh; read install.sh once and move on"
	f := startFollow(t, bin, top, false, "run", "--follow", "--harness", "pi", issueFile(t, issue))
	waitFor(t, "the pi worker to start its work", exists(ready))
	if out, err := steer(bin, top, "issue-1", "refuse this"); err == nil ||
		!strings.Contains(out, "run issue-1: pi refused the message: steering is off") {
		t.Fatalf("a refused steer: %v\n%s", err, out)
	}
	steered := make(chan string, 1)
	go func() {
		out, err := steer(bin, top, "issue-1", message)
		if err != nil {
			out += err.Error()
		}
		steered <- out
	}()
	release()
	if out := <-steered; out != "the agent of issue-1 took the message\n" {
		t.Fatalf("atm steer: %q", out)
	}
	f.exit(0)
	assertSteerInLog(t, filepath.Join(top, ".atm", "runs", "issue-1", "worker.jsonl"), message)
	for _, c := range []struct{ run, message, want string }{
		{"issue-1", "too late", "run issue-1 has no agent working now; the message was not sent"},
		{"no-such-run", "hello", "no run no-such-run"},
	} {
		if out, err := steer(bin, top, c.run, c.message); err == nil || !strings.Contains(out, c.want) {
			t.Fatalf("atm steer %s %q: want %q, got %v\n%s", c.run, c.message, c.want, err, out)
		}
	}
}

// assertSteerInLog checks the worker log at path has the steer of message, then pi's user message with it, then
// the worker's final message.
func assertSteerInLog(t *testing.T, path, message string) {
	t.Helper()
	lines := logLines(t, path)
	at := func(typ, role, has string) int { // the steer's message is a string, which has no role
		for i, l := range lines {
			var ev struct {
				Type    string
				Message json.RawMessage
			}
			var m struct{ Role string }
			_ = json.Unmarshal([]byte(l), &ev)
			_ = json.Unmarshal(ev.Message, &m)
			if ev.Type == typ && m.Role == role && strings.Contains(l, has) {
				return i
			}
		}
		return -1
	}
	sent, user, final := at("steer", "", message), at("message_end", "user", message),
		at("message_end", "assistant", "test_file")
	if sent < 0 || user <= sent || final <= user {
		t.Fatalf("want the steer, then pi's user message, then the final message: %d, %d, %d\n%s", sent, user, final,
			strings.Join(lines, "\n"))
	}
}

// Every harness but pi refuses atm steer while its worker works, and the run goes on as if none was sent.
func TestSteerRefusedOnOtherHarnesses(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode", "cursor", "hermes"} {
		t.Run(harness, func(t *testing.T) {
			top := atmRepo(t)
			if harness == "hermes" { // the fake hermes is this test binary under that name
				dir := t.TempDir()
				exe, err := os.Executable()
				if err == nil {
					err = os.Symlink(exe, filepath.Join(dir, "hermes"))
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			}
			ready, release := heldWork(t)
			t.Cleanup(release)
			bin := followUpsServer(t, top)
			f := startFollow(t, bin, top, false, "run", "--follow", "--harness", harness, issueFile(t, issue))
			waitFor(t, "the worker to start its work", exists(ready))
			out, err := steer(bin, top, "issue-1", "a message for "+harness)
			if want := harness + " cannot take a message mid-run"; err == nil || !strings.Contains(out, want) {
				t.Fatalf("want %q, got %v\n%s", want, err, out)
			}
			release()
			f.exit(0)
			if b, _ := os.ReadFile(filepath.Join(top, ".atm", "runs", "issue-1", "worker.jsonl")); strings.Contains(
				string(b), "a message for") {
				t.Fatalf("the refused message reached the worker:\n%s", b)
			}
		})
	}
}

// pi in RPC mode ends when its agent settles: a model call pi retries after its agent_end goes on until the retry
// ends, a compaction after its last agent_end ends once the compaction does, and a prompt pi refuses fails the
// agent with pi's reason.
func TestSteerPiSettles(t *testing.T) {
	t.Run("retried", func(t *testing.T) {
		top := atmRepo(t)
		t.Setenv("FAKE_AGENT", "pi-retry")
		bin := followUpsServer(t, top)
		startFollow(t, bin, top, false, "run", "--follow", "--harness", "pi", issueFile(t, issue)).exit(0)
		b, err := os.ReadFile(filepath.Join(top, ".atm", "runs", "issue-1", "worker.jsonl"))
		if err != nil || strings.Count(string(b), `"type":"get_state"`) != 3 ||
			!strings.Contains(string(b), "auto_retry_end") {
			t.Fatalf("want a get_state at each agent_end and at the retry's end, the run past the retry: %v\n%s", err, b)
		}
	})
	t.Run("compacted", func(t *testing.T) {
		top := atmRepo(t)
		t.Setenv("FAKE_AGENT", "pi-compact")
		bin := followUpsServer(t, top)
		startFollow(t, bin, top, false, "run", "--follow", "--harness", "pi", issueFile(t, issue)).exit(0)
		b, err := os.ReadFile(filepath.Join(top, ".atm", "runs", "issue-1", "worker.jsonl"))
		if err != nil || !strings.Contains(string(b), `"isCompacting":true`) ||
			strings.Count(string(b), `"type":"get_state"`) != 2 {
			t.Fatalf("want a get_state at the agent_end, another at the compaction's end: %v\n%s", err, b)
		}
	})
	t.Run("report returned to its session", func(t *testing.T) { // the session get_state names, as no header does
		top := atmRepo(t)
		call := filepath.Join(t.TempDir(), "call.json")
		t.Setenv("FAKE_AGENT_CALL", call)
		t.Setenv("FAKE_AGENT", "no-report")
		bin := followUpsServer(t, top)
		startFollow(t, bin, top, false, "run", "--follow", "--harness", "pi", issueFile(t, issue)).exit(0)
		got := agentCall(t, call)
		if i := slices.Index(got.Args, "--session"); i < 0 || i+1 == len(got.Args) || got.Args[i+1] != "pi-rpc-session" ||
			!strings.Contains(got.Stdin, "Your final message was rejected") {
			t.Fatalf("want the rejected report returned to pi's session: %v\n%s", got.Args, got.Stdin)
		}
	})
	t.Run("prompt refused", func(t *testing.T) {
		top := atmRepo(t)
		t.Setenv("FAKE_AGENT", "pi-prompt-fail")
		bin := followUpsServer(t, top)
		f := startFollow(t, bin, top, false, "run", "--follow", "--harness", "pi", issueFile(t, issue))
		var failed obj
		for line := range f.lines {
			var ev obj
			if json.Unmarshal([]byte(line), &ev) == nil && ev["step"] == "agent" && ev["state"] == "failed" {
				failed = ev
			}
		}
		if why, _ := failed["error"].(string); !strings.Contains(why, "pi: prompt: No API key for fake") {
			t.Fatalf("want the agent failed with pi's reason, got %v", failed)
		}
	})
}

// A steer pi has queued but not delivered when its agent ends fails, and so does one sent after pi settled while
// its process still lives: pi would only queue either, so neither is dropped silently.
func TestSteerNeverDropsAMessage(t *testing.T) {
	top := atmRepo(t)
	ready, release := heldWork(t)
	t.Cleanup(release)
	linger := t.TempDir()
	fifo := filepath.Join(linger, "go")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	end := func() { // a reader waits on fifo, or else the write fails at once
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_, _ = w.WriteString("go\n")
			_ = w.Close()
		}
	}
	t.Cleanup(end)
	t.Setenv("FAKE_PI_LINGER", linger)
	bin := followUpsServer(t, top)
	f := startFollow(t, bin, top, false, "run", "--follow", "--harness", "pi", issueFile(t, issue))
	waitFor(t, "the pi worker to start its work", exists(ready))
	queued := make(chan string, 1)
	go func() {
		out, err := steer(bin, top, "issue-1", "never delivered")
		if err != nil {
			out += err.Error()
		}
		queued <- out
	}()
	log := filepath.Join(top, ".atm", "runs", "issue-1", "worker.jsonl")
	waitFor(t, "ATM to send the steer", func() bool {
		b, _ := os.ReadFile(log)
		return strings.Contains(string(b), `"type":"steer"`)
	})
	release()
	waitFor(t, "pi to settle and linger", exists(filepath.Join(linger, "ready")))
	if out, err := steer(bin, top, "issue-1", "after settling"); err == nil ||
		!strings.Contains(out, "run issue-1: no agent working now; the message was not sent") {
		t.Fatalf("a steer after pi settled: %v\n%s", err, out)
	}
	end()
	if out := <-queued; !strings.Contains(out, "run issue-1: pi ended before the message reached it") {
		t.Fatalf("a steer pi never delivered: %q", out)
	}
	f.exit(0)
}
