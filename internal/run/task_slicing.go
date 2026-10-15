package run

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// taskSlicingReview is the reviewer's semantic judgment, never inferred from issue Markdown.
// Drafts stay in contract-review.json, alongside the literal parent issue, base, head and blocking reason.
type taskSlicingReview struct {
	Disposition string                  `json:"disposition"`
	Reason      string                  `json:"reason"`
	Fields      map[string]slicingField `json:"fields"`
	Pending     *[]string               `json:"mandatory_pending"`
	Drafts      *[]slicingChild         `json:"drafts"`
}

type slicingField struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type slicingChild struct {
	Purpose  string            `json:"purpose"`
	Cause    string            `json:"cause"`
	Contract map[string]string `json:"contract"`
}

// slicingApproved validates the report, then blocks unready issues, mixed contracts and mandatory work
// still pending. An optional proposal, absent from the product diff, does not invalidate a fulfilled contract.
func slicingApproved(s *taskSlicingReview) error {
	if s == nil {
		return errors.New("invalid contract review: missing task_slicing report")
	}
	if strings.TrimSpace(s.Reason) == "" || s.Pending == nil || s.Drafts == nil {
		return errors.New("invalid task_slicing: want reason, mandatory_pending and drafts")
	}
	if !slices.Contains([]string{"ready", "unready", "split-required"}, s.Disposition) {
		return fmt.Errorf("invalid task_slicing disposition %q", s.Disposition)
	}
	fields := slicingFields(s.Fields)
	drafts := slicingDrafts(*s.Drafts, s.Disposition)
	var pending error
	if len(*s.Pending) > 0 {
		pending = errors.New("task_slicing mandatory requirements pending: " + strings.Join(*s.Pending, "; "))
	}
	var disposition error
	if s.Disposition != "ready" {
		disposition = fmt.Errorf("task_slicing %s: %s", s.Disposition, s.Reason)
	}
	return errors.Join(fields, drafts, pending, disposition)
}

func slicingFields(fields map[string]slicingField) error {
	for _, name := range []string{"non_goals", "invariants", "acceptance", "split_triggers"} {
		f, ok := fields[name]
		if !ok || strings.TrimSpace(f.Reason) == "" {
			return fmt.Errorf("invalid task_slicing fields: missing %s result or reason", name)
		}
		switch f.Status {
		case "present":
		case "missing", "not-executable":
			return fmt.Errorf("task_slicing unready: %s %s: %s", name, f.Status, f.Reason)
		default:
			return fmt.Errorf("invalid task_slicing %s status %q", name, f.Status)
		}
	}
	if len(fields) != 4 {
		return errors.New("invalid task_slicing fields: want exactly four results")
	}
	return nil
}

func slicingDrafts(drafts []slicingChild, disposition string) error {
	if disposition == "split-required" && len(drafts) == 0 {
		return errors.New("invalid task_slicing: split-required needs a local child draft")
	}
	var blocks []error
	for _, d := range drafts {
		if err := slicingChildValid(d); err != nil {
			return err
		}
		if d.Purpose != "optional" {
			blocks = append(blocks, fmt.Errorf("task_slicing %s draft blocks delivery: %s", d.Purpose, d.Cause))
		}
	}
	return errors.Join(blocks...)
}

func slicingChildValid(d slicingChild) error {
	if !slices.Contains([]string{"optional", "required", "split"}, d.Purpose) || strings.TrimSpace(d.Cause) == "" {
		return errors.New("invalid task_slicing draft: want purpose (optional, required or split) and cause")
	}
	for _, field := range []string{"kind", "goal", "non_goals", "invariants", "behavioral_contract", "scope_surface",
		"acceptance", "split_triggers", "dependencies_parent", "evidence"} {
		if strings.TrimSpace(d.Contract[field]) == "" {
			return fmt.Errorf("invalid task_slicing draft: missing %s", field)
		}
	}
	if len(d.Contract) != 10 {
		return errors.New("invalid task_slicing draft: want exactly ten contract fields")
	}
	return nil
}

// The full skill is distributed by agent.go; this only describes the required report wire format.
const slicingSchema = `{"disposition":"<ready, unready or split-required>","reason":"<why>","fields":{` +
	`"non_goals":{"status":"<present, missing or not-executable>","reason":"<evidence>"},` +
	`"invariants":{"status":"<present, missing or not-executable>","reason":"<evidence>"},` +
	`"acceptance":{"status":"<present, missing or not-executable>","reason":"<executable check or gap>"},` +
	`"split_triggers":{"status":"<present, missing or not-executable>","reason":"<evidence>"}},` +
	`"mandatory_pending":["<each unmet mandatory requirement, or empty array>"],` +
	`"drafts":[{"purpose":"<optional, required or split>","cause":"<why separate>","contract":{` +
	`"kind":"<kind>","goal":"<goal>","non_goals":"<non-goals>","invariants":"<invariants>",` +
	`"behavioral_contract":"<states and behavior>","scope_surface":"<paths>","acceptance":"<runnable check>",` +
	`"split_triggers":"<triggers>","dependencies_parent":"<parent identity and dependency>",` +
	`"evidence":"<evidence>"}}]}`
