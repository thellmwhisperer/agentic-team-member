//go:build unix

package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
	"gopkg.in/yaml.v3"
)

// Decode non-strictly so the base reaches the row assertion even before it knows e2e.
func TestChecksE2ERow(t *testing.T) {
	top := repo(t, "local", atmYAML)
	var c config.Config
	if err := yaml.Unmarshal([]byte(atmYAML+"e2e: echo uncovered.go:42; exit 1\n"), &c); err != nil {
		t.Fatal(err)
	}
	if err := before(top); err != nil {
		t.Fatal(err)
	}
	ev, err := checks(top, t.TempDir(), gitT(t, top, "rev-parse", "HEAD"), "chore", "", c)
	cmds, _ := ev["commands"].([]cmdResult)
	if err == nil || len(cmds) != 2 || cmds[0].Name != "test" || cmds[0].Result != "passed" ||
		cmds[1].Name != "e2e" || cmds[1].Result != "failed" || cmds[1].Tail != "uncovered.go:42" {
		t.Fatalf("want passed test then failed e2e with uncovered lines; commands=%+v, err=%v", cmds, err)
	}
}

func TestRunE2EResume(t *testing.T) {
	bin := builtATM(t)
	for _, tc := range []e2eResumeCase{
		{"green", "claude", "touch reached", "e2e", ""},
		{"codex", "codex", "touch reached", "e2e", ""},
		{"red_again", "claude", "true", "e2e", ""},
		{"refactor", "claude", "true", "e2e", ""},
		{"tests", "claude", "true", "e2e", ""},
		{"commits", "claude", "git add a.txt; git commit -qm forbidden", "e2e", ""},
		{"test", "claude", "true", "test", "failed row is test"},
		{"missing_session", "claude", "true", "e2e", "no saved session id"},
		{"unsupported", "opencode", "true", "e2e", "harness opencode cannot resume"},
		{"no_e2e", "claude", "true", "lint", "failed row is lint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top, call, order, path := e2eFixture(t, tc)
			if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
				t.Fatal(err)
			}
			server := startVersionServe(t, bin, top)
			// A graceful exit flushes the real binary's coverage counters.
			defer func() { _ = os.Remove(socket(top)); _ = server.Wait() }()
			l := lease{PID: os.Getpid(), Started: time.Now(), Heartbeat: time.Now()}
			if err := l.write(top, "e2e-test"); err != nil {
				t.Fatal(err)
			}
			launch := func(args ...string) string {
				t.Helper()
				cmd := exec.Command(bin, append([]string{"run", "--watcher", "e2e-test", "--harness", tc.harness}, args...)...)
				cmd.Dir = top
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("launch: %s, %v", out, err)
				}
				return strings.TrimSpace(string(out))
			}
			label := launch(path)
			end, err := Attach(top, label, &bytes.Buffer{})
			if err != nil || end.FailedNode != "checks" || !strings.Contains(end.Reason, tc.row+":") {
				t.Fatalf("first run: %+v, %v", end, err)
			}
			r := e2eReport(t, top, label)
			if !r.Resumable || r.ResumableUnit != 1 {
				t.Fatalf("e2e red is not resumable: %+v", r)
			}
			e2eRows(t, r, tc.row, tc.name != "no_e2e")
			label = followE2EResume(t, bin, top, label, tc)
			var screen bytes.Buffer
			end, err = Attach(top, label, &screen)
			if err != nil {
				t.Fatal(err)
			}
			r = e2eReport(t, top, label)
			assertE2ETurn(t, tc, call, order, screen.String())
			assertE2EResult(t, tc, r, end)
		})
	}
}

type e2eResumeCase struct{ name, harness, repair, row, skip string }

