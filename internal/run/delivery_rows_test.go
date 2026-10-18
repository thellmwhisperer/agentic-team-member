package run

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var deliveryParts = []string{"commit", "review", "repair 1", "review 2", "command"}

func deliveryRowsRun(t *testing.T) (*watched, obj, string) {
	t.Helper()
	blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 never retries", nil)
	repairEnv(t, []string{blocked, slicingJSON(t, slicingReport())}, []string{repairGrow})
	root := repo(t, t.TempDir(), atmSet(repairATM(), "delivery", "echo delivery-output-once"))
	t.Setenv("FAKE_AGENT_CALL", filepath.Join(t.TempDir(), "call.json"))
	var w watched
	var terminal bytes.Buffer
	if err := Run("t", []string{issueFile(t, reviewIssue)}, &w, &terminal); err != nil {
		t.Fatal(err)
	}
	return &w, readReport(t, filepath.Join(root, ".atm", "runs", "t", "report.json")), terminal.String()
}

func TestDeliveryEmitsARowPerPart(t *testing.T) {
	w, _, _ := deliveryRowsRun(t)
	var got, want [][2]string
	for _, name := range deliveryParts {
		result := "passed"
		if name == "review" {
			result = "failed"
		}
		want = append(want, [2]string{name, "started"}, [2]string{name, result})
	}
	for _, ev := range w.live {
		name, _ := ev["check"].(string)
		if ev["step"] != "delivery" || name == "" {
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
		result := "passed"
		if name == "review" {
			result = "failed"
		}
		ms, ok := r["duration_ms"].(float64)
		if r["result"] != result || !ok || ms < 0 {
			t.Errorf("saved row = %v, want %s and a duration", r, result)
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
			if ev["check"] != nil {
				t.Fatalf("command sub-row duplicates output: %v", ev)
			}
		}
	}
	if outputs != 1 {
		t.Fatalf("plain screen output events = %d, want 1", outputs)
	}
}

func TestDeliveryRowsKeepBothRepairRoundsAndFailureReason(t *testing.T) {
	for _, approves := range []bool{true, false} {
		t.Run(map[bool]string{true: "approved", false: "blocked"}[approves], func(t *testing.T) {
			blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 never retries", nil)
			last := blocked
			if approves {
				last = slicingJSON(t, slicingReport())
			}
			repairEnv(t, []string{blocked, blocked, last}, []string{repairGrow, repairGrow + "; echo retry >> a.txt"})
			dir, _, _, _, err := reviewRun(t, repairATM())
			if (err == nil) != approves {
				t.Fatalf("approved=%t, delivery error=%v", approves, err)
			}
			report := readReport(t, filepath.Join(dir, "report.json"))
			rows, _ := reportNode(t, report, "delivery")["rows"].([]any)
			want := []string{"commit", "review", "repair 1", "review 2", "repair 2", "review 3"}
			if approves {
				want = append(want, "command")
			}
			var names []string
			for _, value := range rows {
				r := value.(map[string]any)
				name := r["name"].(string)
				names = append(names, name)
				result := "passed"
				if name == "review" || name == "review 2" || name == "review 3" && !approves {
					result = "failed"
				}
				if r["result"] != result {
					t.Errorf("row %s = %v, want %s", name, r["result"], result)
				}
			}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("rows = %v, want %v", names, want)
			}
			if !approves && (report["failed_node"] != "delivery" || report["reason"] != err.Error()) {
				t.Fatalf("delivery reason changed: %v, error %v", report, err)
			}
		})
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
