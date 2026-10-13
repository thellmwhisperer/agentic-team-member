package tui

import (
	"encoding/json"
	"os"
	"slices"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

// restoreReport recovers node results after the daemon lost its in-memory frames. The report has only
// the last Agent/Checks timings, not every unit's events, so it must not invent per-unit rows or tool counts.
func (m *Model) restoreReport(path string, outcome run.Outcome) {
	for _, r := range m.rows {
		r.state = "unknown"
	}
	m.current("agent").label = "Agent"
	var report struct {
		Nodes []struct {
			Name       string `json:"name"`
			Result     string `json:"result"`
			DurationMS *int64 `json:"duration_ms"`
		}
		Reason string
	}
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &report)
	}
	if err != nil {
		m.findings = append(m.findings, finding{"warning", "report.json", "Node results unavailable: " + err.Error()})
		return
	}
	for _, n := range report.Nodes {
		r := m.current(n.Name)
		if r == nil || n.DurationMS != nil && *n.DurationMS < 0 || !consistentResult(n.Name, n.Result, outcome) {
			continue
		}
		switch n.Result {
		case "passed", "failed", "skipped", "not run":
			r.state = n.Result
			if n.DurationMS != nil {
				r.took, r.hasDuration = time.Duration(*n.DurationMS)*time.Millisecond, true
			}
		default: // a stale running report is not evidence that the ended run is still working
			continue
		}
		if n.Name == "agent" {
			r.note = "saved node"
		}
		if n.Result == "failed" && report.Reason != "" {
			m.findings = append(m.findings, finding{"error", r.label, report.Reason})
		}
	}
}

func consistentResult(name, result string, outcome run.Outcome) bool {
	if outcome.Outcome == "passed" {
		return result != "failed" && (result != "not run" || name == "delivery")
	}
	if outcome.Outcome != "failed" || outcome.FailedNode == "" {
		return result != "failed" || outcome.Outcome == "failed"
	}
	order := []string{"issue", "clone", "contract", "agent", "checks", "ponytail", "delivery"}
	failed := slices.Index(order, outcome.FailedNode)
	i := slices.Index(order, name)
	if failed < 0 || i < 0 {
		return result != "failed"
	}
	switch {
	case i < failed:
		return result != "failed" && result != "not run"
	case i == failed:
		return result == "failed"
	default:
		return result == "not run" || result == "skipped"
	}
}
