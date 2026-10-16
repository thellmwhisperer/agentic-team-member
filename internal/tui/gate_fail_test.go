//go:build unix

package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// A gate poll can jump from Test running to Document failed. herdr must name Document, and report.json must
// keep no-mistakes' start times instead of the time ATM noticed the snapshot.
func TestGateJumpNamesTheFailedNodeAndKeepsItsStart(t *testing.T) {
	root, bin := gateRepo(t)
	snaps := filepath.Join(bin, "snaps")
	if err := os.Mkdir(snaps, 0o700); err != nil {
		t.Fatal(err)
	}
	writeGateSnaps(t, snaps)
	t.Setenv("ATM_GATE_DIR", snaps)
	calls := filepath.Join(bin, "herdr-log")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "wX:p1")
	t.Setenv("HERDR_LOG", calls)
	t.Setenv("HERDR_BIN_PATH", filepath.Join(bin, "herdr"))

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
	h := openHerdr("t", "startup")
	for _, ev := range events {
		h.frame(run.Frame{Event: ev})
	}
	h.end(run.Outcome{Outcome: "failed", FailedNode: fmt.Sprint(report["failed_node"]), Ended: time.Now()}, nil)
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "idle") || !strings.Contains(string(b), "failed at Document") ||
		strings.Contains(string(b), "failed at Test") {
		t.Fatalf("herdr named the wrong node:\n%s", b)
	}
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
	want := map[string]string{
		"review": "2020-01-02T03:04:05.006Z", "test": "2020-01-02T03:04:06.007Z",
		"document": "2020-01-02T03:04:07.008Z",
	}
	for _, raw := range report["nodes"].([]any) {
		n := raw.(map[string]any)
		name, _ := n["name"].(string)
		got, ok := n["started_at"].(string)
		if expect, named := want[name]; named {
			if !ok || got != expect {
				t.Fatalf("node %s started_at = %v, want %s", name, n["started_at"], expect)
			}
			continue
		}
		if (name == "lint" || name == "push") && ok {
			t.Fatalf("node %s invented started_at %v", name, got)
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
	header := "steps[7]{step,status,findings,duration_ms,started_at}:\n"
	running := header + "  review,completed,0,100,2020-01-02T03:04:05.006Z\n" +
		"  test,fixing,0,50,2020-01-02T03:04:06.007Z\n" +
		"  document,pending,0,0,\n  lint,pending,0,0,\n  push,pending,0,0,\n  pr,pending,0,0,\n  ci,pending,0,0,\n"
	failed := header + "  review,completed,0,100,2020-01-02T03:04:05.006Z\n" +
		"  test,completed,0,80,2020-01-02T03:04:06.007Z\n" +
		"  document,failed,1,12,2020-01-02T03:04:07.008Z\n" +
		"  lint,skipped,0,0,2020-01-02T03:04:08.009Z\n" +
		"  push,completed,0,1,\n  pr,pending,0,0,\n  ci,pending,0,0,\n"
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
		{"gate-herdr.sh", "herdr"}, {"gate-nm.sh", "no-mistakes"}, {"gate-codex.sh", "codex"},
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