func e2eFixture(t *testing.T, tc e2eResumeCase) (top, call, order, path string) {
	t.Helper()
	top = repo(t, "local", tc.config())
	call, order = filepath.Join(t.TempDir(), "call"), filepath.Join(t.TempDir(), "order")
	t.Setenv("FAKE_AGENT_CALL", call)
	t.Setenv("FAKE_AGENT_ORDER", order)
	if tc.name != "missing_session" {
		t.Setenv("FAKE_AGENT_STREAM", `{"type":"session","id":"sess-1","session_id":"sess-1","thread_id":"sess-1"}`+"\n")
	}
	typ, initial, marker := "fix", fixWork, "first-turn"
	switch tc.name {
	case "refactor":
		typ, initial = "refactor", "true"
	case "tests":
		typ, initial, marker = "tests", "true", "first_turn_test.sh"
	}
	// A tracked marker distinguishes the restored work from the initial turn.
	t.Setenv("FAKE_AGENT_WORK", "if test -f "+marker+"; then "+tc.repair+"; else "+initial+"; touch "+marker+"; fi")
	return top, call, order, issueFile(t, strings.Replace(issue, "Type: fix", "Type: "+typ, 1))
}

func (tc e2eResumeCase) config() string {
	cfg := atmSet(atmSet(atmYAML, "typecheck", "true"), "lint", "true")
	if tc.name == "no_e2e" {
		return atmSet(cfg, "lint", "exit 1")
	}
	cfg += "e2e: 'test -f \"$GOCOVERDIR/test-ran\" && test -f reached || { echo uncovered.go:42; exit 1; }'\n"
	cfg = atmSet(cfg, "test", `sh a_test.sh && test -d "$GOCOVERDIR" && `+
		`test ! -f "$GOCOVERDIR/test-ran" && touch "$GOCOVERDIR/test-ran"`)
	if tc.name == "refactor" {
		cfg = atmSet(cfg, "test", `sh a_test.sh && if [ -n "$GOCOVERDIR" ]; then test -d "$GOCOVERDIR" && `+
			`test ! -f "$GOCOVERDIR/test-ran" && touch "$GOCOVERDIR/test-ran"; fi`)
	}
	if tc.row == "test" {
		cfg = atmSet(cfg, "test", "echo test-red; exit 1")
	}
	return cfg
}

func assertE2ETurn(t *testing.T, tc e2eResumeCase, call, order, screen string) {
	t.Helper()
	wantCalls := 2
	if tc.skip != "" {
		wantCalls = 1
		if want := "resume: no agent turn (" + tc.skip + ")"; !strings.Contains(screen, want) {
			t.Fatalf("screen missing %q: %s", want, screen)
		}
	} else {
		got := agentCall(t, call)
		want := "Checks failed at e2e: these changed lines are not executed by any end-to-end run of the real binary. " +
			"Delete them, or write the end-to-end test that reaches them from a real command. " +
			"Do not touch anything else.\nuncovered.go:42"
		if sentPrompt(tc.harness, got) != want || !slices.Contains(got.Args, "sess-1") {
			t.Fatalf("resume turn: %+v", got)
		}
		if !strings.Contains(screen, "agent turn after e2e") {
			t.Fatalf("missing turn event: %s", screen)
		}
	}
	if n := workerCalls(t, order); n != wantCalls {
		t.Fatalf("worker calls=%d, want %d", n, wantCalls)
	}
}

func assertE2EResult(t *testing.T, tc e2eResumeCase, r *verdict, end Outcome) {
	t.Helper()
	switch tc.name {
	case "green", "codex":
		if r.node("checks").Result != "passed" {
			t.Fatalf("resumed checks: %+v", end)
		}
		e2eRows(t, r, "", true)
	case "commits":
		if !strings.Contains(end.Reason, "made commits") {
			t.Fatalf("commit accepted: %+v", end)
		}
	default:
		if end.FailedNode != "checks" || !strings.Contains(end.Reason, tc.row+":") {
			t.Fatalf("second red: %+v", end)
		}
	}
}

// The real --follow sees exactly one turn start, before the resumed Checks.
func followE2EResume(t *testing.T, bin, top, label string, tc e2eResumeCase) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "--follow", "--resume", label)
	cmd.Dir = top
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, _ := cmd.Output() // No delivery means no PR: even a green run ends follow with 1.
	turns, checks := 0, 0
	var resumed string
	var message string
	for _, ev := range events(t, bytes.NewReader(out)) {
		resumed, _ = ev["run"].(string)
		if ev["step"] == "resume" {
			message, _ = ev["message"].(string)
		}
		if ev["step"] == "agent turn after e2e" {
			turns++
			if checks != 0 || ev["harness"] != tc.harness {
				t.Fatalf("turn after Checks or wrong harness: %s", out)
			}
		}
		if ev["event"] == "step" && ev["step"] == "checks" {
			checks++
		}
	}
	wantTurns := 1
	if tc.skip != "" {
		wantTurns = 0
		if message != "resume: no agent turn ("+tc.skip+")" {
			t.Fatalf("missing skip message: %s", out)
		}
	}
	if resumed == "" || turns != wantTurns || checks != 1 {
		t.Fatalf("follow resume: %s; stderr: %s", out, &stderr)
	}
	return resumed
}

