package tui

import (
	"encoding/json"
	"os"
	"slices"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

const maxDurationMS = int64(^uint64(0)>>1) / int64(time.Millisecond)

// restoreReport recovers node results after the daemon lost its in-memory frames. The report has only
// the last Agent/Checks timings, not every unit's events, so it must not invent per-unit rows or tool counts.
func (m *Model) restoreReport(path string, outcome run.Outcome) {
	for _, r := range m.rows {
		r.state = "unknown"
	}
	m.current("agent").label = "Agent"
	if path == "" {
		return
	}
	var report struct {
		Nodes []struct {
			Name       string `json:"name"`
			Result     string `json:"result"`
			DurationMS *int64 `json:"duration_ms"`
		}
	}
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &report)
	}
	if err != nil {
		m.findings = append(m.findings, finding{"warning", "report.json", "Node results unavailable: " + err.Error()})
		return
	}
	seen := make(map[string]bool, len(report.Nodes))
	for _, n := range report.Nodes {
		r := m.current(n.Name)
		if r == nil || seen[n.Name] {
			if r != nil && seen[n.Name] {
				r.state, r.took, r.hasDuration, r.note = "unknown", 0, false, ""
			}
			continue
		}
		seen[n.Name] = true
		if !consistentResult(n.Name, n.Result, outcome) {
			continue
		}
		switch n.Result {
		case "passed", "failed", "skipped", "not run":
			r.state = n.Result
			if n.DurationMS != nil && *n.DurationMS >= 0 && *n.DurationMS <= maxDurationMS &&
				(n.Result == "passed" || n.Result == "failed") {
				r.took, r.hasDuration = time.Duration(*n.DurationMS)*time.Millisecond, true
			}
		default: // a stale running report is not evidence that the ended run is still working
			continue
		}
		if n.Name == "agent" {
			r.note = "saved node"
		}
	}
}

func consistentResult(name, result string, outcome run.Outcome) bool {
	order := []string{"issue", "clone", "contract", "agent", "checks", "ponytail", "delivery"}
	i := slices.Index(order, name)
	if i < 0 {
		return false
	}
	if outcome.Outcome == "passed" {
		return result == "passed" || i == len(order)-1 && result == "skipped"
	}
	if outcome.Outcome != "failed" {
		return false
	}
	failed := slices.Index(order, outcome.FailedNode)
	if failed < 0 {
		return false
	}
	switch {
	case i < failed:
		return result == "passed"
	case i == failed:
		return result == "failed"
	default:
		return result == "not run"
	}
}
