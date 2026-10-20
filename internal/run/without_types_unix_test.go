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
)

// Exercise the installed command boundary, including saved runs from older ATM versions.
func TestRunWithoutTypesOrRedGreen(t *testing.T) {
	builtATM(t)
	for _, typ := range []string{"", "fix", "hotfix", "feature", "greenfield", "refactor",
		"tests", "docs", "chore", "unknown"} {
		t.Run("type="+typ, func(t *testing.T) {
			runWithoutType(t, typ)
		})
	}
	for _, history := range []string{"intact"} {
		t.Run("suite failure and legacy resume/"+history, func(t *testing.T) { resumeWithoutType(t, history) })
	}
	for _, kind := range []string{"absolute", "parent", "symlink"} {
		t.Run("external test_file/"+kind, func(t *testing.T) {
			externalReportedTest(t, kind)
		})
	}
	t.Run("missing report and no work", emptyUntypedWork)
}

func runWithoutType(t *testing.T, typ string) {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "commands")
	t.Setenv("ATM_TEST_TRACE", trace)
	cfg := atmSet(atmYAML, "test_file", `echo forbidden {file} >> "$ATM_TEST_TRACE"; exit 73`)
	for _, name := range []string{"test", "typecheck", "lint", "delivery"} {
		cfg = atmSet(cfg, name, `echo `+name+` >> "$ATM_TEST_TRACE"; grep -q fixed a.txt`)
	}
	cfg += "e2e: 'echo e2e >> \"$ATM_TEST_TRACE\"; grep -q fixed a.txt'\n"
	top, bin := untypedRepo(t, cfg)
	work, test, want := fixWork, "a_test.sh", "test\ne2e\ntypecheck\nlint\ndelivery\n"
	if typ == "" || typ == "unknown" {
		work = "printf 'fixed\\nextra\\nmore\\n' > a.txt" + fixTest
		t.Setenv("FAKE_PONYTAIL_REPORT", cut)
		t.Setenv("FAKE_PONYTAIL_WORK", "echo fixed > a.txt")
		want = "test\ne2e\ntypecheck\nlint\n" + want
	}
	if typ == "unknown" {
		test = ""
	}
	t.Setenv("FAKE_AGENT_WORK", work+"; "+reportJSON(test))
	body := "# Retry\nRetry once.\n"
	if typ != "" {
		body += "Type: " + typ + "\n"
	}
	serveUntyped(t, bin, top)
	out, err := exec.Command(bin, "run", "--follow", issueFile(t, body)).CombinedOutput()
	if err != nil {
		t.Fatalf("untyped checks failed: %v\n%s", err, out)
	}
	b, err := os.ReadFile(trace)
	if err != nil || string(b) != want {
		t.Fatalf("want only configured checks on the result, got %q, %v", b, err)
	}
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %v, %v; output: %s", runs, err, out)
	}
	rep := readReport(t, runs[0].Report)
	assertNoTaskProof(t, rep)
	brief, err := os.ReadFile(filepath.Join(filepath.Dir(runs[0].Report), "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"Task type:", "ATM runs the red test", "suite passes before and after"} {
		if bytes.Contains(brief, []byte(removed)) {
			t.Fatalf("contract retains %q: %s", removed, brief)
		}
	}
	if !strings.Contains(string(out), `"step":"ponytail"`) {
		t.Fatalf("ponytail missing: %s", out)
	}
}

func resumeWithoutType(t *testing.T, history string) {
	t.Helper()
	trace, ready := filepath.Join(t.TempDir(), "commands"), filepath.Join(t.TempDir(), "ready")
	t.Setenv("ATM_TEST_TRACE", trace)
	t.Setenv("ATM_TEST_READY", ready)
	cfg := atmSet(atmYAML, "test", `echo test >> "$ATM_TEST_TRACE"; grep -q fixed a.txt && test -f "$ATM_TEST_READY"`)
	cfg = atmSet(cfg, "test_file", `echo forbidden {file} >> "$ATM_TEST_TRACE"; exit 73`)
	cfg = atmSet(cfg, "delivery", `echo delivery >> "$ATM_TEST_TRACE"`)
	top, bin := untypedRepo(t, cfg)
	t.Setenv("FAKE_AGENT_WORK", "printf 'fixed\\nextra\\nmore\\n' > a.txt"+fixTest+"; "+reportJSON("a_test.sh"))
	t.Setenv("FAKE_PONYTAIL_WORK", "echo fixed > a.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	serveUntyped(t, bin, top)
	out, err := exec.Command(bin, "run", "--follow", issueFile(t, issue)).CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("test:")) {
		t.Fatalf("suite failure not enforced: %v, %s", err, out)
	}
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %v, %v; output: %s", runs, err, out)
	}
	b, err := os.ReadFile(trace)
	if err != nil || string(b) != "test\n" {
		t.Fatalf("failed suite delivered or ran proof: %q, %v", b, err)
	}
	saved := filepath.Join(filepath.Dir(runs[0].Report), "resume", "resume.json")
	want, reason := legacyResume(t, saved, history)
	writeFixture(t, ready, "ready")
	out, err = exec.Command(bin, "run", "--follow", "--resume", runs[0].Run).CombinedOutput()
	if err != nil {
		t.Fatalf("legacy resume: %v, %s", err, out)
	}
	b, err = os.ReadFile(trace)
	if err != nil || string(b) != want {
		t.Fatalf("resume ran type proof: %q, %v", b, err)
	}
	if reason != "" && !bytes.Contains(out, []byte(reason)) {
		t.Fatalf("history guard not enforced: %s", out)
	}
	if bytes.Contains(out, []byte(`"red":`)) || bytes.Contains(out, []byte(`"green":`)) {
		t.Fatalf("legacy trial restored: %s", out)
	}
}

