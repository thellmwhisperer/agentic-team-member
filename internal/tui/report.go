package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/run"
)

const maxDurationMS = int64(^uint64(0)>>1) / int64(time.Millisecond)

type savedNode struct {
	Name          string            `json:"name"`
	ClonePath     string            `json:"clone"`
	Result        string            `json:"result"`
	DurationMS    *int64            `json:"duration_ms"`
	Rows          []savedNode       `json:"rows"`
	RejectedFiles []json.RawMessage `json:"rejected_files"`
}

// restoreReport recovers node results after the daemon lost its in-memory frames. The report has only
// the last Agent/Checks timings, not every unit's events, so it must not invent per-unit rows or tool counts.
func (m *Model) restoreReport(path string, outcome run.Outcome) {
	for _, r := range m.rows {
		if !strings.HasPrefix(r.step, "gate:") {
			r.state = "unknown"
		}
	}
	if path == "" {
		return
	}
	var report struct {
		Nodes     []savedNode
		Decisions []run.Decision
	}
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &report)
	}
	if err != nil {
		m.findings = append(m.findings, finding{"warning", "report.json", "Node results unavailable: " + err.Error()})
		return
	}
	m.skipPonytail(skippedByConfig(report.Nodes))
	seen := make(map[string]bool, len(report.Nodes))
	for _, n := range report.Nodes {
		r := m.current(n.Name)
		if r == nil || seen[n.Name] {
			clearDuplicate(r, seen[n.Name])
			continue
		}
		seen[n.Name] = true
		approval := savedApproval(n, report.Decisions, outcome)
		if approval == nil && !validSavedNode(n, outcome) {
			continue
		}
		r.state = n.Result
		if n.Name == "clone" {
			m.clone = n.ClonePath
		}
		if n.Result == "passed" || n.Result == "failed" {
			r.took, r.hasDuration = savedDuration(n.DurationMS)
		}
		m.restoreRows(n)
		if approval != nil {
			m.approveChecks(approval.By)
		}
	}
}

// skippedByConfig reports whether the saved report marks the ponytail node as skipped.
func skippedByConfig(nodes []savedNode) bool {
	for _, n := range nodes {
		if n.Name == "ponytail" && n.Result == "skipped" {
			return true
		}
	}
	return false
}

// savedApproval returns the last answer to a failed Checks node of a passed run when it was an approve.
func savedApproval(n savedNode, decisions []run.Decision, outcome run.Outcome) *run.Decision {
	if n.Name != "checks" || n.Result != "failed" || !consistentResult(n.Name, "passed", outcome) {
		return nil
	}
	for i := len(decisions) - 1; i >= 0; i-- {
		d := &decisions[i]
		if d.Node == n.Name {
			if d.Answer == "approve" {
				return d
			}
			return nil
		}
	}
	return nil
}

func savedDuration(ms *int64) (time.Duration, bool) {
	if ms != nil && *ms >= 0 && *ms <= maxDurationMS {
		return time.Duration(*ms) * time.Millisecond, true
	}
	return 0, false
}

func (m *Model) restoreRows(n savedNode) {
	for _, part := range n.Rows {
		child := m.checkRow(n.Name, part.Name)
		child.state = "unknown"
		if part.Result == "passed" || part.Result == "failed" {
			child.state = part.Result
			if part.DurationMS != nil && *part.DurationMS >= 0 && *part.DurationMS <= maxDurationMS {
				child.took, child.hasDuration = time.Duration(*part.DurationMS)*time.Millisecond, true
			}
		}
	}
	if n.Name == "ponytail" && n.Result == "passed" && len(n.RejectedFiles) > 0 {
		m.checkRow(n.Name, fmt.Sprintf("put back %d files", len(n.RejectedFiles))).state = "passed"
	}
}

func clearDuplicate(r *row, duplicate bool) {
	if duplicate {
		r.state, r.took, r.hasDuration, r.note = "unknown", 0, false, ""
	}
}

func validSavedNode(n savedNode, outcome run.Outcome) bool {
	switch n.Result {
	case "passed", "failed", "skipped", "not run":
		return consistentResult(n.Name, n.Result, outcome)
	default: // a stale running report is not evidence that the ended run is still working
		return false
	}
}

func consistentResult(name, result string, outcome run.Outcome) bool {
	if outcome.Outcome == "failed" && outcome.FailedNode == name {
		return result == "failed"
	}
	switch result {
	case "passed":
		return outcome.Outcome == "passed" || outcome.Outcome == "failed"
	case "failed":
		return outcome.Outcome == "failed" && outcome.FailedNode == name
	case "skipped":
		return name == "delivery" && (outcome.Outcome == "passed" || outcome.Outcome == "failed" && outcome.FailedNode == "")
	case "not run":
		return outcome.Outcome == "failed"
	default:
		return false
	}
}
