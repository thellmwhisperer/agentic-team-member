package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// The persisted nodes from run 297-65, whose daemon no longer had replay frames.
const completedReport = `{"nodes":[
 {"name":"issue","result":"passed","duration_ms":395},
 {"name":"clone","result":"passed","duration_ms":4274},
 {"name":"contract","result":"passed","duration_ms":0},
 {"name":"agent","result":"passed","duration_ms":1318621},
 {"name":"checks","result":"passed","duration_ms":385916},
 {"name":"ponytail","result":"passed","duration_ms":285402},
 {"name":"delivery","result":"passed","duration_ms":8135}
]}`

func savedReport(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCompletedScreenRestoresSavedNodesWithoutFrames(t *testing.T) {
	path := savedReport(t, completedReport)
	m := New("297-65", 69, 39)
	_, cmd := m.Update(ended{run.Outcome{Outcome: "passed", Ended: time.Now(), Report: path}, nil})
	if cmd != nil {
		t.Fatal("completed view quit without user input")
	}
	want := []time.Duration{395, 4274, 0, 1318621, 385916, 285402, 8135}
	for i, r := range m.rows {
		if r.state != "passed" || r.took != want[i]*time.Millisecond {
			t.Errorf("row %s = %s, %v", r.step, r.state, r.took)
		}
	}
	if s := view(m); !order(s, "✓ Issue", "✓ Clone", "✓ Contract", "✓ Agent  21 min 58 s",
		"✓ Checks  6 min 25 s", "✓ Slop detector  4 min 45 s", "✓ Delivery  8.1 s", "q close") ||
		strings.Contains(s, "unit 1") {
		t.Fatalf("cold native view:\n%s", s)
	}
	b, _ := os.ReadFile(path)
	if string(b) != completedReport {
		t.Fatal("restoring the view changed the saved report")
	}
}

func TestCompletedScreenKeepsReplayDetail(t *testing.T) {
	m := screen(80, 40, unit...)
	m.end(run.Outcome{Outcome: "passed", Report: "absent-report.json"}, nil)
	if s := view(m); !strings.Contains(s, "Agent · unit 1  4 min 28 s") || strings.Contains(s, "unavailable") {
		t.Fatalf("replay detail replaced by report fallback:\n%s", s)
	}
}

func TestCompletedScreenDoesNotInventMissingResults(t *testing.T) {
	for _, body := range []string{"{", `{}`, `{"nodes":[{"name":"agent","result":"running"}]}`} {
		m := New("cold-1", 80, 40)
		m.end(run.Outcome{Outcome: "passed", Report: savedReport(t, body)}, nil)
		for _, r := range m.rows {
			if r.state != "unknown" {
				t.Errorf("report %s: %s incorrectly %s", body, r.step, r.state)
			}
		}
	}
}

func TestCompletedScreenRestoresFailureAndUnrunNodes(t *testing.T) {
	path := savedReport(t, `{"reason":"tests failed","nodes":[
	 {"name":"checks","result":"failed","duration_ms":500},
	 {"name":"ponytail","result":"not run"},
	 {"name":"delivery","result":"not run"}]}`)
	m := New("failed-1", 80, 40)
	m.end(run.Outcome{Outcome: "failed", FailedNode: "checks", Report: path}, nil)
	if s := view(m); !order(s, "✗ Checks  0.5 s", "○ Slop detector", "not run", "○ Delivery", "not run", "tests failed") {
		t.Fatalf("saved failed run:\n%s", s)
	}
}

func TestCompletedScreenClosesOnlyOnQOrControlC(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune{'q'}}, {Type: tea.KeyCtrlC}} {
		m := New("done-1", 80, 40)
		if _, cmd := m.Update(ended{run.Outcome{Outcome: "passed", Ended: time.Now()}, nil}); cmd != nil {
			t.Fatal("completed screen quit automatically")
		}
		if _, cmd := m.Update(tick(time.Now())); cmd != nil {
			t.Fatal("completed screen kept scheduling animation")
		}
		_, cmd := m.Update(key)
		if cmd == nil || m.detached {
			t.Fatal("closing completed screen must quit without reporting a still-running task")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("close did not quit")
		}
	}
	m := New("live-1", 80, 40)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil || !m.detached {
		t.Fatal("live q no longer detaches")
	}
}

func TestCompletedScreenWarnsWhenSavedReportIsMissing(t *testing.T) {
	m := New("cold-1", 80, 40)
	m.end(run.Outcome{Outcome: "passed", Report: filepath.Join(t.TempDir(), "missing.json")}, nil)
	if s := view(m); !strings.Contains(s, "Node results unavailable") || strings.Contains(s, "skipped") {
		t.Fatalf("missing report silently invented results:\n%s", s)
	}
}

func TestAttachErrorWithoutCompletedRunStillQuits(t *testing.T) {
	m := New("missing-1", 80, 40)
	_, cmd := m.Update(ended{err: os.ErrNotExist})
	if cmd == nil {
		t.Fatal("attach error waited for q")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("attach error did not quit")
	}
}
