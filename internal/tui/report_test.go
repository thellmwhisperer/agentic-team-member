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
		if r.state != "passed" || r.took != want[i]*time.Millisecond || !r.hasDuration {
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

func TestCompletedScreenWithoutReportKeepsRowsUnknown(t *testing.T) {
	m := New("cold-no-report", 80, 40)
	m.end(run.Outcome{Outcome: "passed", Ended: time.Now()}, nil)
	for _, r := range m.rows {
		if r.state != "unknown" {
			t.Errorf("row %s = %s, want unknown", r.step, r.state)
		}
	}
	if strings.Contains(view(m), "unit 1") {
		t.Fatal("cold completed view invented an Agent unit")
	}
}

func TestCompletedScreenKeepsMissingDurationUnknown(t *testing.T) {
	m := New("cold-2", 80, 40)
	m.end(run.Outcome{Outcome: "passed", Report: savedReport(t, `{"nodes":[{"name":"agent","result":"passed"}]}`)}, nil)
	r := m.current("agent")
	if r.state != "passed" || r.hasDuration {
		t.Fatalf("missing duration became evidence: state=%s duration=%v", r.state, r.hasDuration)
	}
	if s := view(m); strings.Contains(s, "Agent  0.0 s") {
		t.Fatalf("missing duration rendered as zero:\n%s", s)
	}
}

func TestCompletedScreenKeepsOverflowingDurationUnknown(t *testing.T) {
	m := New("cold-overflow", 80, 40)
	m.end(run.Outcome{Outcome: "passed", Report: savedReport(t, `{"nodes":[
		{"name":"agent","result":"passed","duration_ms":9223372036855}
	]}`)}, nil)
	r := m.current("agent")
	if r.state != "passed" || r.hasDuration {
		t.Fatalf("overflowing duration became evidence: state=%s duration=%v", r.state, r.took)
	}
	if s := view(m); strings.Contains(s, "292471") || strings.Contains(s, "0.0 s") {
		t.Fatalf("overflowing duration rendered in completed view:\n%s", s)
	}
}

func TestCompletedScreenKeepsContradictorySavedResultUnknown(t *testing.T) {
	m := New("cold-3", 80, 40)
	m.end(run.Outcome{Outcome: "passed", Report: savedReport(t, `{"nodes":[
		{"name":"issue","result":"passed","duration_ms":12},
		{"name":"checks","result":"failed","duration_ms":500}
	]}`)}, nil)
	if m.current("issue").state != "passed" || m.current("checks").state != "unknown" {
		t.Fatalf("stale report was not reconciled: issue=%s checks=%s", m.current("issue").state, m.current("checks").state)
	}
	if s := view(m); strings.Contains(s, "✗ Checks") || !strings.Contains(s, "? Checks") {
		t.Fatalf("contradictory result shown as current:\n%s", s)
	}
}

func TestCompletedScreenRestoresFailureAndUnrunNodes(t *testing.T) {
	path := savedReport(t, `{"reason":"stale report reason","nodes":[
	 {"name":"checks","result":"failed","duration_ms":500},
	 {"name":"ponytail","result":"not run","duration_ms":0},
	 {"name":"delivery","result":"not run","duration_ms":0}]}`)
	m := New("failed-1", 80, 40)
	m.end(run.Outcome{Outcome: "failed", FailedNode: "checks", Reason: "tests failed", Report: path}, nil)
	if s := view(m); !order(s, "✗ Checks  0.5 s", "○ Slop detector", "not run", "○ Delivery", "not run", "tests failed") {
		t.Fatalf("saved failed run:\n%s", s)
	}
	for _, step := range []string{"ponytail", "delivery"} {
		if r := m.current(step); r.hasDuration {
			t.Errorf("unrun %s kept a duration", step)
		}
	}
	if strings.Contains(view(m), "stale report reason") {
		t.Fatal("displayed an untrusted saved failure reason")
	}
}

func TestCompletedScreenRestoresPriorUnitChecksAfterLaterAgentFailure(t *testing.T) {
	m := New("multi-unit-1", 80, 40)
	m.end(run.Outcome{Outcome: "failed", FailedNode: "agent", Report: savedReport(t, `{"nodes":[
		{"name":"issue","result":"passed","duration_ms":5},
		{"name":"clone","result":"passed","duration_ms":6},
		{"name":"contract","result":"passed","duration_ms":7},
		{"name":"agent","result":"failed","duration_ms":800},
		{"name":"checks","result":"passed","duration_ms":100},
		{"name":"ponytail","result":"not run","duration_ms":0},
		{"name":"delivery","result":"not run","duration_ms":0}
	]}`)}, nil)
	if m.current("agent").state != "failed" || m.current("checks").state != "passed" ||
		m.current("checks").took != 100*time.Millisecond {
		t.Fatalf("multi-unit evidence was lost: agent=%+v checks=%+v", m.current("agent"), m.current("checks"))
	}
}

func TestCompletedScreenRestoresNodesWhenFailureIsOutsideNode(t *testing.T) {
	m := New("external-failure-1", 80, 40)
	m.end(run.Outcome{Outcome: "failed", Reason: "could not save follow-ups", Report: savedReport(t, `{"nodes":[
		{"name":"issue","result":"passed","duration_ms":5},
		{"name":"clone","result":"passed","duration_ms":6},
		{"name":"contract","result":"passed","duration_ms":7},
		{"name":"agent","result":"passed","duration_ms":8},
		{"name":"checks","result":"passed","duration_ms":9},
		{"name":"ponytail","result":"not run","duration_ms":0},
		{"name":"delivery","result":"not run","duration_ms":0}
	]}`)}, nil)
	if m.current("checks").state != "passed" || m.current("checks").took != 9*time.Millisecond ||
		m.current("ponytail").state != "not run" {
		t.Fatalf("valid external-failure evidence was lost: checks=%+v ponytail=%+v",
			m.current("checks"), m.current("ponytail"))
	}
}

func TestCompletedScreenRejectsImpossibleAndDuplicateNodes(t *testing.T) {
	m := New("cold-4", 80, 40)
	m.end(run.Outcome{Outcome: "passed", Report: savedReport(t, `{"nodes":[
		{"name":"issue","result":"passed","duration_ms":10},
		{"name":"checks","result":"skipped","duration_ms":0},
		{"name":"clone","result":"passed","duration_ms":10},
		{"name":"clone","result":"passed"},
		{"name":"delivery","result":"skipped","duration_ms":0}
	]}`)}, nil)
	if m.current("issue").state != "passed" || m.current("checks").state != "unknown" ||
		m.current("clone").state != "unknown" || m.current("clone").hasDuration ||
		m.current("delivery").state != "skipped" || m.current("delivery").hasDuration {
		t.Fatalf("unsupported report data presented as evidence: issue=%+v checks=%+v clone=%+v delivery=%+v",
			m.current("issue"), m.current("checks"), m.current("clone"), m.current("delivery"))
	}
	failed := New("failed-skipped", 80, 40)
	failed.end(run.Outcome{Outcome: "failed", FailedNode: "checks", Report: savedReport(t, `{"nodes":[
		{"name":"checks","result":"skipped","duration_ms":0},
		{"name":"delivery","result":"skipped","duration_ms":0}
	]}`)}, nil)
	if failed.current("checks").state != "unknown" || failed.current("delivery").state != "unknown" {
		t.Fatalf("impossible skipped result was accepted after node failure: checks=%s delivery=%s",
			failed.current("checks").state, failed.current("delivery").state)
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
