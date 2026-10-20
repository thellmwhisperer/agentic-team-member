package scripts

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"gopkg.in/yaml.v3"
)

// elapsedHold is the installed no-mistakes' output when an axi run's wait ends with the gate still running,
// as the CoS captured it reattaching to a running gate.
const elapsedHold = "error: wait of 1s elapsed while driving the run\n" +
	"help[3]: This bounded hold ended; it is not a pipeline failure and does not mean the daemon is dead.," +
	"Run `no-mistakes axi status` to inspect progress,Re-run `no-mistakes axi run` to reattach for another 1s\n"

// fakeNoMistakes logs each call to $FAKE_NM/log. Its first axi run reports progress on stderr, holds until the
// test writes to the $FAKE_NM/release FIFO, prints elapsedHold and exits 1, the gate still running; the next
// prints $FAKE_NM/end and exits $FAKE_NM_CODE, the gate's terminal result.
const fakeNoMistakes = `#!/bin/sh
d=$FAKE_NM
echo "$*" >> "$d/log"
[ "$1" = init ] && exit 0
[ "$1 $2" = "axi run" ] || exit 2
if [ "$(wc -l < "$d/log")" -eq 2 ]; then
	echo "progress: step test running" >&2
	read _ < "$d/release"
	cat "$d/elapsed" >&2
	exit 1
fi
cat "$d/end" >&2
exit "$FAKE_NM_CODE"
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
	for _, tc := range []struct {
		name, end string
		code      int
	}{
		{"gate passes after the hold", "gate passed\n", 0},
		{"gate failure quoting the hold", "step test failed:\n" + elapsedHold + "error: pipeline failed at step test\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, log, out := deliverAgainstFakeGate(t, c.Delivery, tc.end, tc.code)
			if got != tc.code || strings.Count(log, "axi run") != 2 {
				t.Fatalf("delivery exited %d after these calls, want %d after one reattach:\n%s\noutput:\n%s",
					got, tc.code, log, out)
			}
			if !strings.Contains(out, "progress: step test running\n") || !strings.HasSuffix(out, tc.end) {
				t.Fatalf("output lost the gate's progress or its result:\n%s", out)
			}
			t.Logf("delivery exited %d; calls:\n%soutput on stderr:\n%s", got, log, out)
		})
	}
}

// deliverAgainstFakeGate runs line with sh against fakeNoMistakes, its terminal result end and code, and
// releases the held gate once its progress reaches stderr. It returns line's exit code, the fake's calls and
// line's stderr.
func deliverAgainstFakeGate(t *testing.T, line, end string, code int) (int, string, string) {
	d := t.TempDir()
	bin := filepath.Join(d, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]string{"bin/no-mistakes": fakeNoMistakes, "elapsed": elapsedHold, "end": end} {
		if err := os.WriteFile(filepath.Join(d, name), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	release := filepath.Join(d, "release")
	if err := syscall.Mkfifo(release, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = d
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_NM="+d, "TMPDIR="+d,
		"FAKE_NM_CODE="+strconv.Itoa(code), "ATM_INTENT=fix the wait")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		out.WriteString(sc.Text() + "\n")
		if sc.Text() == "progress: step test running" {
			// The gate is still held: its progress reached stderr before it ended.
			if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := 0
	if err := cmd.Wait(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		got = ee.ExitCode()
	}
	log, _ := os.ReadFile(filepath.Join(d, "log"))
	return got, string(log), out.String()
}
