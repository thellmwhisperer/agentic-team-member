//go:build unix

package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// fakeHello is a claude that writes its directory and arguments to $FAKE_HELLO_CALL, then plays $FAKE_HELLO:
// no-auth exits 2 after 1.5 s, past the hang's limit, hang waits while its owner and directory exist, any other
// fails with a controlling terminal and answers OK to the brief.
const fakeHello = `#!/bin/sh
{ pwd; echo "$@"; } > "$FAKE_HELLO_CALL"
case "$FAKE_HELLO" in
no-auth)
	ATM_FAKE_HELLO_HANG=1 ATM_FAKE_HELLO_TICKS=6 "$ATM_FAKE_TEST_BINARY" -test.run=^TestFakeHelloClaudeHang$ >/dev/null
	echo 'no auth' >&2; exit 2 ;;
hang) ATM_FAKE_HELLO_HANG=1 exec "$ATM_FAKE_TEST_BINARY" -test.run=^TestFakeHelloClaudeHang$ ;;
esac
if (exec </dev/tty) 2>/dev/null; then echo 'has a terminal'; exit 3; fi
grep -q 'Reply with the single word OK and nothing else.' || exit 4
echo '{"type": "result", "result": "OK"}'
`

// helloRepo is a repository whose agent is fakeHello, model m1, playing mode; it returns the repository and
// where the fake writes its call.
func helloRepo(t *testing.T, mode string) (dir, call string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATM_FAKE_TEST_BINARY", exe)
	t.Setenv("ATM_FAKE_OWNER_PID", strconv.Itoa(os.Getpid()))
	return helloRepoWith(t, "claude", mode, fakeHello)
}

// Invoked by fakeHello instead of tail so a killed test cannot leave the hang running; with $ATM_FAKE_HELLO_TICKS
// it stops after that many ticks.
func TestFakeHelloClaudeHang(t *testing.T) {
	if os.Getenv("ATM_FAKE_HELLO_HANG") != "1" {
		return
	}
	owner, err := strconv.Atoi(os.Getenv("ATM_FAKE_OWNER_PID"))
	if err != nil || owner <= 0 {
		t.Fatalf("invalid fake claude owner: %v", err)
	}
	ticks, _ := strconv.Atoi(os.Getenv("ATM_FAKE_HELLO_TICKS"))
	wd, wdErr := os.Getwd()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for i := 1; ; i++ {
		<-tick.C
		_, err := os.Stat(wd)
		if syscall.Kill(owner, 0) == syscall.ESRCH || wdErr != nil || err != nil || i == ticks {
			return
		}
	}
}

func helloRepoWith(t *testing.T, harness, mode, fake string) (dir, call string) {
	t.Helper()
	dir = repo(t, "https://example.com/o/r.git")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, harness), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	call = filepath.Join(t.TempDir(), "call")
	t.Setenv("FAKE_HELLO_CALL", call)
	t.Setenv("FAKE_HELLO", mode)
	config := "harness: " + harness + "\nmodel: m1\ninstall: ''\ntest: ''\ntypecheck: ''\nlint: ''\ntest_file: ''\n" +
		"test_patterns: []\ndocs_patterns: []\ndelivery: ''\n"
	if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".atm/\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir, call
}

func lastLine(out string) string {
	ls := strings.Split(strings.TrimSpace(out), "\n")
	return ls[len(ls)-1]
}

func assertHelloSucceeded(t *testing.T, out string, err error, harness string) {
	t.Helper()
	if err != nil || !strings.HasPrefix(lastLine(out), "ok agent: "+harness+" m1 answered in ") {
		t.Fatalf("atm doctor --agent: %v\n%s", err, out)
	}
}

