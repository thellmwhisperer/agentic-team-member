//go:build unix

package tui

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func TestGateJumpNamesTheFailedNodeAndOmitsUnknownStarts(t *testing.T) {
	root, bin := gateRepo(t)
	snaps := filepath.Join(bin, "snaps")
	if err := os.Mkdir(snaps, 0o700); err != nil {
		t.Fatal(err)
	}
	writeGateSnaps(t, snaps)
	t.Setenv("ATM_GATE_DIR", snaps)

	issue := filepath.Join(bin, "issue.md")
	if err := os.WriteFile(issue, []byte("# Chore\nType: chore\nDo it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- run.Run("t", []string{issue}, &out, io.Discard) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("run passed; want Document failed\n%s", out.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("run did not finish")
	}
	events := gateEvents(t, &out)
	checkGateJump(t, events)
	report := readGateReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
	checkGateStarts(t, report)
}

func gateEvents(t *testing.T, out *bytes.Buffer) []json.RawMessage {
	t.Helper()
	var evs []json.RawMessage
	dec := json.NewDecoder(out)
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		evs = append(evs, raw)
	}
	return evs
}

func checkGateJump(t *testing.T, evs []json.RawMessage) {
	t.Helper()
	var running, documentRunning bool
	for _, raw := range evs {
		var ev struct{ Step, State string }
		if json.Unmarshal(raw, &ev) != nil {
			continue
		}
		running = running || ev.Step == "test" && ev.State == "running"
		documentRunning = documentRunning || ev.Step == "document" && ev.State == "running"
	}
	if !running || documentRunning {
		t.Fatalf("want Test running then Document failed, never running; events %s", evs)
	}
}

func checkGateStarts(t *testing.T, report map[string]any) {
	t.Helper()
	if report["failed_node"] != "document" {
		t.Fatalf("failed_node = %v, want document", report["failed_node"])
	}
	want := map[string]struct {
		result string
		ms     float64
	}{
		"review": {"passed", 100}, "test": {"passed", 80},
		"document": {"failed", 12}, "lint": {"skipped", 0},
		"push": {"passed", 1}, "pr": {"pending", 0}, "ci": {"pending", 0},
	}
	for name, expect := range want {
		n := gateNode(t, report, name)
		if got, ok := n["started_at"]; ok {
			t.Fatalf("node %s invented started_at %v", name, got)
		}
		if n["result"] != expect.result {
			t.Fatalf("node %s result = %v, want %s", name, n["result"], expect.result)
		}
		if n["duration_ms"] != expect.ms {
			t.Fatalf("node %s duration_ms = %v, want %v", name, n["duration_ms"], expect.ms)
		}
	}
	if gateNode(t, report, "lint")["result"] != "skipped" || gateNode(t, report, "push")["result"] != "passed" {
		t.Fatalf("lint and push = %v %v", gateNode(t, report, "lint"), gateNode(t, report, "push"))
	}
}

func gateNode(t *testing.T, report map[string]any, name string) map[string]any {
	t.Helper()
	for _, raw := range report["nodes"].([]any) {
		if n := raw.(map[string]any); n["name"] == name {
			return n
		}
	}
	t.Fatalf("no node %s", name)
	return nil
}

func readGateReport(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func writeGateSnaps(t *testing.T, dir string) {
	t.Helper()
	header := "steps[7]{step,status,findings,duration_ms}:\n"
	running := header + "  review,completed,0,100\n" +
		"  test,fixing,0,50\n" +
		"  document,pending,0,0\n  lint,pending,0,0\n  push,pending,0,0\n  pr,pending,0,0\n  ci,pending,0,0\n"
	failed := header + "  review,completed,0,100\n" +
		"  test,completed,0,80\n" +
		"  document,failed,1,12\n" +
		"  lint,skipped,0,0\n" +
		"  push,completed,0,1\n  pr,pending,0,0\n  ci,pending,0,0\n"
	for name, text := range map[string]string{"running": running, "failed": failed} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func gateRepo(t *testing.T) (string, string) {
	t.Helper()
	dir, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	for _, kv := range []string{"GIT_AUTHOR_NAME=atm", "GIT_AUTHOR_EMAIL=atm@example.com",
		"GIT_COMMITTER_NAME=atm", "GIT_COMMITTER_EMAIL=atm@example.com"} {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeGateFakes(t, bin)
	cmd := exec.Command("sh", "-c", gateRepoScript)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("repo: %v\n%s", err, out)
	}
	t.Chdir(dir)
	return dir, bin
}

const gateRepoScript = `export GIT_AUTHOR_NAME=atm GIT_AUTHOR_EMAIL=atm@example.com
export GIT_COMMITTER_NAME=atm GIT_COMMITTER_EMAIL=atm@example.com
git init -q -b main
printf a > a.txt
git add a.txt
git commit -q -m init
git remote add origin https://github.com/o/r.git
cat > .atm.yaml << 'EOF'
harness: codex
install: ""
test: ""
typecheck: ""
lint: ""
test_file: ""
test_patterns: []
docs_patterns: []
delivery: no-mistakes axi run
EOF
`

func writeGateFakes(t *testing.T, bin string) {
	t.Helper()
	for _, pair := range [][2]string{
		{"gate-nm.sh", "no-mistakes"}, {"gate-codex.sh", "codex"},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", pair[0]))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, pair[1]), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}
