//go:build unix

package run

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// The JSON stream from the real binary is the interface a following client consumes.
func TestRunFollowOutputNamesItsCommand(t *testing.T) {
	bin := builtATM(t)
	cfg := atmYAML
	for _, kv := range [][2]string{{"install", "echo out-install"}, {"test", "echo out-test; sh a_test.sh"},
		{"typecheck", "echo out-typecheck"}, {"lint", "echo out-lint"},
		{"test_file", "echo out-red/green; sh {file}"}, {"delivery", "echo out-delivery"}} {
		cfg = atmSet(cfg, kv[0], kv[1])
	}
	top := repo(t, t.TempDir(), cfg)
	stopServing(t, top)
	t.Setenv("FAKE_AGENT_CALL", filepath.Join(t.TempDir(), "call.json"))
	cmd := exec.Command(bin, "run", "--follow", issueFile(t, issue))
	cmd.Dir = top
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("atm run --follow: %v; stderr: %s; stdout: %s", err, &stderr, out)
	}
	want := map[string]string{
		"out-install": "install", "out-test": "test",
		"out-typecheck": "typecheck", "out-lint": "lint", "out-delivery": "delivery",
	}
	seen := map[string]bool{}
	var evidence bytes.Buffer
	var deliveryRows []string
	deliveryOutputs := 0
	for _, ev := range events(t, bytes.NewReader(out)) {
		if ev["check"] == "red/green" {
			t.Fatalf("removed proof emitted: %v", ev)
		}
		if row := checkFollowDeliveryRow(t, ev, &evidence); row != "" {
			deliveryRows = append(deliveryRows, row)
		}
		if checkFollowOutput(t, ev, want, seen, &evidence) {
			deliveryOutputs++
		}
	}
	for output := range want {
		if !seen[output] {
			t.Errorf("missing output %q in --follow stream", output)
		}
	}
	if deliveryOutputs != 1 || !slices.Equal(deliveryRows,
		[]string{"commit:started", "commit:passed", "command:started", "command:passed"}) {
		t.Errorf("delivery output count %d, rows %v", deliveryOutputs, deliveryRows)
	}
	if path := os.Getenv("ATM_OUTPUT_EVIDENCE"); path != "" {
		if err := os.WriteFile(path, evidence.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func checkFollowDeliveryRow(t *testing.T, ev map[string]any, evidence *bytes.Buffer) string {
	if ev["step"] == "delivery" && ev["state"] != nil && ev["check"] != nil {
		if ev["state"] != "started" {
			if ms, ok := ev["duration_ms"].(float64); !ok || ms < 0 {
				t.Errorf("delivery row has no duration: %v", ev)
			}
		}
		b, _ := json.Marshal(ev)
		evidence.Write(append(b, '\n'))
		return ev["check"].(string) + ":" + ev["state"].(string)
	}
	return ""
}

func checkFollowOutput(
	t *testing.T, ev map[string]any, want map[string]string, seen map[string]bool, evidence *bytes.Buffer,
) bool {
	output, ok := ev["output"].(string)
	if !ok || ev["step"] != "checks" && ev["step"] != "delivery" {
		return false
	}
	check, _ := ev["check"].(string)
	if check == "" || want[output] != "" && check != want[output] {
		t.Errorf("output %q names %q, want %q: %v", output, check, want[output], ev)
	}
	if want[output] != "" {
		seen[output] = true
		b, _ := json.Marshal(ev)
		evidence.Write(append(b, '\n'))
		return output == "out-delivery"
	}
	return false
}