func externalReportedTest(t *testing.T, kind string) {
	t.Helper()
	sentinel := filepath.Join(t.TempDir(), "sentinel_test.sh")
	writeFixture(t, sentinel, "innocuous external sentinel\n")
	top, bin := untypedRepo(t, atmYAML)
	name := sentinel
	work := fixWork
	if kind == "parent" {
		var err error
		name, err = filepath.Rel(filepath.Join(top, ".atm", "clones", "placeholder"), sentinel)
		if err != nil {
			t.Fatal(err)
		}
	}
	if kind == "symlink" {
		name = "outside_test.sh"
		t.Setenv("ATM_TEST_SENTINEL", sentinel)
		work += `; ln -s "$ATM_TEST_SENTINEL" outside_test.sh`
	}
	t.Setenv("FAKE_AGENT_WORK", work+"; "+reportJSON(name))
	serveUntyped(t, bin, top)
	out, err := exec.Command(bin, "run", "--follow", issueFile(t, issue)).CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("test_file")) {
		t.Fatalf("external test accepted: %v, %s", err, out)
	}
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %v, %v; output: %s", runs, err, out)
	}
	out, err = exec.Command(bin, "run", "--follow", "--resume", runs[0].Run).CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("test_file")) {
		t.Fatalf("resumed external test accepted: %v, %s", err, out)
	}
	b, err := os.ReadFile(sentinel)
	if err != nil || string(b) != "innocuous external sentinel\n" {
		t.Fatalf("external file changed: %q, %v", b, err)
	}
}

func assertNoTaskProof(t *testing.T, rep map[string]any) {
	t.Helper()
	if _, ok := rep["type"]; ok {
		t.Fatalf("report retains classification: %v", rep)
	}
	for _, key := range []string{"units", "reproofs"} {
		units, _ := rep[key].([]any)
		for _, unit := range units {
			for _, field := range []string{"type", "red", "green"} {
				if _, ok := unit.(map[string]any)[field]; ok {
					t.Fatalf("report retains %s: %v", field, unit)
				}
			}
		}
	}
}

// A separate server exits gracefully, so real-command coverage includes the run itself.
func untypedRepo(t *testing.T, cfg string) (string, string) {
	t.Helper()
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	bin := filepath.Join(t.TempDir(), "atm")
	buildATM(t, bin, "follow-test")
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	return top, bin
}

func serveUntyped(t *testing.T, bin, top string) {
	t.Helper()
	server := startVersionServe(t, bin, top)
	t.Cleanup(func() { endServe(bin, top, server) })
}

func legacyResume(t *testing.T, saved, history string) (string, string) {
	t.Helper()
	var legacy map[string]any
	b, err := os.ReadFile(saved)
	if err != nil || json.Unmarshal(b, &legacy) != nil {
		t.Fatalf("saved run: %s, %v", b, err)
	}
	legacy["original"].(map[string]any)["Type"] = "fix"
	test, content := "a_test.sh", "grep -q fixed a.txt && ! grep -q broken a.txt\n"
	want, reason := "test\ntest\ntest\ndelivery\n", ""
	switch history {
	case "changed":
		content, want, reason = "old test content", "test\ntest\ndelivery\n", "changed since it was recorded"
	case "outside":
		test = filepath.Join(t.TempDir(), "outside_test.sh")
		writeFixture(t, test, "external sentinel\n")
		want, reason = "test\ntest\ndelivery\n", "test_file"
	}
	legacy["proofs"] = [][4]string{{legacy["base"].(string), test, "unknown", content}}
	b, _ = json.Marshal(legacy)
	writeFixture(t, saved, string(b))
	return want, reason
}

func emptyUntypedWork(t *testing.T) {
	top, bin := untypedRepo(t, atmYAML)
	t.Setenv("FAKE_AGENT", "no-report")
	t.Setenv("FAKE_AGENT_WORK", "true")
	serveUntyped(t, bin, top)
	out, err := exec.Command(bin, "run", "--follow", issueFile(t, issue)).CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("no change to prove")) {
		t.Fatalf("missing report delivered empty work: %v, %s", err, out)
	}
}