func TestResumeE2EAgentTurn(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "pi", "cursor", "opencode"} {
		for _, repair := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/repair=%t", harness, repair), func(t *testing.T) {
				top := repo(t, "local", atmYAML+"e2e: 'test -f reached || { echo uncovered.go:42; exit 1; }'\n")
				call, order := filepath.Join(t.TempDir(), "call"), filepath.Join(t.TempDir(), "order")
				t.Setenv("FAKE_AGENT_CALL", call)
				t.Setenv("FAKE_AGENT_ORDER", order)
				t.Setenv("FAKE_AGENT_STREAM", `{"type":"session","id":"sess-1","session_id":"sess-1","thread_id":"sess-1"}`+"\n")
				t.Setenv("FAKE_AGENT_WORK", fixWork)
				var first bytes.Buffer
				err := Run("failed", []string{"--harness", harness, issueFile(t, issue)}, &first, io.Discard)
				if err == nil || !strings.Contains(err.Error(), "e2e:") {
					t.Fatalf("first red: %v", err)
				}
				if repair {
					t.Setenv("FAKE_AGENT_WORK", "touch reached")
				}
				var resumed bytes.Buffer
				err = Run("resumed", []string{"--resume", "failed"}, &resumed, io.Discard)
				wantGreen := repair && harness != "opencode"
				if (err == nil) != wantGreen {
					t.Fatalf("resume: %v; events: %s", err, resumed.String())
				}
				wantCalls := 2
				if harness == "opencode" {
					wantCalls = 1
				}
				if calls := workerCalls(t, order); calls != wantCalls {
					t.Fatalf("worker turns=%d, want %d", calls, wantCalls)
				}
				e2eTurnEvents(t, &resumed, harness, wantCalls == 2)
				if r := e2eReport(t, top, "resumed"); r.node("checks").Result != outcome(err) {
					t.Fatalf("second Checks result: %+v", r)
				}
			})
		}
	}
}

func e2eTurnEvents(t *testing.T, out *bytes.Buffer, harness string, want bool) {
	t.Helper()
	var followed bytes.Buffer
	s := observer{w: &followed, now: time.Now}
	r := bgRun{}
	starts, ends := 0, 0
	for _, ev := range events(t, out) {
		b, _ := json.Marshal(ev)
		line := r.event(string(b))
		s.frame(Frame{Line: &line, Event: b})
		if ev["step"] != "agent turn after e2e" {
			continue
		}
		if ev["harness"] != harness {
			t.Fatalf("turn harness: %v", ev)
		}
		if ev["state"] == "started" {
			starts++
		} else {
			ends++
			if ms, ok := ev["duration_ms"].(float64); !ok || ms < 0 || ev["started_at"] == "" {
				t.Fatalf("turn timing missing: %v", ev)
			}
		}
	}
	wantCount := 0
	if want {
		wantCount = 1
	}
	if starts != wantCount || ends != wantCount || strings.Count(followed.String(), "agent turn after e2e") != wantCount {
		t.Fatalf("turn events=%d/%d; follow: %s", starts, ends, &followed)
	}
}

func TestE2EFixtureProofTypes(t *testing.T) {
	for _, typ := range []string{"refactor", "tests"} {
		t.Run(typ, func(t *testing.T) {
			tc := e2eResumeCase{name: typ, harness: "claude", repair: "true", row: "e2e"}
			top, _, _, path := e2eFixture(t, tc)
			for _, run := range []struct {
				label string
				args  []string
			}{
				{"first", []string{path}}, {"resumed", []string{"--resume", "first"}},
			} {
				if err := Run(run.label, run.args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "e2e:") {
					t.Fatalf("proof must pass before e2e red: %v", err)
				}
				e2eRows(t, e2eReport(t, top, run.label), "e2e", true)
			}
		})
	}
}

