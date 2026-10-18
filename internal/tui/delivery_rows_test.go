package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

func TestScreenShowsDeliveryRows(t *testing.T) {
	names := []string{"commit", "review", "repair 1", "review 2", "command"}
	m := screen(60, 50, `{"step":"delivery","state":"started",`+ts+`}`)
	m.now = time.Date(2026, 10, 7, 5, 0, 2, 0, time.UTC)
	var rows []map[string]any
	for _, name := range names {
		m.apply(run.Frame{Event: fmt.Appendf(nil, `{"step":"delivery","check":%q,"state":"started",%s}`, name, ts)})
		if r := m.checkRow("delivery", name); r.state != "running" || !strings.Contains(view(m), name+"  2.0 s") {
			t.Fatalf("%s not live: %+v\n%s", name, r, view(m))
		}
		result := "passed"
		if name == "review" {
			result = "failed"
		}
		m.apply(run.Frame{Event: fmt.Appendf(nil,
			`{"step":"delivery","check":%q,"state":%q,"duration_ms":1100}`, name, result)})
		rows = append(rows, map[string]any{"name": name, "result": result, "duration_ms": 1100})
	}
	m.apply(run.Frame{Event: []byte(`{"step":"delivery","state":"passed","duration_ms":5500}`)})
	b, err := json.Marshal(map[string]any{"nodes": []any{map[string]any{
		"name": "delivery", "result": "passed", "duration_ms": 5500, "rows": rows}}})
	if err != nil {
		t.Fatal(err)
	}
	attached := New("finished", 60, 50)
	attached.end(run.Outcome{Outcome: "passed", Report: savedReport(t, string(b))}, nil)
	for _, model := range []*Model{m, attached} {
		if s := view(model); !order(s, "✓ Delivery  5.5 s", "✓ commit  1.1 s", "✗ review  1.1 s",
			"✓ repair 1  1.1 s", "✓ review 2  1.1 s", "✓ command  1.1 s") {
			t.Fatalf("delivery rows missing from screen:\n%s", s)
		}
	}
}

func TestScreenSkippedDeliveryHasNoRows(t *testing.T) {
	m := New("skipped", 60, 40)
	m.end(run.Outcome{Outcome: "passed", Report: savedReport(t,
		`{"nodes":[{"name":"delivery","result":"skipped","duration_ms":0}]}`)}, nil)
	for _, r := range m.rows {
		if r.step == "delivery" && r.check {
			t.Fatalf("skipped delivery has sub-row: %+v", r)
		}
	}
	if s := view(m); !order(s, "Delivery", "skipped") {
		t.Fatalf("skipped delivery missing:\n%s", s)
	}
}
