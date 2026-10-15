package run

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These fixtures use only the pre-existing Run entry point and JSON, so the red
// compiles with main's source files unchanged.
func slicingReport() obj {
	fields := obj{}
	for _, field := range []string{"non_goals", "invariants", "acceptance", "split_triggers"} {
		fields[field] = obj{"status": "present", "reason": "Explicit in the literal issue; acceptance is executable."}
	}
	return obj{"disposition": "ready", "fields": fields, "reason": "One contract, fulfilled.",
		"mandatory_pending": []string{}, "drafts": []obj{}}
}

func slicingJSON(t *testing.T, slicing any) string {
	t.Helper()
	b, err := json.Marshal(obj{"disposition": "approved", "base": "{base}", "head": "{head}",
		"findings": []obj{}, "summary": "Contract fulfilled.", "task_slicing": slicing})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func slicingDraft(purpose string) obj {
	return obj{"purpose": purpose, "cause": "Independent logging contract", "contract": obj{
		"kind": "implement", "goal": "Add a log entry", "non_goals": "No retry changes",
		"invariants": "Retry remains unchanged", "acceptance": "go test ./internal/logging",
		"split_triggers": "Remote export needs another contract", "scope_surface": "internal/logging",
		"behavioral_contract": "A timeout emits one log entry", "dependencies_parent": "Parent #323; after parent",
		"evidence": "The diff introduces logging independently of the retry requirement",
	}}
}

func TestContractReviewTaskSlicingFields(t *testing.T) {
	for _, field := range []string{"non_goals", "invariants", "acceptance", "split_triggers"} {
		for _, status := range []string{"missing", "not-executable", "omitted"} {
			t.Run(field+"/"+status, func(t *testing.T) {
				s := slicingReport()
				s["disposition"] = "unready"
				fields := s["fields"].(obj)
				fields[field] = obj{"status": status, "reason": "The issue does not supply this requirement"}
				if status == "omitted" {
					delete(fields, field)
				}
				runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery),
					"FAKE_REVIEW_REPORT", slicingJSON(t, s))
				notDelivered(t, err, runDir, got, field)
			})
		}
	}
}

func TestContractReviewTaskSlicingDrafts(t *testing.T) {
	for _, purpose := range []string{"split", "optional", "required"} {
		t.Run(purpose, func(t *testing.T) {
			s := slicingReport()
			drafts := []obj{slicingDraft(purpose)}
			if purpose == "split" {
				s["disposition"] = "split-required"
				s["reason"] = "Retry and logging are two independent contracts"
				drafts = append(drafts, slicingDraft(purpose))
				drafts[1]["contract"].(obj)["goal"] = "Retry once on timeout"
			}
			s["drafts"] = drafts
			runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery),
				"FAKE_REVIEW_REPORT", slicingJSON(t, s))
			if purpose == "optional" {
				if err != nil {
					t.Fatal(err)
				}
				if calls := reader(t, got)("calls"); calls != "ran" {
					t.Fatalf("want one delivery, got %q", calls)
				}
				clone := reader(t, got)("clone")
				if left := gitT(t, clone, "status", "--porcelain"); left != "" {
					t.Fatalf("optional improvement changed product: %s", left)
				}
			} else {
				notDelivered(t, err, runDir, got, purpose)
			}
			report := contractReview(t, runDir)["report"].(map[string]any)
			want, _ := json.Marshal(s)
			actual, _ := json.Marshal(report["task_slicing"])
			if !bytes.Equal(actual, want) {
				t.Fatalf("lost draft, cause or parent dependency: %s, want %s", actual, want)
			}
		})
	}
}

func TestContractReviewTaskSlicingRejectsInvalidOrPending(t *testing.T) {
	cases := []struct {
		name, why string
		change    func(obj)
	}{
		{"pending in previous follow-up", "Retry still missing", func(s obj) {
			s["mandatory_pending"] = []string{"Retry still missing; already recorded in a previous follow-up"}
		}},
		{"missing fields", "fields", func(s obj) { delete(s, "fields") }},
		{"wrong type", "invalid", func(s obj) { s["mandatory_pending"] = false }},
		{"unknown disposition", "disposition", func(s obj) { s["disposition"] = "maybe" }},
		{"unknown field", "invalid", func(s obj) { s["inferred"] = true }},
		{"split without draft", "draft", func(s obj) { s["disposition"] = "split-required" }},
		{"unready despite present fields", "unready", func(s obj) { s["disposition"] = "unready" }},
		{"ready despite missing field", "acceptance", func(s obj) {
			s["fields"].(obj)["acceptance"] = obj{"status": "missing", "reason": "No runnable check"}
		}},
		{"incomplete draft", "acceptance", func(s obj) {
			d := slicingDraft("optional")
			delete(d["contract"].(obj), "acceptance")
			s["drafts"] = []obj{d}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := slicingReport()
			tc.change(s)
			runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery),
				"FAKE_REVIEW_REPORT", slicingJSON(t, s))
			notDelivered(t, err, runDir, got, tc.why)
		})
	}
}