func TestResumeE2ESafeguards(t *testing.T) {
	for _, tc := range []struct{ name, mode, work, why string }{
		{"commit", "", "git add a.txt; git commit -qm forbidden", "made commits"},
		{"invalid_report", "no-report", "true", "e2e:"},
		{"saved_skill_proof", "skill-error", "touch reached", ""},
		{"missing_session", "", "true", "e2e:"},
		{"old_bundle", "", "true", "e2e:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top := repo(t, "local", atmYAML+"e2e: 'test -f reached || { echo uncovered.go:42; exit 1; }'\n")
			order := filepath.Join(t.TempDir(), "order")
			t.Setenv("FAKE_AGENT_ORDER", order)
			if tc.name != "missing_session" {
				t.Setenv("FAKE_AGENT_STREAM", `{"session_id":"sess-1"}`+"\n")
			}
			if err := Run("failed", []string{issueFile(t, issue)}, io.Discard, io.Discard); err == nil {
				t.Fatal("want first e2e red")
			}
			if tc.name == "old_bundle" {
				removeE2EResumeFields(t, top)
			}
			t.Setenv("FAKE_AGENT", tc.mode)
			t.Setenv("FAKE_AGENT_WORK", tc.work)
			var out bytes.Buffer
			err := Run("resumed", []string{"--resume", "failed"}, &out, io.Discard)
			if tc.why == "" && err != nil || tc.why != "" && (err == nil || !strings.Contains(err.Error(), tc.why)) {
				t.Fatalf("resume: %v; want %q", err, tc.why)
			}
			wantCalls := 2
			if tc.name == "missing_session" || tc.name == "old_bundle" {
				wantCalls = 1
				if !strings.Contains(out.String(), "resume: no agent turn (") {
					t.Fatalf("missing skip reason: %s", &out)
				}
			}
			if calls := workerCalls(t, order); calls != wantCalls {
				t.Fatalf("worker calls=%d, want %d", calls, wantCalls)
			}
			if tc.name == "invalid_report" {
				resumed := e2eReport(t, top, "resumed")
				if resumed.node("agent").Result != "passed" || resumed.node("checks").Result != "failed" ||
					resumed.FailedNode != "checks" || len(resumed.Units) != 1 || resumed.Units[0].TestFile != "a_test.sh" ||
					strings.Contains(err.Error(), "no JSON object") || strings.Contains(err.Error(), "agent left no report") {
					t.Fatalf("turn without worker JSON did not reach Checks with the saved report: %v; %+v", err, resumed)
				}
			}
		})
	}
}

func removeE2EResumeFields(t *testing.T, top string) {
	t.Helper()
	path := filepath.Join(runDir(top, "failed"), "resume", "resume.json")
	b, err := os.ReadFile(path)
	var saved map[string]any
	if err != nil || json.Unmarshal(b, &saved) != nil {
		t.Fatal("cannot read resume bundle", err)
	}
	for _, key := range []string{"session_id", "failed_row", "e2e_output", "skill_proved",
		"agent", "harness_args", "env"} {
		delete(saved, key)
	}
	b, _ = json.Marshal(saved)
	writeFixture(t, path, string(b))
}

func e2eReport(t *testing.T, top, label string) *verdict {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir(top, label), "report.json"))
	var r verdict
	if err != nil || json.Unmarshal(b, &r) != nil {
		t.Fatalf("report: %s, %v", b, err)
	}
	return &r
}

func e2eRows(t *testing.T, r *verdict, failed string, e2e bool) {
	t.Helper()
	want := []string{"test", "typecheck", "lint"}
	if e2e {
		want = []string{"test", "e2e", "typecheck", "lint"}
	}
	if failed != "" {
		want = want[:slices.Index(want, failed)+1]
	}
	var got []string
	for _, c := range r.Commands {
		got = append(got, c.Name)
		if c.StartedAt == "" || c.DurationMS < 0 {
			t.Fatalf("command without timing: %+v", c)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("checks order=%v, want %v", got, want)
	}
	if e2e && failed != "test" {
		rows := r.node("checks").Rows
		if len(rows) != 1 || rows[0].Name != "e2e" || rows[0].StartedAt == "" || rows[0].DurationMS < 0 {
			t.Fatalf("e2e row missing from checks node: %+v", rows)
		}
	}
}
