//go:build unix

package cli

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// The real binary reads the machine choice, keeps a completed screen interactive,
// and returns the run's code after q or Ctrl+C. The default still closes itself.
func TestRealScreenConfig(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "atm")
	args := []string{"build", "-o", bin}
	if os.Getenv("GOCOVERDIR") != "" {
		args = append(args, "-cover", "-coverpkg=./...")
	}
	build := exec.Command("go", append(args, "./cmd/atm")...)
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build atm: %v\n%s", err, out)
	}
	for _, mode := range []string{"", "close", "keep"} {
		t.Run("failed run/"+mode, func(t *testing.T) {
			testFailedScreenChoice(t, bin, mode)
		})
		for _, present := range []bool{true, false} {
			t.Run(fmt.Sprintf("gate %v/%s", present, mode), func(t *testing.T) {
				testGateScreenChoice(t, bin, mode, present)
			})
		}
	}
	t.Run("invalid choice", func(t *testing.T) {
		testInvalidScreenChoice(t, bin)
	})
}

func testFailedScreenChoice(t *testing.T, bin, mode string) {
	t.Helper()
	setupRetryRepo(t)
	onPath(t, "codex", "claude")
	machineScreen(t, mode)
	title := "# Screen remains readable after the run has ended and the terminal user can unfold this entire title" +
		"\n\nRetry.\n"
	if err := os.WriteFile("retry.md", []byte(title), 0o600); err != nil {
		t.Fatal(err)
	}
	sc := configTerminal(t, bin, "run", "retry.md")
	sc.until("q close")
	if mode == "keep" {
		sc.key("i")
		sc.until("entire title")
		sc.key("q")
	}
	sc.exits(2)
	// Opening a completed run also reads the current terminal owner's choice.
	sc = configTerminal(t, bin, "attach", "retry-1")
	sc.until("q close")
	if mode == "keep" {
		sc.key("\t")
		sc.until("ATM ") // tab moved the focus marker from ATM to Findings
		sc.key("\x03")
	}
	sc.exits(2)
}

func testGateScreenChoice(t *testing.T, bin, mode string, present bool) {
	t.Helper()
	repo(t, "https://example.com/o/r.git")
	machineScreen(t, mode)
	replayScreenEnd(t, present)
	sc := configTerminal(t, bin, "attach", "live-run")
	sc.until("q close")
	if present {
		sc.until("▸ ✓ review")
	}
	if mode == "keep" {
		if present {
			sc.key("\th")
			sc.until("gate detail")
			sc.until("ATM*")
		}
		// The first gate request began before the run ended. Let the next one
		// finish, and prove the screen still accepts keys after that answer.
		sc.staysOpen(1500 * time.Millisecond)
		if present {
			sc.key("h")
			sc.until("▸ ✓ review")
		}
		t.Logf("completed terminal, still interactive before q:\n%s", ansi.Strip(sc.raw.String()))
		sc.key("q")
	}
	sc.exits(0)
}

func testInvalidScreenChoice(t *testing.T, bin string) {
	t.Helper()
	repo(t, "https://example.com/o/r.git")
	onPath(t, "claude")
	if out, err := exec.Command(bin, "init").CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	machineScreen(t, "other")
	doctor := exec.Command(bin, "doctor")
	out, err := doctor.CombinedOutput()
	if err == nil || doctor.ProcessState.ExitCode() != 2 ||
		!strings.Contains(string(out), `screen "other": want close or keep`) {
		t.Fatalf("doctor accepted an invalid screen: %v\n%s", err, out)
	}
	sc := configTerminal(t, bin, "attach", "live-run")
	sc.exits(2)
	if !strings.Contains(sc.raw.String(), `screen "other": want close or keep`) {
		t.Fatalf("attach lost the config error:\n%s", sc.raw.String())
	}
}

