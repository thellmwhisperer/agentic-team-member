package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestContractReviewRepairDeliversCurrentEvidence(t *testing.T) {
	for _, rounds := range []int{1, 2} {
		t.Run(strconv.Itoa(rounds), func(t *testing.T) {
			blocked := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 retry missing", nil)
			reports := []string{blocked, slicingJSON(t, slicingReport())}
			if rounds == 2 {
				reports = []string{blocked, blocked, slicingJSON(t, slicingReport())}
			}
			repairEnv(t, reports, []string{"printf 'fixed\\nround-one\\n' > a.txt", "printf 'fixed\\nround-two\\n' > a.txt"})
			atm := atmSet(repairATM(), "test_file", "cat a.txt; sh {file}")
			atm = atmSet(atm, "lint", "cat a.txt")
			atm = atmSet(atm, "delivery", reviewDelivery+`; cp "$ATM_REPORT" "$ATM_TEST_OUT/report.json"`)
			runDir, _, got, _, err := reviewRun(t, atm)
			if err != nil {
				t.Fatal(err)
			}
			var delivered verdict
			if err := json.Unmarshal(mustRead(t, filepath.Join(got, "report.json")), &delivered); err != nil {
				t.Fatal(err)
			}
			var review struct {
				Head   string
				Repair repairEvidence
			}
			if err := json.Unmarshal(mustRead(t, filepath.Join(runDir, "contract-review.json")), &review); err != nil {
				t.Fatal(err)
			}
			if delivered.HeadSHA != reader(t, got)("head") || review.Head != delivered.HeadSHA ||
				len(review.Repair.Attempts) != rounds {
				t.Fatalf("wrong delivered head or attempts: %+v %+v", delivered, review)
			}
			assertRepairAttemptEvidence(t, review.Repair)
			want := []string{"fixed\nround-one", "fixed\nround-two"}[rounds-1]
			if len(delivered.Reproofs) != 1 || delivered.Reproofs[0].Green.Tail != want {
				t.Fatalf("delivery received stale proofs: %+v", delivered.Reproofs)
			}
			last := delivered.Commands[len(delivered.Commands)-1]
			if last.Name != "lint" || last.Tail != want {
				t.Fatalf("delivery received stale checks: %+v", delivered.Commands)
			}
		})
	}
}

