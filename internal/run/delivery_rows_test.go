package run

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var deliveryParts = []string{"commit", "command"}

func deliveryRowsRun(t *testing.T) (*watched, obj, string) {
	t.Helper()
	root := repo(t, t.TempDir(), atmSet(atmYAML, "delivery", "echo delivery-output-once"))
	t.Setenv("FAKE_AGENT_CALL", filepath.Join(t.TempDir(), "call.json"))
	var w watched
	var terminal bytes.Buffer
	if err := Run("t", []string{issueFile(t, issue)}, &w, &terminal); err != nil {
		t.Fatal(err)
	}
	return &w, readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json")), terminal.String()
}

func TestDeliveryEmitsARowPerPart(t *testing.T) {
	w, _, _ := deliveryRowsRun(t)
	var got, want [][2]string
	for _, name := range deliveryParts {
		want = append(want, [2]string{name, "started"}, [2]string{name, "passed"})
	}
	for _, ev := range w.live {
		name, _ := ev["check"].(string)
		if ev["step"] != "delivery" || name == "" || ev["state"] == nil && ev["output"] != nil {
			continue
		}
		state, _ := ev["state"].(string)
		got = append(got, [2]string{name, state})
		if state != "started" {
			if ms, ok := ev["duration_ms"].(float64); !ok || ms < 0 {
				t.Errorf("row has no duration: %v", ev)
			}
		}
		if ev["output"] != nil {
			t.Errorf("state row repeated command output: %v", ev)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delivery rows = %v, want %v", got, want)
	}
}

func TestDeliveryRowsSurviveInReportJSON(t *testing.T) {
	_, report, _ := deliveryRowsRun(t)
	assertDeliveryRows(t, report)
}

func assertDeliveryRows(t *testing.T, report obj) {
	t.Helper()
	rows, _ := reportNode(t, report, "delivery")["rows"].([]any)
	var names []string
	for _, value := range rows {
		r := value.(map[string]any)
		name, _ := r["name"].(string)
		names = append(names, name)
		ms, ok := r["duration_ms"].(float64)
		if r["result"] != "passed" || !ok || ms < 0 {
			t.Errorf("saved row = %v, want passed and a duration", r)
		}
	}
	if !reflect.DeepEqual(names, deliveryParts) {
		t.Fatalf("saved delivery rows = %v, want %v", names, deliveryParts)
	}
	count := 0
	for _, value := range report["commands"].([]any) {
		command := value.(map[string]any)
		if command["name"] == "delivery" {
			count++
			if command["command"] != "echo delivery-output-once" || command["result"] != "passed" ||
				!strings.Contains(command["tail"].(string), "delivery-output-once") {
				t.Errorf("delivery command changed: %v", command)
			}
		}
	}
	if count != 1 {
		t.Fatalf("delivery command entries = %d, want 1", count)
	}
}

func TestDeliveryCommandOutputIsNotDuplicated(t *testing.T) {
	w, _, terminal := deliveryRowsRun(t)
	if strings.Count(terminal, "delivery-output-once") != 1 {
		t.Fatalf("command output must reach terminal once: %q", terminal)
	}
	outputs := 0
	for _, ev := range w.live {
		if output, _ := ev["output"].(string); strings.Contains(output, "delivery-output-once") {
			outputs++
			if ev["check"] != "delivery" || ev["state"] != nil {
				t.Fatalf("command sub-row duplicates output: %v", ev)
			}
		}
	}
	if outputs != 1 {
		t.Fatalf("plain screen output events = %d, want 1", outputs)
	}
}

func TestSkippedDeliveryHasNoSavedRows(t *testing.T) {
	root := repo(t, t.TempDir(), atmYAML)
	var w watched
	if err := Run("t", []string{issueFile(t, issue)}, &w, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	report := readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json"))
	if n := reportNode(t, report, "delivery"); n["result"] != "skipped" || n["rows"] != nil {
		t.Fatalf("skipped delivery = %v", n)
	}
	for _, ev := range w.live {
		if ev["step"] == "delivery" && ev["check"] != nil {
			t.Fatalf("skipped delivery emitted a row: %v", ev)
		}
	}
}
