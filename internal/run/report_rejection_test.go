package run

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

func TestReportRejectionPersistsAcrossConsumers(t *testing.T) {
	for _, name := range []string{"worker", "ponytail", "contract-review", "scope-review-unit2", "repair-1"} {
		for _, outcome := range []string{"valid", "returned", "invalid twice", "process failure"} {
			t.Run(name+"/"+outcome, func(t *testing.T) {
				root := repo(t, "https://example.com/owner/repo.git", "")
				head := gitT(t, root, "rev-parse", "HEAD")
				a := agent{Agent: config.Agent{Harness: "claude"}, root: root, dir: t.TempDir()}
				valid := validWorker
				wantError := noJSON
				switch name {
				case "ponytail":
					valid = `{"findings":[],"summary":"s"}`
				case "contract-review":
					valid = strings.NewReplacer("{base}", head, "{head}", head).Replace(defaultReview)
					wantError = "agent left no report: invalid contract review: want a single JSON object"
				case "scope-review-unit2":
					valid = `{"disposition":"approved","paths":["a.txt"],"findings":[],"summary":"s"}`
					wantError = "agent left no report: invalid scope review: want a single JSON object"
				}
				first, second := "not json", valid
				if outcome == "valid" {
					first = valid
				}
				if outcome == "invalid twice" {
					second = "still not json"
				}
				rejectionHarness(t, first, second, outcome == "process failure")
				err := runRejectionConsumer(t, a, root, head, name, outcome, wantError)
				wantFailure := outcome == "invalid twice" || outcome == "process failure"
				if (err != nil) != wantFailure {
					t.Fatalf("outcome %s: %v", outcome, err)
				}
				if outcome == "process failure" && !strings.Contains(err.Error(), "exit code 3") {
					t.Fatalf("want continuation process failure, got %v", err)
				}
				assertRejectionLogs(t, a, name, outcome, wantError)
			})
		}
	}
}

func runRejectionConsumer(t *testing.T, a agent, root, head, name, outcome, wantError string) error {
	t.Helper()
	var err error
	switch name {
	case "contract-review":
		err = a.contractReview(root, head, head, "", Issue{Text: reviewIssue}, obj{})
	case "scope-review-unit2":
		err = a.scopeReview(root, head, 2, Issue{Text: reviewIssue}, followUp{Paths: []string{"a.txt"}},
			&scopeReview{Paths: []string{"a.txt"}})
	case "repair-1":
		err = a.repairWork(root, head, config.Config{}, 1, nil)
	case "ponytail":
		_, err = a.run(root, name, "ponytail-review", "brief", func(r obj) error {
			_, err := findings(r, nil)
			return err
		})
	default:
		var ev obj
		ev, err = a.run(root, name, "ponytail", "brief", testFile)
		if outcome != "valid" && ev["rejected"] != wantError {
			t.Fatalf("rejection metadata lost: %v", ev)
		}
	}
	return err
}

