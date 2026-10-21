//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The real binary: atm run on a terminal shows the run's screen and
// quits by itself when the run ends, leaving its last view; atm attach off one, with no run named, follows the run
// going in plain lines.
func TestRealRunScreenQuitsWhenTheRunEnds(t *testing.T) {
	bin := builtATM(t)
	root := repo(t, "https://example.com/o/r.git", atmSet(atmYAML, "delivery", "echo delivered"))
	top := gitT(t, root, "rev-parse", "--show-toplevel")
	stopServing(t, top)
	wait, release := holdFakeAgents(t, 1)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	f := startFollow(t, bin, top, true, "run", issueFile(t, issue))
	var shown strings.Builder
	untilShown(t, f, &shown, "Agent")
	wait()
	attach, plain := plainAttach(bin, top)
	if err := attach.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attach.Process.Kill(); _ = attach.Wait() })
	release()
	quit := make(chan struct{})
	go func() {
		defer close(quit)
		for l := range f.lines {
			shown.WriteString(l + "\n")
		}
		_ = f.cmd.Wait()
		_ = attach.Wait()
	}()
	select {
	case <-quit:
	case <-time.After(2 * time.Minute):
		t.Fatalf("the screen stayed open after the run ended; stderr:\n%s", f.errors())
	}
	s := ansi.Strip(shown.String())
	if code := f.cmd.ProcessState.ExitCode(); code != 0 || !strings.Contains(s, "q close") {
		t.Fatalf("atm run exit %d, want 0 with the run's last view; the terminal showed:\n%s", code, s)
	}
	if attach.ProcessState.ExitCode() != 0 || strings.Contains(plain.String(), "\x1b") ||
		!strings.Contains(plain.String(), "delivery") {
		t.Fatalf("plain atm attach exit %d, want 0 with the run's lines:\n%s", attach.ProcessState.ExitCode(), plain)
	}
	// Nothing runs now: atm attach with no run named follows the last one, ended.
	again, out := plainAttach(bin, top)
	if err := again.Run(); err != nil || !strings.Contains(out.String(), "delivery") {
		t.Fatalf("atm attach after the run ended: %v\n%s", err, out)
	}
}

// untilShown reads f's terminal into shown until it shows want, two minutes at most.
func untilShown(t *testing.T, f *followed, shown *strings.Builder, want string) {
	t.Helper()
	for !strings.Contains(ansi.Strip(shown.String()), want) {
		select {
		case l, open := <-f.lines:
			if !open {
				t.Fatalf("atm run ended before it showed %q; the terminal showed:\n%s", want, ansi.Strip(shown.String()))
			}
			shown.WriteString(l + "\n")
		case <-time.After(2 * time.Minute):
			t.Fatalf("the screen never showed %q:\n%s", want, ansi.Strip(shown.String()))
		}
	}
}

// plainAttach is the real atm attach on top, no run named, in plain lines, and what it prints.
func plainAttach(bin, top string) (*exec.Cmd, *strings.Builder) {
	var out strings.Builder
	cmd := exec.Command(bin, "attach")
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = top, append(os.Environ(), "NO_COLOR=1"), &out, &out
	return cmd, &out
}

// The real binary: atm attach with no run names none, and one whose background process cannot start says why.
func TestRealAttachWithoutARun(t *testing.T) {
	bin := builtATM(t)
	top := gitT(t, repo(t, "https://example.com/o/r.git", atmYAML), "rev-parse", "--show-toplevel")
	stopServing(t, top)
	writeFixture(t, filepath.Join(top, ".atm"), "not a directory\n")
	for _, want := range []string{".atm", "no run yet"} {
		attach := exec.Command(bin, "attach")
		attach.Dir, attach.Env = top, append(os.Environ(), "NO_COLOR=1")
		out, _ := attach.CombinedOutput()
		if attach.ProcessState.ExitCode() == 0 || !strings.Contains(string(out), want) {
			t.Fatalf("atm attach exit %d, want a failure naming %q:\n%s", attach.ProcessState.ExitCode(), want, out)
		}
		if want == ".atm" {
			if err := os.Remove(filepath.Join(top, ".atm")); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// The real binary reads ~/.config/atm/config.yaml: a key it does not know is named once on stderr and ignored, a
// project key there fails, and so does a file that is not YAML.
func TestRealDoctorReadsTheMachineConfig(t *testing.T) {
	bin := builtATM(t)
	top := gitT(t, repo(t, "https://example.com/o/r.git", atmYAML), "rev-parse", "--show-toplevel")
	machine := filepath.Join(os.Getenv("HOME"), ".config", "atm", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(machine), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ config, stdout, stderr string }{
		{"harness: codex\nretired_key: ATM\n", "ok .atm.yaml: every key declared", "config: ignored key retired_key\n"},
		{"test: npm test\n", "fail .atm.yaml: " + machine + ": test is a project key: put it in .atm.yaml", ""},
		{"harness: [\n", "fail .atm.yaml: " + machine + ":", ""},
	} {
		writeFixture(t, machine, c.config)
		doctor := exec.Command(bin, "doctor")
		doctor.Dir = top
		var stdout, stderr strings.Builder
		doctor.Stdout, doctor.Stderr = &stdout, &stderr
		_ = doctor.Run()
		if !strings.Contains(stdout.String(), c.stdout) ||
			strings.Count(stderr.String(), "config: ignored key") != strings.Count(c.stderr, "config: ignored key") ||
			!strings.Contains(stderr.String(), c.stderr) {
			t.Fatalf("config %q: stdout\n%s\nstderr\n%s", c.config, stdout.String(), stderr.String())
		}
	}
}

// An older server can replay steps that the current binary no longer renders.
func TestRealAttachIgnoresUnknownSteps(t *testing.T) {
	bin := builtATM(t)
	top := atmRepo(t)
	done := replayed(t, top,
		`{"step":"delivery","state":"passed"}`,
		`{"step":"external-stage","state":"started"}`,
		`{"step":"external-stage","check":"external-check","state":"failed"}`,
	)
	shown := coldDeliveryAttach(t, bin, top, "old-run")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown, "✓ Delivery") || strings.Contains(shown, "external-") {
		t.Fatalf("unknown steps changed the screen:\n%s", shown)
	}
}