func machineScreen(t *testing.T, mode string) {
	t.Helper()
	if mode == "" {
		return
	}
	path := filepath.Join(os.Getenv("HOME"), ".config", "atm", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("screen: "+mode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// replayScreenEnd serves a live run ending at Delivery and a completed gate.
func replayScreenEnd(t *testing.T, present bool) {
	t.Helper()
	if err := os.MkdirAll(".atm", 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(".atm", "atm.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	clone := t.TempDir()
	nm := filepath.Join(t.TempDir(), "no-mistakes")
	script := "#!/bin/sh\ncase \"$2\" in\nstatus) printf 'run:\\n  status: completed\\n" +
		"  steps[1]{step,status,findings,duration_ms}:\\n    review,completed,0,1\\n' ;;\n" +
		"logs) printf 'log[2]{line}:\\n  gate summary\\n  gate detail\\n' ;;\nesac\n"
	if !present {
		script = "#!/bin/sh\nprintf 'run: null\\n'\n"
	}
	if err := os.WriteFile(nm, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(nm)+string(os.PathListSeparator)+os.Getenv("PATH"))
	served := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			served <- err
			return
		}
		defer func() { _ = c.Close() }()
		var req map[string]any
		if err := json.NewDecoder(c).Decode(&req); err != nil {
			served <- err
			return
		}
		_, err = fmt.Fprintf(c, "{\"event\":{\"step\":\"clone\",\"state\":\"passed\",\"clone\":%q}}\n"+
			"{\"event\":{\"step\":\"delivery\",\"state\":\"started\"}}\n"+
			"{\"event\":{\"step\":\"delivery\",\"state\":\"passed\"}}\n"+
			"{\"run\":{\"run\":\"live-run\",\"outcome\":\"passed\",\"ended\":\"2026-11-13T21:00:00Z\"}}\n", clone)
		served <- err
	}()
	t.Cleanup(func() {
		_ = l.Close()
		if err := <-served; err != nil {
			t.Error(err)
		}
	})
}

type screenTerminal struct {
	t      *testing.T
	cmd    *exec.Cmd
	master *os.File
	chunks chan string
	raw    strings.Builder
	from   int // keys must cause a new draw, rather than matching an earlier frame
}

func configTerminal(t *testing.T, bin string, args ...string) *screenTerminal {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	sc := &screenTerminal{t: t, cmd: exec.Command(bin, args...), chunks: make(chan string, 128)}
	var err error
	sc.master, err = pty.StartWithSize(sc.cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sc.cmd.Process.Kill(); _ = sc.master.Close() })
	go func() {
		defer close(sc.chunks)
		b := make([]byte, 4096)
		for {
			n, err := sc.master.Read(b)
			if n > 0 {
				sc.chunks <- string(b[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return sc
}

func (sc *screenTerminal) until(want string) {
	sc.t.Helper()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(ansi.Strip(sc.raw.String()[sc.from:]), want) {
		select {
		case chunk, open := <-sc.chunks:
			if !open {
				sc.t.Fatalf("screen closed before %q:\n%s", want, sc.raw.String())
			}
			sc.raw.WriteString(chunk)
		case <-deadline:
			sc.t.Fatalf("screen never showed %q:\n%s", want, sc.raw.String())
		}
	}
}

func (sc *screenTerminal) key(key string) {
	sc.t.Helper()
	if strings.Contains(sc.raw.String(), "\x1b[?1049l") {
		sc.t.Fatal("screen closed before the key was pressed")
	}
	sc.from = sc.raw.Len()
	if _, err := sc.master.Write([]byte(key)); err != nil {
		sc.t.Fatal(err)
	}
}

func (sc *screenTerminal) staysOpen(d time.Duration) {
	sc.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case chunk, open := <-sc.chunks:
			if !open {
				sc.t.Fatalf("screen: keep closed without a key:\n%s", sc.raw.String())
			}
			sc.raw.WriteString(chunk)
		case <-deadline:
			return
		}
	}
}

func (sc *screenTerminal) exits(code int) {
	sc.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case chunk, open := <-sc.chunks:
			if !open {
				_ = sc.cmd.Wait()
				if got := sc.cmd.ProcessState.ExitCode(); got != code {
					sc.t.Fatalf("atm %v: exit %d, want %d:\n%s", sc.cmd.Args, got, code, sc.raw.String())
				}
				return
			}
			sc.raw.WriteString(chunk)
		case <-deadline:
			sc.t.Fatalf("atm %v did not exit:\n%s", sc.cmd.Args, sc.raw.String())
		}
	}
}