func TestContractReviewTaskSlicingNeedsBothApprovals(t *testing.T) {
	t.Run("A alone", func(t *testing.T) {
		runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery), "FAKE_REVIEW_REPORT",
			`{"disposition":"approved","base":"{base}","head":"{head}","findings":[],"summary":"ok"}`)
		notDelivered(t, err, runDir, got, "task_slicing")
	})
	t.Run("task-slicing alone", func(t *testing.T) {
		r := strings.Replace(slicingJSON(t, slicingReport()), `"findings":[]`,
			`"findings":[{"criterion":"Retry once on a timeout.","evidence":"Still missing"}]`, 1)
		r = strings.Replace(r, `"disposition":"approved"`, `"disposition":"blocked"`, 1)
		runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery), "FAKE_REVIEW_REPORT", r)
		notDelivered(t, err, runDir, got, "Still missing")
	})
	t.Run("ponytail proof still required", func(t *testing.T) {
		runDir, _, got, _, err := reviewRun(t, atmSet(atmYAML, "delivery", reviewDelivery),
			"FAKE_REVIEW_REPORT", slicingJSON(t, slicingReport()), "FAKE_REVIEW", "no-skill")
		notDelivered(t, err, runDir, got, "no proof it used the ponytail-review skill")
	})
}

func TestContractReviewTaskSlicingLiteralIssueAndSkills(t *testing.T) {
	for _, ready := range []bool{true, false} {
		t.Run(map[bool]string{true: "prose", false: "empty headings"}[ready], func(t *testing.T) {
			issue := "Retry on timeout\nType: fix\nRetry once on timeout. Do not add logging. Preserve all other errors. " +
				"Run go test ./retry. Separate persistent failures into another contract."
			s := slicingReport()
			if !ready {
				issue = "Retry on timeout\nType: fix\n## Non-goals\n## Invariants\n## Acceptance\n## Split triggers\n"
				s["disposition"] = "unready"
				s["fields"].(obj)["acceptance"] = obj{"status": "missing", "reason": "Empty heading"}
			}
			root := repo(t, "local", atmSet(atmYAML, "delivery", reviewDelivery))
			got, call := t.TempDir(), filepath.Join(t.TempDir(), "call.json")
			t.Setenv("ATM_TEST_OUT", got)
			t.Setenv("FAKE_AGENT_CALL", call)
			t.Setenv("FAKE_REVIEW_REPORT", slicingJSON(t, s))
			err := Run("t", []string{issueFile(t, issue)}, &bytes.Buffer{}, &bytes.Buffer{})
			if ready && err != nil {
				t.Fatal(err)
			}
			if !ready {
				notDelivered(t, err, filepath.Join(root, ".atm", "runs", "t"), got, "acceptance")
			} else if reader(t, got)("calls") != "ran" {
				t.Fatal("delivery did not run exactly once")
			}
			brief := agentCall(t, call+".review").Stdin
			for _, want := range []string{issue, "ponytail-review", "task-slicing", "Non-goals", "Invariants",
				"Acceptance", "Split triggers"} {
				if !strings.Contains(brief, want) {
					t.Fatalf("review brief missing %q", want)
				}
			}
			for _, skill := range []string{"ponytail-review", "task-slicing"} {
				content, e := skills.ReadFile(skill + "/SKILL.md")
				if e != nil || !bytes.Contains(content, []byte("name: "+skill)) {
					t.Fatalf("skill %s not distributed: %v", skill, e)
				}
			}
		})
	}
}

func TestContractReviewTaskSlicingDistribution(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode", "pi", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			repo(t, "local", atmSet(atmYAML, "delivery", reviewDelivery))
			got, call := t.TempDir(), filepath.Join(t.TempDir(), "call.json")
			t.Setenv("ATM_TEST_OUT", got)
			t.Setenv("FAKE_AGENT_CALL", call)
			t.Setenv("FAKE_REVIEW_REPORT", slicingJSON(t, slicingReport()))
			if err := Run("t", []string{"--harness", harness, issueFile(t, reviewIssue)},
				&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			clone := reader(t, got)("clone")
			invocation := agentCall(t, call+".review")
			brief := invocation.Stdin + strings.Join(invocation.Args, " ")
			for _, skill := range []string{"ponytail-review", "task-slicing"} {
				path := filepath.Join(skillDirs[harness], skill, "SKILL.md")
				actual, err := os.ReadFile(filepath.Join(clone, path))
				want, _ := skills.ReadFile(skill + "/SKILL.md")
				if err != nil || !bytes.Equal(actual, want) || !strings.Contains(brief, skill) {
					t.Fatalf("%s did not receive complete %s skill: %v", harness, skill, err)
				}
				if tracked := gitT(t, clone, "ls-files", path); tracked != "" {
					t.Fatalf("review skill entered product branch: %s", tracked)
				}
			}
			if harness == "pi" && strings.Count(brief, "--skill ") != 2 {
				t.Fatalf("pi must explicitly load both skills: %v", invocation.Args)
			}
		})
	}
}

func TestContractReviewTaskSlicingTenDraftFields(t *testing.T) {
	for field := range slicingDraft("optional")["contract"].(obj) {
		t.Run(field, func(t *testing.T) {
			s, d := slicingReport(), slicingDraft("optional")
			delete(d["contract"].(obj), field)
			s["drafts"] = []obj{d}
			var r obj
			if err := json.Unmarshal([]byte(slicingJSON(t, s)), &r); err != nil {
				t.Fatal(err)
			}
			if err := approved(r, "{base}", "{head}"); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("want incomplete child blocked naming %s, got %v", field, err)
			}
		})
	}
}
