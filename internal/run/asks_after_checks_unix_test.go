//go:build unix

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunAsksAfterChecks: a ponytail step or delivery command that fails stops the run and asks, as a failed
// check does. Approve goes on, fix gives the worker one turn with the finding, stop keeps the clone and its work.
func TestRunAsksAfterChecks(t *testing.T) {
	builtATM(t)
	// Each step fails until the fix turn leaves repaired; want is in its finding, code is its exit code.
	cases := map[string]struct {
		ponytail, delivery, want string
		code                     int
	}{
		"ponytail": {"test -f repaired", "", "ponytail.jsonl", 2},
		"delivery": {"", "test -f repaired || { echo delivery-finding; exit 1; }; ", "delivery-finding", 4},
	}
	for step, c := range cases {
		for _, answer := range []string{"approve", "fix", "stop"} {
			t.Run(step+"/"+answer, func(t *testing.T) {
				delivered, call := filepath.Join(t.TempDir(), "delivered"), filepath.Join(t.TempDir(), "call")
				for k, v := range map[string]string{"FAKE_PONYTAIL_WORK": c.ponytail, "FAKE_AGENT_CALL": call,
					"FAKE_AGENT_WORK": "test -f first-turn && touch repaired || { " + fixWork + "; touch first-turn; }"} {
					t.Setenv(k, v)
				}
				top := gitT(t, repo(t, "local", atmSet(atmYAML, "delivery", c.delivery+"pwd > "+delivered)),
					"rev-parse", "--show-toplevel")
				bin := followUpsServer(t, top)
				clone, label := question(t, bin, top, c.want, c.code, "run", "--follow", issueFile(t, issue))
				assertCheckWaiting(t, top, label)
				_, early := os.Stat(delivered)
				answerFailedCheck(t, bin, top, label, answer, map[bool]int{true: c.code}[answer == "stop"])
				if !os.IsNotExist(early) {
					t.Fatalf("delivered before the answer: %v", early)
				}
				assertAnsweredAfterChecks(t, top, label, clone, delivered, step, answer, c.want, call)
			})
		}
	}
}

func TestRunDeliveryFixRechecks(t *testing.T) {
	builtATM(t)
	for _, cuts := range []struct {
		name          string
		first, second bool
	}{
		{"without cuts", false, false},
		{"with cuts", true, true},
		{"first cut only", true, false},
		{"second cut only", false, true},
	} {
		for _, tc := range []struct{ name, repair, finding string }{
			{"passes", "touch repaired", ""},
			{"check fails", "touch repaired broken-check", "repair-check-finding"},
			{"agent commits", "touch repaired; git add -A; git commit -qm agent-commit", "the agent made commits"},
		} {
			t.Run(cuts.name+"/"+tc.name, func(t *testing.T) {
				runDeliveryFixRecheck(t, cuts.first, cuts.second, tc.repair, tc.finding)
			})
		}
	}
}

func runDeliveryFixRecheck(t *testing.T, first, second bool, repair, finding string) {
	delivered, order := filepath.Join(t.TempDir(), "delivered"), filepath.Join(t.TempDir(), "order")
	metadata := t.TempDir()
	t.Setenv("ATM_TEST_METADATA", metadata)
	cfg := atmSet(atmSet(atmYAML, "lint",
		"test ! -f broken-check || { echo repair-check-finding; exit 1; }"), "delivery",
		`test -f repaired && attempt=second || attempt=first; `+
			`for field in ATM_PONYTAIL ATM_INTENT ATM_ISSUE_TEXT; do `+
			`printenv "$field" > "$ATM_TEST_METADATA/$attempt-$field"; done; `+
			`git ls-files .slop/tombstones > "$ATM_TEST_METADATA/$attempt-tombs"; `+
			"test -f repaired || { echo delivery-finding; exit 1; }; cat a.txt > "+delivered)
	top := gitT(t, repo(t, "local", cfg), "rev-parse", "--show-toplevel")
	base := gitT(t, top, "rev-parse", "HEAD")
	work := deliveryCutWork(t, first, second)
	t.Setenv("FAKE_AGENT_ORDER", order)
	if first && second {
		repair = "sleep 1; " + repair
	}
	t.Setenv("FAKE_AGENT_WORK", "if test -f first-turn; then "+repair+"; else "+work+"; touch first-turn; fi")
	bin := followUpsServer(t, top)
	clone, label := question(t, bin, top, "delivery-finding", 4, "run", "--follow", issueFile(t, issue))
	assertDeliveryCutMetadata(t, metadata, "first", map[bool]int{true: 1}[first])
	before := e2eReport(t, top, label)
	if before.HeadSHA == base || before.HeadSHA != gitT(t, clone, "rev-parse", "HEAD") {
		t.Fatalf("delivery did not record ATM's commits: %+v", before)
	}
	answerFailedCheck(t, bin, top, label, "fix", map[bool]int{true: 1}[finding != ""])
	rep := e2eReport(t, top, label)
	if workerCalls(t, order) != 2 || len(rep.Decisions) == 0 || rep.Decisions[0].Answer != "fix" ||
		!strings.Contains(rep.Decisions[0].Finding, "delivery-finding") ||
		len(rep.Units) != 1 || rep.Units[0].Base != base {
		t.Fatalf("delivery fix lost its one turn, decision, or original base: %+v", rep)
	}
	if finding != "" {
		assertDeliveryRepairQuestion(t, bin, top, label, delivered, finding, rep)
		return
	}
	assertDeliveryRepairSuccess(t, delivered, order, metadata, base, first, second, rep)
}