func TestContractReviewRepairTypesWithoutTestFile(t *testing.T) {
	for typ, file := range map[string]string{"docs": "README.md", "chore": "deps.txt", "refactor": "b.txt"} {
		for _, fail := range []bool{false, true} {
			t.Run(typ+"/failed-check="+strconv.FormatBool(fail), func(t *testing.T) {
				blocked := repairBlocked(t, "Retry once on a timeout.", file+":1 incomplete", nil)
				repairEnv(t, []string{blocked, slicingJSON(t, slicingReport())}, []string{"echo repaired > " + file})
				t.Setenv("ATM_TEST_ISSUE", strings.Replace(reviewIssue, "Type: fix", "Type: "+typ, 1))
				t.Setenv("FAKE_AGENT_WORK",
					work[typ]+`; mkdir -p .atm; printf '{"test_file":"","follow_ups":[]}' > .atm/fake-report.json`)
				atm := repairATM()
				if fail {
					atm = atmSet(atm, "lint", "! grep -q repaired "+file)
				}
				runDir, _, got, _, err := reviewRun(t, atm)
				if fail {
					assertNoDelivery(t, err, runDir, got, "lint")
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if reader(t, got)("calls") != "ran" || repairLogs(t, runDir) != 1 {
					t.Fatal("valid repair did not deliver exactly once")
				}
				var review struct{ Repair repairEvidence }
				if err := json.Unmarshal(mustRead(t, filepath.Join(runDir, "contract-review.json")), &review); err != nil {
					t.Fatal(err)
				}
				attempt := review.Repair.Attempts[0]
				assertRepairTypeProof(t, attempt, typ)
			})
		}
	}
}

func TestContractReviewRepairRetainsFindingsAcrossRounds(t *testing.T) {
	for _, approve := range []bool{false, true} {
		t.Run("approved="+strconv.FormatBool(approve), func(t *testing.T) {
			var reports []string
			for n := 1; n <= 3; n++ {
				s := slicingReport()
				d := slicingDraft("optional")
				d["contract"].(obj)["goal"] = "Optional " + strconv.Itoa(n)
				s["drafts"] = []obj{d}
				var report obj
				raw := repairBlocked(t, "Retry once on a timeout.", "a.txt:1 retry missing", s)
				if approve && n == 3 {
					raw = slicingJSON(t, s)
				}
				if err := json.Unmarshal([]byte(raw), &report); err != nil {
					t.Fatal(err)
				}
				if !approve || n < 3 {
					report["findings"] = append(report["findings"].([]any),
						obj{"criterion": "Owner " + strconv.Itoa(n), "evidence": "Separate work"})
				}
				b, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				reports = append(reports, string(b))
			}
			repairEnv(t, reports, []string{repairGrow, repairGrow})
			runDir, _, got, call, err := reviewRun(t, repairATM())
			if approve && err != nil || !approve && err == nil {
				t.Fatalf("unexpected review result: %v", err)
			}
			assertRepairRetainedFindings(t, runDir, got, call, approve)
		})
	}
}

func TestContractReviewRepairClassifiesErrorsByIdentity(t *testing.T) {
	words := "invalid contract review; invalid task_slicing; changed the clone; uncommitted changes; made commits"
	t.Run("criterion and evidence are data", func(t *testing.T) {
		criterion := "Delivery must leave no uncommitted changes."
		t.Setenv("ATM_TEST_ISSUE", reviewIssue+criterion+"\n")
		s := slicingReport()
		s["mandatory_pending"] = []string{criterion}
		repairEnv(t, []string{repairBlocked(t, criterion, words, s), slicingJSON(t, slicingReport())}, []string{repairGrow})
		runDir, _, got, _, err := reviewRun(t, repairATM())
		if err != nil || repairLogs(t, runDir) != 1 {
			t.Fatalf("contract finding was mistaken for a reviewer failure: %v", err)
		}
		if reader(t, got)("calls") != "ran" {
			t.Fatal("repair was not delivered")
		}
	})
	for name, work := range map[string]string{
		"file mutation":    "echo changed > touched.txt",
		"ignored mutation": "mkdir -p .atm; echo changed > .atm/touched",
		"commit mutation":  "git -c user.name=r -c user.email=r@example.com commit -q --allow-empty -m moved",
	} {
		t.Run(name, func(t *testing.T) {
			repairEnv(t, []string{repairBlocked(t, "Retry once on a timeout.", "a.txt:1 missing retry", nil)}, nil)
			runDir, _, got, _, err := reviewRun(t, repairATM(), "FAKE_REVIEW_WORK", work)
			notDelivered(t, err, runDir, got, "contract reviewer")
			if repairLogs(t, runDir) != 0 {
				t.Fatal("mutating reviewer triggered repair")
			}
		})
	}
	for _, invalid := range []string{"reason", "fields", "draft"} {
		t.Run("invalid slicing/"+invalid, func(t *testing.T) {
			s := slicingReport()
			switch invalid {
			case "reason":
				s["reason"] = ""
			case "fields":
				s["fields"].(obj)["invariants"].(obj)["reason"] = ""
			case "draft":
				s["drafts"] = []obj{{"purpose": "optional", "cause": "missing contract"}}
			}
			repairEnv(t, []string{repairBlocked(t, "Retry once on a timeout.", "a.txt:1 missing retry", s)}, nil)
			runDir, _, got, _, err := reviewRun(t, repairATM())
			notDelivered(t, err, runDir, got, "invalid task_slicing")
			if repairLogs(t, runDir) != 0 {
				t.Fatal("invalid reviewer report triggered repair")
			}
		})
	}
}

func assertRepairAttemptEvidence(t *testing.T, evidence repairEvidence) {
	t.Helper()
	for n, attempt := range evidence.Attempts {
		want := []string{"fixed\nround-one", "fixed\nround-two"}[n]
		if len(attempt.Reproofs) != 1 || attempt.Reproofs[0].Green.Tail != want || attempt.Head != evidence.Heads[n+1] {
			t.Fatalf("round %d lost its proof or head: %+v", n+1, attempt)
		}
		if len(attempt.Commands) != 3 || attempt.Commands[2].Name != "lint" || attempt.Commands[2].Tail != want {
			t.Fatalf("round %d lost its checks: %+v", n+1, attempt.Commands)
		}
	}
}

func assertRepairTypeProof(t *testing.T, attempt repairAttempt, typ string) {
	t.Helper()
	if len(attempt.Reproofs) != 1 || attempt.Reproofs[0].Type != typ ||
		attempt.Reproofs[0].TestFile != "" || len(attempt.Commands) != 3 {
		t.Fatalf("type-specific proof or checks missing: %+v", attempt)
	}
	if typ == "refactor" && (attempt.Reproofs[0].Red.Command == "" || attempt.Reproofs[0].Red.Exit != 0) {
		t.Fatalf("refactor lost its base-suite proof: %+v", attempt)
	}
}

func assertRepairRetainedFindings(t *testing.T, runDir, got, call string, approve bool) {
	t.Helper()
	if !approve {
		if _, err := os.Stat(filepath.Join(got, "calls")); err == nil {
			t.Fatal("blocked run delivered")
		}
	}
	var review struct{ Repair repairEvidence }
	if err := json.Unmarshal(mustRead(t, filepath.Join(runDir, "contract-review.json")), &review); err != nil {
		t.Fatal(err)
	}
	owners := 3
	if approve {
		owners = 2
	}
	if len(review.Repair.Optional) != 3 || len(review.Repair.Unmatched) != owners {
		t.Fatalf("lost optional drafts or owner findings: %+v", review.Repair)
	}
	for n, draft := range review.Repair.Optional {
		if draft.(map[string]any)["contract"].(map[string]any)["goal"] != "Optional "+strconv.Itoa(n+1) {
			t.Fatalf("lost round %d optional draft", n+1)
		}
	}
	for n, finding := range review.Repair.Unmatched {
		if finding.Criterion != "Owner "+strconv.Itoa(n+1) || finding.Disposition != "owner" {
			t.Fatalf("lost round %d owner finding: %+v", n+1, finding)
		}
	}
	for n := 1; n <= 2; n++ {
		brief := agentCall(t, call+".repair"+strconv.Itoa(n)).Stdin
		if strings.Contains(brief, "Owner ") || strings.Contains(brief, "Optional ") {
			t.Fatalf("unrelated work sent to repair: %s", brief)
		}
	}
}
