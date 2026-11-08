//go:build unix

package run

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A rejected cut restores build files in one pipeline row, with every path kept in the run.
func TestRealPonytailRestorationIsOneRow(t *testing.T) {
	builtATM(t)
	for _, count := range []int{1000, 0} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			t.Setenv("FAKE_AGENT_WORK", fixWork+`; mkdir -p .tmp/go-build;
for n in $(seq 1 1001); do printf before > .tmp/go-build/$(printf '%04d' "$n"); done`)
			t.Setenv("FAKE_PONYTAIL_WORK", fmt.Sprintf(
				`n=1; while [ "$n" -le %d ]; do printf changed > .tmp/go-build/$(printf '%%04d' "$n"); n=$((n+1)); done`, count))
			t.Setenv("FAKE_PONYTAIL_REPORT", `{"findings": [], "summary": "No findings."}`)
			cfg := atmSet(atmYAML, "delivery", `for f in .tmp/go-build/*; do
test "$(cat "$f")" = before || exit 1; done; test "$(ls .tmp/go-build | wc -l)" -eq 1001`)
			top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
			bin := versionServer(t, top, "follow-test")
			startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue)).exit(0)
			runs, err := RecordedRuns(top)
			if err != nil || len(runs) != 1 || runs[0].Outcome != "passed" {
				t.Fatalf("want one passed run, got %+v, %v", runs, err)
			}
			var paths []string
			for n := 1; n <= count; n++ {
				paths = append(paths, fmt.Sprintf(".tmp/go-build/%04d", n))
			}
			assertRestorePipeline(t, coldDeliveryAttach(t, bin, top, runs[0].Run), count)
			assertRestorePaths(t, top, runs[0].Run, paths)
		})
	}
}

func assertRestorePipeline(t *testing.T, shown string, count int) {
	t.Helper()
	start := strings.LastIndex(shown, "╭─ ATM")
	if start < 0 {
		t.Fatalf("missing pipeline box:\n%s", shown)
	}
	left, _, _ := strings.Cut(shown[start:], "╰")
	want := fmt.Sprintf("✓ put back %d files", count)
	rows := 0
	if count > 0 {
		rows = 1
	}
	if strings.Count(left, "put back ") != rows || count > 0 && !strings.Contains(left, want) ||
		strings.Contains(left, ".tmp/go-build/") {
		t.Fatalf("want %d restoration rows, %q and no single path:\n%s", rows, want, left)
	}
	previous := -1
	for _, step := range []string{"✓ Issue", "✓ Clone", "✓ Contract", "✓ Agent", "✓ Checks", "✓ Slop detector"} {
		at := strings.Index(left, step)
		if at <= previous {
			t.Fatalf("pipeline step %q is missing or out of order:\n%s", step, left)
		}
		previous = at
	}
	if delivery := strings.Index(left, "✓ Delivery"); delivery <= previous ||
		count > 0 && (strings.Index(left, want) <= previous || strings.Index(left, want) >= delivery) {
		t.Fatalf("restoration must appear under ponytail with Delivery visible:\n%s", left)
	}
}

func assertRestorePaths(t *testing.T, top, label string, want []string) {
	t.Helper()
	n := reportNode(t, readReport(t, filepath.Join(runDir(top, label), "report.json")), "ponytail")
	if n["kept"] != false {
		t.Fatalf("want a rejected cut: %v", n)
	}
	var paths []string
	for _, f := range n["rejected_files"].([]any) {
		file := f.(map[string]any)
		paths = append(paths, file["path"].(string))
		if file["reason"] != n["reason"] {
			t.Fatalf("restoration lost its rejection reason: %v", file)
		}
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("report lost restored paths: got %v, want %v", paths, want)
	}
	assertRestoreEvents(t, top, label, len(want))
}

func assertRestoreEvents(t *testing.T, top, label string, count int) {
	t.Helper()
	rows := 0
	_, err := Follow(top, label, nil, nil, func(f Frame) {
		var ev map[string]any
		if len(f.Event) == 0 || json.Unmarshal(f.Event, &ev) != nil {
			return
		}
		check, _ := ev["check"].(string)
		if !strings.HasPrefix(check, "put back ") {
			return
		}
		rows++
		if ev["step"] != "ponytail" || ev["state"] != "passed" || check != fmt.Sprintf("put back %d files", count) {
			t.Errorf("restoration event lost its step, state or count: %+v", ev)
		}
		if _, ok := ev["files"]; ok {
			t.Errorf("restored paths belong in report.json: %+v", ev)
		}
	})
	if err != nil || count == 0 && rows != 0 || count > 0 && rows != 1 {
		t.Fatalf("want one event only when files were restored, got %d, %v", rows, err)
	}
}
