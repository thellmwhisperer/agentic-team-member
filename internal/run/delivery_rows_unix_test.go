//go:build unix

package run

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

func TestDeliveryCommandOutputIsNotDuplicatedOnTerminal(t *testing.T) {
	repo(t, t.TempDir(), atmSet(atmYAML, "delivery", "test -t 1 && echo delivery-output-once"))
	r := &bgRun{ttys: 1, size: [2]int{60, 40}, more: make(chan struct{})}
	terminal := &screen{s: &server{}, r: r}
	var w watched
	if err := Run("t", []string{issueFile(t, issue)}, &w, terminal); err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	for _, f := range r.frames {
		raw.Write(f.Raw)
	}
	if strings.Count(raw.String(), "delivery-output-once") != 1 {
		t.Fatalf("attached terminal output = %q, want one copy", raw.String())
	}
	for _, ev := range w.live {
		if ev["check"] == "command" && ev["output"] != nil {
			t.Fatalf("command sub-row duplicated output: %v", ev)
		}
	}
}

func TestDeliveryRowsThroughRealBinaryAndColdAttach(t *testing.T) {
	nm := gateFake(t)
	if err := os.WriteFile(filepath.Join(nm, "none"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := builtATM(t)
	// The real binary's serve binds at git's toplevel. t.TempDir can be another spelling of that
	// directory (Darwin's /var, or any symlink); short() then cannot dial the socket.
	top := gitT(t, repo(t, t.TempDir(), atmSet(atmYAML, "delivery", "echo delivery-output-once")),
		"rev-parse", "--show-toplevel")
	t.Setenv("FAKE_AGENT_CALL", filepath.Join(t.TempDir(), "call.json"))
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := startVersionServe(t, bin, top)
	out, err := versionLaunch(bin, top, issueFile(t, issue))
	if err != nil {
		t.Fatalf("atm run: %v\n%s", err, out)
	}
	label := strings.TrimSpace(string(out))
	var emitted bytes.Buffer
	end, err := Follow(top, label, nil, nil, func(f Frame) {
		if len(f.Event) > 0 {
			emitted.Write(f.Event)
			emitted.WriteByte('\n')
		}
	})
	if err != nil || end.Outcome != "passed" {
		t.Fatalf("delivery: %+v, %v", end, err)
	}
	reportPath := filepath.Join(top, ".atm", "runs", label, "report.json")
	report := readReport(t, reportPath)
	assertDeliveryRows(t, report)
	if _, ok := report["gate"]; ok {
		t.Fatalf("atm run saved a gate field: %v", report["gate"])
	}
	for _, node := range report["nodes"].([]any) {
		if node.(map[string]any)["name"] == "gate" {
			t.Fatal("atm run saved a gate node")
		}
	}
	for _, line := range bytes.Split(bytes.TrimSpace(emitted.Bytes()), []byte{'\n'}) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("run event: %v: %s", err, line)
		}
		if event["step"] == "gate" {
			t.Fatalf("atm run emitted a gate event: %s", line)
		}
	}
	if dir := os.Getenv("ATM_GATE_EVIDENCE_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "atm-run-events.jsonl"), emitted.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "atm-run-report.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Let the real server exit normally, flushing coverage and discarding its replay frames.
	endDeliveryServer(t, server, top)
	server = startVersionServe(t, bin, top)
	shown := coldDeliveryAttach(t, bin, top, label)
	for _, name := range deliveryParts {
		if !strings.Contains(shown, "✓ "+name+"  ") {
			t.Errorf("atm attach lost %s:\n%s", name, shown)
		}
	}
	if !strings.Contains(shown, "✓ Delivery") {
		t.Fatalf("atm attach lost Delivery:\n%s", shown)
	}
	endDeliveryServer(t, server, top)
}

func endDeliveryServer(t *testing.T, server *exec.Cmd, top string) {
	t.Helper()
	if err := os.Remove(socket(top)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		_ = server.Process.Kill()
		<-done
		t.Fatal("server did not exit after removing its socket")
	}
}

func coldDeliveryAttach(t *testing.T, bin, top, label string) string {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	cmd := exec.Command(bin, "attach", label)
	cmd.Dir = top
	p, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 60, Rows: 50})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	read := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		b := make([]byte, 4096)
		closed := false
		for {
			n, err := p.Read(b)
			out.Write(b[:n])
			if !closed && strings.Contains(ansi.Strip(out.String()), "q close") {
				closed = true
				_, _ = p.Write([]byte("q"))
			}
			if err != nil {
				read <- ansi.Strip(out.String())
				return
			}
		}
	}()
	select {
	case shown := <-read:
		if err := cmd.Wait(); err != nil {
			t.Fatalf("atm attach: %v\n%s", err, shown)
		}
		return shown
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = p.Close()
		t.Fatalf("atm attach did not complete:\n%s", <-read)
		return ""
	}
}
