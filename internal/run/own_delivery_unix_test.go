//go:build unix

package run

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"
	"gopkg.in/yaml.v3"
)

const ownDeliveryScript = `#!/bin/sh
# ATM's delivery: every run works in a fresh clone, so no-mistakes is initialised there first.
no-mistakes init || exit 1
exec no-mistakes axi run --intent "$ATM_INTENT"
`

// ownDeliveryNM returns from axi run at review; gateNM below answers the screen's queries afterwards.
const ownDeliveryNM = `#!/bin/sh
case "$1 $2" in
"init ")
	printf 'init\n' >>"$FAKE_NM/delivery-calls"
	exit "${FAKE_NM_INIT_EXIT:-0}" ;;
"axi run")
	[ "$#" = 4 ] && [ "$3" = --intent ] || exit 2
	printf 'axi run --intent\n' >>"$FAKE_NM/delivery-calls"
	printf '%s' "$4" >"$FAKE_NM/intent"
	echo 'review awaiting_approval'
	exit 0 ;;
esac
`

func TestOwnDeliveryLineLaunchesTheGate(t *testing.T) {
	bin := builtATM(t)
	t.Run("init", func(t *testing.T) { ownDeliveryInit(t, bin) })
	t.Run("init errors", func(t *testing.T) { ownDeliveryInitErrors(t, bin) })
	b, err := os.ReadFile("../../.atm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var own struct{ Delivery string }
	if err := yaml.Unmarshal(b, &own); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../scripts/deliver.sh")
	if err != nil || string(script) != ownDeliveryScript || own.Delivery != "sh scripts/deliver.sh" {
		t.Fatalf("repository delivery = %q, script = %q, %v", own.Delivery, script, err)
	}
	for _, failInit := range []bool{false, true} {
		name := "review gate"
		if failInit {
			name = "init fails"
		}
		t.Run(name, func(t *testing.T) { ownDeliveryLaunch(t, bin, own.Delivery, string(script), failInit) })
	}
}

func ownDeliveryInit(t *testing.T, bin string) {
	t.Helper()
	for _, existing := range []string{"", ".atm.yaml", "scripts/deliver.sh", "both"} {
		t.Run("preserve "+existing, func(t *testing.T) {
			top := t.TempDir()
			gitT(t, top, "init", "-q")
			want := map[string]string{".atm.yaml": "", "scripts/deliver.sh": ownDeliveryScript}
			for name := range want {
				if existing == name || existing == "both" {
					if err := os.MkdirAll(filepath.Dir(filepath.Join(top, name)), 0o755); err != nil {
						t.Fatal(err)
					}
					want[name] = "existing " + name + "\n"
					writeFixture(t, filepath.Join(top, name), want[name])
				}
			}
			for range 2 {
				cmd := exec.Command(bin, "init")
				cmd.Dir = top
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("atm init: %v\n%s", err, out)
				}
				for name, text := range want {
					ownDeliveryInitFile(t, top, name, text)
				}
			}
		})
	}
}

func ownDeliveryInitFile(t *testing.T, top, name, text string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(top, name))
	if err != nil || text != "" && string(got) != text {
		t.Fatalf("atm init %s = %q, want %q, %v", name, got, text, err)
	}
	if name == ".atm.yaml" && text == "" {
		var generated struct{ Delivery string }
		if err := yaml.Unmarshal(got, &generated); err != nil || generated.Delivery != "sh scripts/deliver.sh" {
			t.Fatalf("atm init delivery = %q, want sh scripts/deliver.sh, %v", generated.Delivery, err)
		}
	}
}