func assertRejectionLogs(t *testing.T, a agent, name, outcome, wantError string) {
	t.Helper()
	log, err := os.ReadFile(filepath.Join(a.dir, name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rejections, results := 0, 0
	dec := json.NewDecoder(bytes.NewReader(log))
	for dec.More() {
		var value any
		if err := dec.Decode(&value); err != nil {
			t.Fatal(err)
		}
		ev, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if ev["type"] == "report_rejected" {
			rejections++
			if ev["error"] != wantError || results != 1 {
				t.Fatalf("rejection must follow the original report with its exact error: %v", ev)
			}
		}
		if ev["type"] == "result" {
			results++
		}
	}
	wantRejections, wantResults, wantStderr := 1, 2, "first\nsecond\n"
	switch outcome {
	case "valid":
		wantRejections, wantResults, wantStderr = 0, 1, "first\n"
	case "process failure":
		wantResults = 1
	}
	stderr, err := os.ReadFile(filepath.Join(a.dir, name+".stderr.txt"))
	if err != nil || rejections != wantRejections || results != wantResults || string(stderr) != wantStderr {
		t.Fatalf("lost attempt evidence: rejections %d, reports %d, stderr %q, error %v", rejections, results, stderr, err)
	}
}

func rejectionHarness(t *testing.T, first, second string, fail bool) {
	t.Helper()
	var initial, returned bytes.Buffer
	enc := json.NewEncoder(&initial)
	_ = enc.Encode(obj{"type": "system", "session_id": "session-1"})
	for _, skill := range []string{"ponytail", "ponytail-review"} {
		_ = enc.Encode(obj{"type": "assistant", "message": obj{"content": []obj{
			{"type": "tool_use", "id": skill, "name": "Skill", "input": obj{"skill": skill}},
		}}})
		_ = enc.Encode(obj{"type": "user", "message": obj{"content": []obj{
			{"type": "tool_result", "tool_use_id": skill, "is_error": false},
		}}})
	}
	_ = enc.Encode(obj{"type": "result", "result": first})
	_ = json.NewEncoder(&returned).Encode(obj{"type": "result", "result": second})
	t.Setenv("REJECTION_FIRST", initial.String())
	t.Setenv("REJECTION_SECOND", returned.String())
	t.Setenv("REJECTION_FAIL", "")
	if fail {
		t.Setenv("REJECTION_FAIL", "yes")
	}
	bin := t.TempDir()
	script := `#!/bin/sh
brief=$(cat)
case "$brief" in
  'Your final message was rejected:'*)
    echo second >&2
    [ "$REJECTION_FAIL" = yes ] && exit 3
    printf '%s' "$REJECTION_SECOND"
    ;;
  *)
    echo first >&2
    printf '%s' "$REJECTION_FIRST"
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The native fake invokes sh explicitly: Windows cannot execute this script via PATH.
	t.Setenv("REJECTION_SCRIPT", filepath.Join(bin, "claude"))
}

func TestReportReturnRequiresNativeSession(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "pi", "cursor"} {
		for _, stream := range []string{
			"", `{"sessionId":"wrong"}`, `{"msg":{"session_id":"wrong"}}`,
			`{"payload":{"thread_id":"wrong"}}`, `{"type":"message","id":"wrong"}`,
		} {
			t.Run(harness+"/"+stream, func(t *testing.T) {
				root, order := nativeReportSession(t, stream)
				a := agent{Agent: config.Agent{Harness: harness}, root: root, dir: t.TempDir()}
				ev, err := a.run(root, "worker", "ponytail", "brief", testFile)
				if err == nil || !strings.Contains(err.Error(), noJSON) || ev["rejected"] != noJSON || workerCalls(t, order) != 1 {
					t.Fatalf("must fail without returning to an unknown session: %v, %v", ev, err)
				}
			})
		}
	}
}

func TestReportReturnUsesNativeSession(t *testing.T) {
	for harness, stream := range map[string]string{
		"claude": `{"session_id":"sess-1"}`,
		"codex":  `{"type":"thread.started","thread_id":"sess-1"}`,
		"pi":     `{"type":"session","id":"sess-1"}`,
		"cursor": `{"session_id":"sess-1"}`,
	} {
		t.Run(harness, func(t *testing.T) {
			root, order := nativeReportSession(t, stream)
			call := filepath.Join(t.TempDir(), "call.json")
			t.Setenv("FAKE_AGENT_CALL", call)
			a := agent{Agent: config.Agent{Harness: harness, Model: "m", Effort: "e"}, root: root, dir: t.TempDir()}
			ev, err := a.run(root, "worker", "ponytail", "brief", testFile)
			if err != nil || ev["rejected"] != noJSON || workerCalls(t, order) != 2 {
				t.Fatalf("native session did not continue once: %v, %v", ev, err)
			}
			got := agentCall(t, call)
			if !slices.Contains(got.Args, "sess-1") {
				t.Fatalf("missing continuation arguments: %v", got.Args)
			}
			assertResumeArgv(t, harness, got.Args)
		})
	}
}

func nativeReportSession(t *testing.T, stream string) (root, order string) {
	t.Helper()
	root = repo(t, "https://example.com/owner/repo.git", "")
	order = filepath.Join(t.TempDir(), "order")
	t.Setenv("FAKE_AGENT_ORDER", order)
	t.Setenv("FAKE_AGENT_STREAM", stream+"\n")
	t.Setenv("FAKE_AGENT_WORK", reportWork)
	t.Setenv("FAKE_REPORT_1", "not json")
	t.Setenv("FAKE_REPORT_2", validWorker)
	return root, order
}
