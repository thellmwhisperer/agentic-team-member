//go:build unix

package run

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"gopkg.in/yaml.v3"
)

// childTest is a Go test that builds and runs another binary, child, whose sources go test does not track.
const childTest = "package p\n\nimport (\n\t\"os/exec\"\n\t\"testing\"\n)\n\nfunc TestChild(t *testing.T) {\n" +
	"\tif out, err := exec.Command(\"go\", \"run\", \"../child\").Output(); string(out) != \"fixed\" {\n" +
	"\t\tt.Fatalf(\"child printed %q: %v\", out, err)\n\t}\n}\n"

// A real fix run under ATM's own test_file proves on base files that do not build: the child binary of its test
// is rebuilt each time, so the green go test kept for the fixed sources is never reused.
func TestOwnProofDoesNotReuseCachedResult(t *testing.T) {
	bin := builtATM(t)
	keepGoCache(t)
	_, source, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../.atm.yaml"))
	var own config.Config
	if err == nil {
		err = yaml.Unmarshal(b, &own)
	}
	if err != nil || own.TestFile == "" {
		t.Fatalf("want ATM's .atm.yaml with a test_file, got %q, %v", own.TestFile, err)
	}
	atm := strings.Replace(atmSet(atmYAML, "test_file", own.TestFile), `"**/*_test.sh"`, `"**/*_test.go"`, 1)
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", atm), "rev-parse", "--show-toplevel")
	stopServing(t, top)
	value := "package main\n\nfunc value() string { return \"%s\" }\n"
	commitFiles(t, top, "", map[string]string{"go.mod": "module example.com/m\n\ngo 1.21\n",
		"child/main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Print(value()) }\n",
		"child/value.go": strings.Replace(value, "%s", "broken", 1)})
	gitT(t, top, "update-ref", "refs/remotes/origin/main", "main")
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATM_TEST_CHILD", childTest)
	t.Setenv("ATM_TEST_VALUE", value)
	t.Setenv("FAKE_AGENT_WORK", `mkdir -p p; printf %s "$ATM_TEST_CHILD" > p/p_test.go; `+
		`printf "$ATM_TEST_VALUE" fixed > child/value.go; `+reportJSON("p/p_test.go"))
	startFollow(t, bin, top, false, "run", "--follow", issueFile(t, "# Retry\nType: fix\nRetry once.\n")).exit(0)
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) != 1 || runs[0].Outcome != "passed" {
		t.Fatalf("want the fix proved on base files that do not build, got %+v, %v", runs, err)
	}
}