func deliveryCutWork(t *testing.T, first, second bool) string {
	t.Helper()
	if !first && !second {
		return fixWork
	}
	firstWork, secondWork := ":", ":"
	if first {
		firstWork = "printf 'fixed\\nextra\\n' > a.txt"
	}
	if second {
		secondWork = "echo fixed > a.txt"
	}
	t.Setenv("FAKE_PONYTAIL_WORK", "if test -f repaired; then "+secondWork+"; else "+firstWork+"; fi")
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	return "printf 'fixed\\nextra\\nmore\\n' > a.txt" + fixTest
}

func assertDeliveryRepairQuestion(t *testing.T, bin, top, label, delivered, finding string, rep *verdict) {
	t.Helper()
	if rep.FailedNode != "checks" || len(rep.Decisions) != 2 || rep.Decisions[1].Answer != "" ||
		!strings.Contains(rep.Decisions[1].Finding, finding) {
		t.Fatalf("delivery repair did not ask about its new failure: %+v", rep)
	}
	if _, err := os.Stat(delivered); !os.IsNotExist(err) {
		t.Fatalf("repair delivered before the check answer: %v", err)
	}
	assertQuestionNextStep(t, top, label, true)
	answerFailedCheck(t, bin, top, label, "stop", 1)
}

func assertDeliveryRepairSuccess(t *testing.T, delivered, order, metadata, base string,
	first, second bool, rep *verdict) {
	t.Helper()
	if len(rep.Decisions) != 1 || rep.FailedNode != "" || rep.node("checks").Result != "passed" ||
		rep.node("ponytail").Result != "passed" || rep.node("delivery").Result != "passed" {
		t.Fatalf("delivery repair did not pass all steps: %+v", rep)
	}
	if calls, err := os.ReadFile(order); err != nil || string(calls) != "worker\nponytail\nworker\nponytail\n" {
		t.Fatalf("repair did not rerun ponytail after its single fix turn: %q, %v", calls, err)
	}
	assertDeliveryCutMetadata(t, metadata, "second",
		map[bool]int{true: 1}[first]+map[bool]int{true: 1}[second])
	assertDeliveryRepairOutput(t, delivered, base, first, second, rep)
}

func assertDeliveryRepairOutput(t *testing.T, delivered, base string, first, second bool, rep *verdict) {
	t.Helper()
	want := "fixed\n"
	if first && !second {
		want += "extra\n"
	}
	if data, err := os.ReadFile(delivered); err != nil || string(data) != want {
		t.Fatalf("delivery lost the repair or the second cut of committed work: %q, %v", data, err)
	}
	if second && (len(rep.Reproofs) != 1 || rep.Reproofs[0].Base != base || rep.Reproofs[0].Result != "passed") {
		t.Fatalf("second cut lost its proof against the original base: %+v", rep.Reproofs)
	}
}

func assertDeliveryCutMetadata(t *testing.T, dir, attempt string, cuts int) {
	t.Helper()
	read := reader(t, dir)
	ponytail, intent, issue := read(attempt+"-ATM_PONYTAIL"), read(attempt+"-ATM_INTENT"), read(attempt+"-ATM_ISSUE_TEXT")
	want := strings.TrimSpace(strings.Repeat("- a.txt: dropped two lines (speculative_feature)\n", cuts))
	if ponytail != want {
		t.Fatalf("%s delivery findings = %q, want %q", attempt, ponytail, want)
	}
	tombs := strings.Fields(read(attempt + "-tombs"))
	if len(tombs) != cuts {
		t.Fatalf("%s delivery has %d tombstones, want %d: %v", attempt, len(tombs), cuts, tombs)
	}
	if cuts == 0 {
		if intent != issue {
			t.Fatalf("%s delivery without cuts changed the issue: %q", attempt, intent)
		}
		return
	}
	if !strings.HasPrefix(intent, issue+"\n\n## Files ATM adds\n") {
		t.Fatalf("%s delivery lost its issue or ATM file section: %q", attempt, intent)
	}
	for _, tomb := range tombs {
		if strings.Count(intent, "`"+tomb+"`") != 1 {
			t.Fatalf("%s delivery intent must name tombstone %s exactly once: %q", attempt, tomb, intent)
		}
	}
}

func assertAnsweredAfterChecks(t *testing.T, top, label, clone, delivered, step, answer, want, call string) {
	t.Helper()
	rep := e2eReport(t, top, label)
	if len(rep.Decisions) != 1 || rep.Decisions[0].Answer != answer ||
		!strings.Contains(rep.Decisions[0].Finding, want) {
		t.Fatalf("answer not recorded with finding: %+v", rep.Decisions)
	}
	data, err := os.ReadFile(delivered)
	if (answer == "fix" || answer == "approve" && step == "ponytail") != (err == nil) ||
		err == nil && strings.TrimSpace(string(data)) != clone {
		t.Fatalf("%s after a failed %s delivered %q, %v", answer, step, data, err)
	}
	switch answer {
	case "stop":
		work, err := os.ReadFile(filepath.Join(clone, "a.txt"))
		if rep.FailedNode != step || err != nil || string(work) != "fixed\n" {
			t.Fatalf("stop lost the failed %s or its work: %q, %v, %+v", step, work, err, rep)
		}
	case "fix":
		if got := agentCall(t, call); !strings.Contains(got.Stdin+strings.Join(got.Args, " "), want) {
			t.Fatalf("fix turn did not receive finding: %+v", got)
		}
	}
}
