package scripts

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// fakeNoMistakes is no-mistakes with a gate that outlasts axi run's wait: each axi run shows the gate's step on
// stderr. The first holds until its stdin closes, then ends like an elapsed --wait with the gate still running.
// Later ones find the gate over and end with $FAKE_GATE: passed, or failed with a diagnostic that quotes the
// elapsed-wait text.
const fakeNoMistakes = `#!/bin/sh
echo "$*" >> "$FAKE_NM/calls"
[ "$1 $2" = "axi run" ] || exit 0
echo "step test: running" >&2
if [ ! -e "$FAKE_NM/held" ]; then
	: > "$FAKE_NM/held"
	read -r _
	echo "This bounded hold ended; it is not a pipeline failure"
	echo "error: wait of 8m0s elapsed while driving the run" >&2
	exit 1
fi
if [ "$FAKE_GATE" = passed ]; then
	echo "outcome: passed"
	exit 0
fi
echo 'step test: FAIL TestHold: got "error: wait of 8m0s elapsed while driving the run"' >&2
echo "error: run failed at step test" >&2
exit 1
`

func TestOwnDeliveryLineOutlivesTheGateWait(t *testing.T) {
	b, err := os.ReadFile("../.atm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ Delivery string }
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	for _, gate := range []string{"passed", "failed"} {
		t.Run(gate, func(t *testing.T) {
			progress, calls, rest, err := deliverThroughFake(t, c.Delivery, gate)
			t.Logf("gate=%s progress_before_release=%t calls=%q stderr_after_release=%q exit=%v", gate, progress, calls, rest, err)
			if !progress {
				t.Error("the gate's progress did not reach stderr while the gate ran")
			}
			if want := "init\naxi run --intent ship it\naxi run\n"; calls != want {
				t.Errorf("no-mistakes calls %q, want %q: reattach once after the elapsed hold, never restart", calls, want)
			}
			var exit *exec.ExitError
			switch {
			case gate == "passed" && err != nil:
				t.Errorf("the gate passed after the wait elapsed, the line ended %v:\n%s", err, rest)
			case gate == "failed" && (!errors.As(err, &exit) || exit.ExitCode() != 1 ||
				!strings.Contains(rest, "error: run failed at step test")):
				t.Errorf("the gate failed, the line ended %v:\n%s", err, rest)
			}
		})
	}
}

// deliverThroughFake runs line with sh against fakeNoMistakes ending with gate. It releases the first hold once
// the gate's progress is on stderr, and returns whether it was, the calls to no-mistakes, the rest of stderr and
// how line ended.
func deliverThroughFake(t *testing.T, line, gate string) (progress bool, calls, rest string, err error) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "no-mistakes"), []byte(fakeNoMistakes), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_NM="+dir, "FAKE_GATE="+gate, "ATM_INTENT=ship it")
	release, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for sc := bufio.NewScanner(stderr); !progress && sc.Scan(); {
		progress = sc.Text() == "step test: running"
	}
	_ = release.Close()
	b, _ := io.ReadAll(stderr)
	err = cmd.Wait()
	c, _ := os.ReadFile(filepath.Join(dir, "calls"))
	return progress, string(c), string(b), err
}