func assertHelloRepoRemoved(t *testing.T, call string) {
	t.Helper()
	b, err := os.ReadFile(call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(strings.TrimSpace(string(b))); err == nil {
		t.Fatalf("hello repository was not removed: %q", b)
	}
}

func TestDoctorWithoutAgentMakesNoCallAndSaysHow(t *testing.T) {
	_, call := helloRepo(t, "ok")
	out, err := atm(t, "doctor")
	if err != nil {
		t.Fatalf("atm doctor: %v\n%s", err, out)
	}
	if got := lastLine(out); got != "run atm doctor --agent to say hello to claude" {
		t.Fatalf("got last line %q\n%s", got, out)
	}
	if _, err := os.Stat(call); err == nil {
		t.Fatal("atm doctor called the agent")
	}
}

func TestDoctorAgentReportsUnknownHarnessWithoutStartingIt(t *testing.T) {
	dir, call := helloRepo(t, "ok")
	config := "harness: unknown\ninstall: ''\ntest: ''\ntypecheck: ''\nlint: ''\ntest_file: ''\n" +
		"test_patterns: []\ndocs_patterns: []\ndelivery: ''\n"
	if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := atm(t, "doctor", "--agent")
	if err == nil {
		t.Fatalf("want unknown harness to fail\n%s", out)
	}
	if got := lastLine(out); !strings.HasPrefix(got, `FAIL agent: unknown: harness "unknown" unknown`) {
		t.Fatalf("got %q\n%s", got, out)
	}
	if _, err := os.Stat(call); err == nil {
		t.Fatal("doctor started the fake agent for an unknown harness")
	}
}

func TestDoctorAgentCodexUsesTemporaryGitRepository(t *testing.T) {
	codex := `#!/bin/sh
if [ "$(git rev-parse --is-inside-work-tree 2>/dev/null)" != true ]; then
	 echo 'not a git repository' >&2
	 exit 2
fi
printf '%s\n' "$PWD" > "$FAKE_HELLO_CALL"
final=
while [ "$#" -gt 0 ]; do
	if [ "$1" = "-o" ]; then
		shift
		final=$1
	fi
	shift
done
printf 'OK\n' > "$final"
echo '{"type":"thread.started"}'
`
	_, call := helloRepoWith(t, "codex", "ok", codex)
	out, err := atm(t, "doctor", "--agent")
	assertHelloSucceeded(t, out, err, "codex")
	assertHelloRepoRemoved(t, call)
}

func TestDoctorAgentOtherHarnessesUseTemporaryGitRepository(t *testing.T) {
	fake := `#!/bin/sh
if [ "$(git rev-parse --is-inside-work-tree 2>/dev/null)" != true ]; then
	echo 'not a git repository' >&2
	exit 2
fi
printf '%s\n' "$PWD" > "$FAKE_HELLO_CALL"
case "${0##*/}" in
claude) echo '{"type":"result","result":"OK"}' ;;
opencode) echo '{"type":"text","part":{"text":"OK"}}' ;;
pi) echo '{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"OK"}]}}' ;;
esac
`
	for _, harness := range []string{"claude", "opencode", "pi"} {
		t.Run(harness, func(t *testing.T) {
			_, call := helloRepoWith(t, harness, "ok", fake)
			out, err := atm(t, "doctor", "--agent")
			assertHelloSucceeded(t, out, err, harness)
			assertHelloRepoRemoved(t, call)
		})
	}
}

func TestDoctorAgentSaysHelloTheWayARunDoes(t *testing.T) {
	for _, c := range []struct{ mode, want string }{
		{"ok", "ok agent: claude m1 answered in "},
		{"no-auth", "FAIL agent: claude: exit code 2"},
		{"hang", "FAIL agent: claude: timed out"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			defer func(d time.Duration) { helloLimit = d }(helloLimit)
			if c.mode == "hang" {
				helloLimit = time.Second
			}
			dir, call := helloRepo(t, c.mode)
			out, err := atm(t, "doctor", "--agent")
			got := lastLine(out)
			if !strings.HasPrefix(got, c.want) || (c.mode == "ok") != (err == nil) {
				t.Fatalf("got %q, err %v, want %q\n%s", got, err, c.want, out)
			}
			if c.mode == "no-auth" && !strings.Contains(got, "no auth") {
				t.Fatalf("want its last lines in %q", got)
			}
			b, err := os.ReadFile(call)
			if err != nil {
				t.Fatal(err)
			}
			cwd, args, _ := strings.Cut(string(b), "\n")
			if !strings.Contains(args, "-p --output-format stream-json") || !strings.Contains(args, "--model m1") {
				t.Fatalf("want a run's command line, got %q", args)
			}
			if _, err := os.Stat(cwd); err == nil || strings.HasPrefix(cwd, dir) {
				t.Fatalf("want a temporary directory, removed, got %q", cwd)
			}
			if st, _ := exec.Command("git", "status", "--porcelain").Output(); string(st) != "?? .atm.yaml\n" {
				t.Fatalf("doctor wrote to the repository: %q", st)
			}
		})
	}
}

// TestDoctorAgentGetsNoTerminal runs atm doctor --agent on a terminal that controls it, in this test binary
// again, and the fake fails when it has one.
func TestDoctorAgentGetsNoTerminal(t *testing.T) {
	if os.Getenv("DOCTOR_ON_TTY") != "" {
		if f, err := os.Open("/dev/tty"); err != nil {
			t.Fatalf("want a controlling terminal: %v", err)
		} else {
			_ = f.Close()
		}
		out, err := atm(t, "doctor", "--agent")
		assertHelloSucceeded(t, out, err, "claude")
		return
	}
	dir, _ := helloRepo(t, "ok")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDoctorAgentGetsNoTerminal$", "-test.v")
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "DOCTOR_ON_TTY=1")
	p, err := pty.Start(cmd) // a session of its own, the terminal its controlling one
	if err != nil {
		t.Fatal(err)
	}
	var shown bytes.Buffer
	_, _ = io.Copy(&shown, p) // until nothing holds the terminal
	err = cmd.Wait()
	_ = p.Close()
	if err != nil {
		t.Fatalf("on a terminal: %v\n%s", err, shown.String())
	}
}
