//go:build unix

package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// fakeHello is a claude that writes its directory and arguments to $FAKE_HELLO_CALL, then plays $FAKE_HELLO:
// no-auth exits 2, hang never ends, any other fails with a controlling terminal and answers OK to the brief.
const fakeHello = `#!/bin/sh
{ pwd; echo "$@"; } > "$FAKE_HELLO_CALL"
case "$FAKE_HELLO" in
no-auth) echo 'no auth' >&2; exit 2 ;;
hang) exec tail -f /dev/null ;;
esac
if : 2>/dev/null </dev/tty; then echo 'has a terminal'; exit 3; fi
grep -q 'Reply with the single word OK and nothing else.' || exit 4
echo '{"type": "result", "result": "OK"}'
`

// helloRepo is a repository whose agent is fakeHello, model m1, playing mode; it returns the repository and
// where the fake writes its call.
func helloRepo(t *testing.T, mode string) (dir, call string) {
	t.Helper()
	dir = repo(t, "https://example.com/o/r.git")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeHello), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	call = filepath.Join(t.TempDir(), "call")
	t.Setenv("FAKE_HELLO_CALL", call)
	t.Setenv("FAKE_HELLO", mode)
	config := "harness: claude\nmodel: m1\ninstall: ''\ntest: ''\ntypecheck: ''\nlint: ''\ntest_file: ''\n" +
		"test_patterns: []\ndocs_patterns: []\ndelivery: ''\n"
	if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, call
}

func lastLine(out string) string {
	ls := strings.Split(strings.TrimSpace(out), "\n")
	return ls[len(ls)-1]
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

func TestDoctorAgentSaysHelloTheWayARunDoes(t *testing.T) {
	for _, c := range []struct{ mode, want string }{
		{"ok", "ok agent: claude m1 answered in "},
		{"no-auth", "FAIL agent: claude: exit code 2"},
		{"hang", "FAIL agent: claude: timed out"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			defer func(d time.Duration) { helloLimit = d }(helloLimit)
			helloLimit = time.Second
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
		if err != nil || !strings.HasPrefix(lastLine(out), "ok agent: claude m1") {
			t.Fatalf("atm doctor --agent: %v\n%s", err, out)
		}
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