func ownDeliveryLaunch(t *testing.T, bin, delivery, script string, failInit bool) {
	t.Helper()
	nm := gateFake(t)
	top := gitT(t, repo(t, t.TempDir(), atmSet(atmYAML, "delivery", delivery)), "rev-parse", "--show-toplevel")
	for _, dir := range []string{"scripts", ".atm"} {
		if err := os.Mkdir(filepath.Join(top, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, filepath.Join(top, "scripts", "deliver.sh"), script)
	gitT(t, top, "add", "scripts/deliver.sh")
	gitT(t, top, "commit", "-q", "--amend", "--no-edit")
	t.Setenv("FAKE_AGENT_CALL", filepath.Join(t.TempDir(), "call.json"))
	fake, err := exec.LookPath("no-mistakes")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, fake, ownDeliveryNM+gateNM, 0o755)
	if failInit {
		t.Setenv("FAKE_NM_INIT_EXIT", "7")
	}
	server := startVersionServe(t, bin, top)
	out, err := versionLaunch(bin, top, issueFile(t, issue))
	label := strings.TrimSpace(string(out))
	if err != nil || label == "" {
		t.Fatalf("atm run: %v\n%s", err, out)
	}
	end, err := Attach(top, label, io.Discard)
	calls, _ := os.ReadFile(filepath.Join(nm, "delivery-calls"))
	if failInit {
		ownDeliveryInitFailed(t, top, label, end, err, string(calls))
	} else {
		ownDeliveryAtReview(t, bin, top, label, nm, end, err, string(calls))
	}
	endDeliveryServer(t, server, top)
}

func ownDeliveryInitFailed(t *testing.T, top, label string, end Outcome, err error, calls string) {
	t.Helper()
	if err != nil || end.Outcome != "failed" || end.FailedNode != "delivery" || calls != "init\n" {
		t.Fatalf("failed init: %+v, %v, calls %q", end, err, calls)
	}
	if err := Answer(top, label, "stop"); err != nil {
		t.Fatal(err)
	}
	if end, err := Attach(top, label, io.Discard); err != nil || end.Outcome != "failed" {
		t.Fatalf("stop after failed init: %+v, %v", end, err)
	}
}

func ownDeliveryAtReview(t *testing.T, bin, top, label, nm string, end Outcome, err error, calls string) {
	t.Helper()
	intent, readErr := os.ReadFile(filepath.Join(nm, "intent"))
	if err != nil || end.Outcome != "passed" || calls != "init\naxi run --intent\n" ||
		readErr != nil || string(intent) != issue {
		t.Fatalf("delivery: %+v, %v; calls %q; intent %q, %v", end, err, calls, intent, readErr)
	}
	report := readReport(t, filepath.Join(top, ".atm", "runs", label, "report.json"))
	if node := reportNode(t, report, "delivery"); node["result"] != "passed" {
		t.Fatalf("delivery did not pass at the review gate: %v", node)
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	cmd := exec.Command(bin, "attach", label)
	cmd.Dir = top
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	a := &attached{t: t, cmd: cmd, p: terminal, chunks: make(chan string, 64), rows: 40}
	t.Cleanup(func() { _ = a.cmd.Process.Kill(); _ = a.p.Close() })
	go a.read()
	shown := a.sees(func(s string) bool {
		return strings.Contains(s, "│ ⏸ review") && strings.Contains(s, "✓ Delivery")
	})
	if strings.Index(shown, "✓ Delivery") >= strings.Index(shown, "│ ⏸ review") {
		t.Fatalf("review is not below Delivery:\n%s", shown)
	}
	t.Logf("atm attach at review:\n%s", shown)
	writeFixture(t, filepath.Join(nm, "done"), "")
	a.exits()
}

func ownDeliveryInitErrors(t *testing.T, bin string) {
	t.Helper()
	for _, blocked := range []string{"scripts is a file", "scripts is read-only"} {
		t.Run(blocked, func(t *testing.T) {
			top := t.TempDir()
			gitT(t, top, "init", "-q")
			path := filepath.Join(top, "scripts")
			if blocked == "scripts is a file" {
				writeFixture(t, path, "keep this file\n")
			} else {
				if err := os.Mkdir(path, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
			}
			cmd := exec.Command(bin, "init")
			cmd.Dir = top
			if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "scripts") {
				t.Fatalf("atm init with %s = %q, %v", blocked, out, err)
			}
		})
	}
}
