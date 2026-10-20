//go:build unix

package run

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A serve stamped vA (aaa1111) 2026-10-15 names that version on the run it executes, and listings
// show it last. A run recorded before the field existed shows "-".
func TestServeNamesItsVersionOnTheRunAndInRuns(t *testing.T) {
	const want = "vA (aaa1111) 2026-10-15"
	bin := filepath.Join(t.TempDir(), "atm")
	buildStampedATM(t, bin)
	top := atmRepo(t)
	writeFixture(t, filepath.Join(top, ".atm", "runs.jsonl"),
		`{"outcome":"passed","run":"old-1","issue":"old.md","report":"old-report.json",`+
			`"started":"2026-10-01T00:00:00Z","ended":"2026-10-01T00:01:00Z"}`+"\n")
	wait, release := holdFakeAgents(t, 1)
	startVersionServe(t, bin, top)
	out, err := versionLaunch(bin, top, issueFile(t, issue))
	if err != nil {
		t.Fatalf("launch: %s, %v", out, err)
	}
	label := strings.TrimSpace(string(out))
	wait()
	release()
	checkVersionRun(t, top, label)
	assertServeReport(t, top, label, want)
	assertServeColumn(t, bin, top, label, want)
}

func buildStampedATM(t *testing.T, bin string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	pkg := "github.com/thellmwhisperer/agentic-team-member/internal/buildinfo."
	flags := "-X " + pkg + "Version=vA -X " + pkg + "Commit=aaa1111 -X " + pkg + "Date=2026-10-15"
	cmd := exec.Command("go", "build", "-buildvcs=false", "-ldflags", flags, "-o", bin, "./cmd/atm")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %s, %v", out, err)
	}
}

func assertServeReport(t *testing.T, top, label, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(top, ".atm", "runs", label, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep map[string]any
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep["serve"] != want {
		t.Fatalf("report serve = %v, want %s\n%s", rep["serve"], want, b)
	}
}

func assertServeColumn(t *testing.T, bin, top, label, want string) {
	t.Helper()
	text := stampedAtm(t, bin, top, "runs")
	runsJSON := stampedAtm(t, bin, top, "runs", "--json")
	axi := stampedAtm(t, bin, top, "axi", "runs")
	raw := stampedAtm(t, bin, top, "axi", "runs", "--json")
	if !rowEnds(text, label, want) || !rowEnds(text, "old-1", "-") {
		t.Fatalf("atm runs:\n%s", text)
	}
	if !strings.Contains(runsJSON, `"serve":"`+want+`"}`) || !strings.Contains(runsJSON, `"serve":"-"}`) {
		t.Fatalf("atm runs --json:\n%s", runsJSON)
	}
	if !strings.Contains(axi, ",serve}:\n") || !rowEnds(axi, label, want) || !rowEnds(axi, "old-1", `"-"`) {
		t.Fatalf("atm axi runs:\n%s", axi)
	}
	if !strings.Contains(raw, `"serve":"`+want+`"}`) || !strings.Contains(raw, `"serve":"-"}`) {
		t.Fatalf("atm axi runs --json:\n%s", raw)
	}
}

func stampedAtm(t *testing.T, bin, top string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = top
	out, err := stdout(cmd)
	if err != nil {
		t.Fatalf("atm %v: %s, %v", args, out, err)
	}
	return string(out)
}

func rowEnds(text, label, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, label) {
			return strings.HasSuffix(strings.TrimRight(line, " "), want)
		}
	}
	return false
}
