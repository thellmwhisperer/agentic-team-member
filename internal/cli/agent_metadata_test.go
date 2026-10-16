package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

const metadataHistory = `{"run":"selected","outcome":"passed","agent":` +
	`{"harness":"claude","model":"m1","effort":"high","harness_args":["--x"]}}
{"run":"defaulted","outcome":"passed","agent":{"harness":"pi","model":"","effort":"","harness_args":[]}}
{"run":"legacy","outcome":"passed"}
`

func TestAgentMetadataCLIHistory(t *testing.T) {
	dir := repo(t, t.TempDir())
	cleanBackgroundServer(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".atm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".atm", "runs.jsonl"), []byte(metadataHistory), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"runs"}, {"runs", "--json"}, {"axi", "runs"}, {"axi", "runs", "--json"}} {
		out, err := atm(t, args...)
		if err != nil {
			t.Errorf("%v: %v", args, err)
			if log, err := os.ReadFile(filepath.Join(dir, ".atm", "serve.log")); err == nil {
				t.Log(string(log))
			}
			continue
		}
		checkMetadataListing(t, out, args[len(args)-1] == "--json")
	}
}

func TestAgentMetadataAxiLiveAndFinished(t *testing.T) {
	for _, going := range []bool{false, true} {
		var runs []run.Outcome
		for _, line := range strings.Split(strings.TrimSpace(metadataHistory), "\n") {
			var o run.Outcome
			if err := json.Unmarshal([]byte(line), &o); err != nil {
				t.Fatal(err)
			}
			if going {
				o.Outcome, o.Step = "running", "agent"
			}
			runs = append(runs, o)
		}
		for _, asJSON := range []bool{false, true} {
			var out bytes.Buffer
			if err := list(&out, asJSON, going, runs); err != nil {
				t.Fatal(err)
			}
			checkMetadataListing(t, out.String(), asJSON)
		}
	}
}

func checkMetadataListing(t *testing.T, out string, asJSON bool) {
	t.Helper()
	want := map[string]string{"selected": "claude m1", "defaulted": "pi default", "legacy": "-"}
	if asJSON {
		var result struct{ Runs []map[string]string }
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Runs) != len(want) {
			t.Fatalf("runs = %s", out)
		}
		for _, r := range result.Runs {
			if r["agent"] != want[r["run"]] {
				t.Errorf("run %s agent = %q; want %q", r["run"], r["agent"], want[r["run"]])
			}
		}
		return
	}
	for label, agent := range want {
		found := false
		for _, line := range strings.Split(out, "\n") {
			found = found || strings.Contains(line, label) && strings.Contains(line, agent)
		}
		if !found {
			t.Errorf("missing %s's agent %q:\n%s", label, agent, out)
		}
	}
}
