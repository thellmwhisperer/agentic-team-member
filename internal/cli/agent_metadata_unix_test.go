//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func waitForAgents(t *testing.T, ready string, count int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		entries, err := os.ReadDir(ready)
		if err == nil && len(entries) >= count {
			return
		}
	}
	entries, _ := os.ReadDir(ready)
	t.Fatalf("only %d fake agents reached the barrier, want %d", len(entries), count)
}

func TestAgentMetadataCLILiveAndFinished(t *testing.T) {
	setupConcurrentRunRepo(t)
	ready, release := setupBarrierAgent(t)

	label, err := atm(t, "run", "--model", "m1", "retry.md")
	if err != nil {
		t.Fatal(err)
	}
	waitForAgents(t, ready, 1)
	check := func(command string) {
		t.Helper()
		for _, args := range [][]string{{command}, {command, "--json"}, {"axi", command}, {"axi", command, "--json"}} {
			out, err := atm(t, args...)
			if err != nil {
				t.Fatalf("%v: %v", args, err)
			}
			if !strings.Contains(out, "codex m1") {
				t.Errorf("%v lacks agent: %s", args, out)
			}
			if args[len(args)-1] == "--json" {
				var r struct{ Runs []map[string]string }
				if err := json.Unmarshal([]byte(out), &r); err != nil || len(r.Runs) != 1 || r.Runs[0]["agent"] != "codex m1" {
					t.Errorf("%v: %s, %v", args, out, err)
				}
			}
		}
	}
	check("status")
	check("runs")
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := atm(t, "attach", strings.TrimSpace(label)); err == nil {
		t.Fatal("fake agent should fail")
	}
	check("runs")
}
