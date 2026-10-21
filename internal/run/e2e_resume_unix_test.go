//go:build unix

package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	ev, err := checks(top, t.TempDir(), gitT(t, top, "rev-parse", "HEAD"), "", nil, false, c)
	cmds, _ := ev["commands"].([]cmdResult)
	if err == nil || len(cmds) != 2 || cmds[0].Name != "test" || cmds[0].Result != "passed" ||
		cmds[1].Name != "e2e" || cmds[1].Result != "failed" || cmds[1].Tail != "uncovered.go:42" {
		t.Fatalf("want passed test then failed e2e with uncovered lines; commands=%+v, err=%v", cmds, err)
	}
}

func TestRealRunChecksRestoresATMDirectory(t *testing.T) {
	builtATM(t)
	bin := filepath.Join(t.TempDir(), "atm")
	buildATM(t, bin, "follow-test")
	cfg := atmSet(atmYAML, "test", "test ! -e .atm && sh a_test.sh")
	cfg = atmSet(cfg, "delivery", `test "$(cat .atm/marker)" = retained && echo restored`)
	top := repo(t, t.TempDir(), cfg)
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_AGENT_WORK", fixWork+`; mkdir -p .atm; printf retained > .atm/marker`)
	server := startVersionServe(t, bin, top)
	defer endServe(bin, top, server)
	startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue)).exit(0)
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) != 1 || runs[0].Outcome != "passed" {
		t.Fatalf("Checks did not hide and restore .atm: %+v, %v", runs, err)
	}
	output, err := os.ReadFile(filepath.Join(runDir(top, runs[0].Run), "delivery-output.txt"))
	if err != nil || strings.TrimSpace(string(output)) != "restored" {
		t.Fatalf("delivery did not see restored .atm/marker: %q, %v", output, err)
	}
	t.Logf("atm run %s outcome=%s; configured test saw no .atm; delivery saw restored marker",
		runs[0].Run, runs[0].Outcome)
}

func TestRunE2EResume(t *testing.T) {
	builtATM(t) // before repo moves HOME, where go build would fill a module cache
	for _, tc := range []e2eResumeCase{
		{"green", "claude", "touch reached", "e2e", ""},
		{"codex", "codex", "touch reached", "e2e", ""},
		{"red_again", "claude", "true", "e2e", ""},
		{"refactor", "claude", "true", "e2e", ""},
		{"tests", "claude", "true", "e2e", ""},
		{"commits", "claude", "git add a.txt; git commit -qm forbidden", "e2e", ""},
		// The resumed agent leaves ATM's directory in its clone and Checks' artefact path in its run taken.
		{"stash", "claude", `mkdir -p .atm "$TMPDIR/../checks-atm"`, "e2e", ""},
		{"test", "claude", "true", "test", "failed row is test"},
		{"missing_session", "claude", "true", "e2e", "no saved session id"},
		{"unsupported", "opencode", "true", "e2e", "harness opencode cannot resume"},
		{"no_e2e", "claude", "true", "lint", "failed row is lint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			top, call, order, path := e2eFixture(t, tc)
			bin := filepath.Join(t.TempDir(), "atm") // its own file, for endServe to change
			buildATM(t, bin, "follow-test")
			t.Setenv("FAKE_WORKER_TMPDIR", "1")
			if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
				t.Fatal(err)
			}
			server := startVersionServe(t, bin, top)
			// A graceful exit flushes the real binary's coverage counters.
			defer endServe(bin, top, server)
			launch := func(args ...string) string {
				t.Helper()
				cmd := exec.Command(bin, append([]string{"run", "--harness", tc.harness}, args...)...)
				cmd.Dir = top
				out, err := stdout(cmd)
				if err != nil {
					t.Fatalf("launch: %s, %v", out, err)
				}
				return strings.TrimSpace(string(out))
			}
			label := launch(path)
			end, err := Attach(top, label, &bytes.Buffer{})
			if err == nil && end.FailedNode == "checks" {
				end = stopCheckRun(t, top, label)
			}
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
			if err == nil && end.FailedNode == "checks" {
				end = stopCheckRun(t, top, label)
			}
			if err != nil {
				t.Fatal(err)
			}
			r = e2eReport(t, top, label)
			assertE2ETurn(t, tc, call, order, screen.String(), filepath.Join(runDir(top, label), "worker-scratch"))
			assertE2EResult(t, tc, r, end)
		})
	}
}

// endServe ends server, the real serve of bin at top, gracefully and at once: once its file changed, the next
// connection makes it exit, where a removed socket waits for its next tick, 5 s.
func endServe(bin, top string, server *exec.Cmd) {
	kill := time.AfterFunc(10*time.Second, func() { _ = server.Process.Kill() })
	defer kill.Stop()
	defer func() { _ = server.Wait() }()
	runs, _ := RecordedRuns(top)
	for _, r := range runs {
		c, err := net.DialTimeout("unix", short(socket(top)), time.Second)
		if err != nil {
			break
		}
		pid, err := servePID(c)
		if err != nil || pid != server.Process.Pid {
			_ = c.Close()
			return
		}
		_ = c.SetDeadline(time.Now().Add(time.Second))
		_, _ = exchange(c, request{Cmd: "answer", Run: r.Run, Answer: "stop"}, nil, func(reply) {})
		_ = c.Close()
	}
	later := time.Now().Add(time.Hour)
	err := os.Chtimes(bin, later, later)
	var c net.Conn
	if err == nil {
		c, err = net.Dial("unix", short(socket(top)))
	}
	if err == nil {
		_ = c.Close()
	} else {
		_ = os.Remove(socket(top))
	}
}

func stopCheckRun(t *testing.T, top, label string) Outcome {
	t.Helper()
	if err := Answer(top, label, "stop"); err != nil {
		t.Fatalf("stop pending check in %s: %v", label, err)
	}
	end, err := Attach(top, label, io.Discard)
	if err != nil || end.Outcome != "failed" || end.Ended.IsZero() {
		t.Fatalf("stopped check in %s did not finish: %+v, %v", label, end, err)
	}
	return end
}

type e2eResumeCase struct{ name, harness, repair, row, skip string }

func e2eFixture(t *testing.T, tc e2eResumeCase) (top, call, order, path string) {
	t.Helper()
	t.Setenv("ATM_TEST_INHERITED_GOCOVERDIR", os.Getenv("GOCOVERDIR"))
	// The real binary binds its serve at git's toplevel. t.TempDir can be another spelling
	// of that directory (Darwin's /var); Attach must use the same spelling or the socket path
	// does not fit sun_path.
	top = gitT(t, repo(t, "local", tc.config()), "rev-parse", "--show-toplevel")
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
		// The refactor proof runs test on the base with the caller's GOCOVERDIR, which a go test under
		// coverage sets and every run of the case shares; only Checks' own one holds the marker.
		cfg = atmSet(cfg, "test", `sh a_test.sh && if [ -n "$GOCOVERDIR" ] && `+
			`[ "$GOCOVERDIR" != "$ATM_TEST_INHERITED_GOCOVERDIR" ]; then `+
			`test -d "$GOCOVERDIR" && test ! -f "$GOCOVERDIR/test-ran" && touch "$GOCOVERDIR/test-ran"; fi`)
	}
	if tc.row == "test" {
		cfg = atmSet(cfg, "test", "echo test-red; exit 1")
	}
	return cfg
}

func assertE2ETurn(t *testing.T, tc e2eResumeCase, call, order, screen, scratch string) {
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
			"Do not touch anything else.\nuncovered.go:42" +
			"\nYour scratch directory for this turn is `" + scratch +
			"`, your `$TMPDIR`: use it for logs, outputs and files you set aside, instead of any previous scratch " +
			"or the system temp directory."
		if sentPrompt(tc.harness, got) != want || !slices.Contains(got.Args, "sess-1") {
			t.Fatalf("resume turn: %+v", got)
		}
		if !slices.Contains(got.Env, "TMPDIR="+scratch) {
			t.Fatalf("resumed worker did not receive its scratch: %v", got.Env)
		}
		if _, err := os.Stat(filepath.Join(scratch, "test.log")); err != nil {
			t.Fatalf("resumed worker did not write its scratch log: %v", err)
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
	case "stash":
		if end.FailedNode != "checks" || !strings.Contains(end.Reason, "checks artefact path already exists: ") {
			t.Fatalf("taken checks artefact path accepted: %+v", end)
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
	out, _ := cmd.Output() // The exit code is the run's; the report says how it ended.
	turns, checks, message := followE2EEvents(t, out, tc.harness)
	var resumed string
	runs, err := RecordedRuns(top)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Run != label {
			resumed = r.Run
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

// followE2EEvents counts the turn and Checks starts in --follow's output, and keeps the agent step's message.
func followE2EEvents(t *testing.T, out []byte, harness string) (turns, checks int, message string) {
	t.Helper()
	for _, ev := range events(t, bytes.NewReader(out)) {
		if ev["step"] == "agent" && ev["message"] != nil {
			message, _ = ev["message"].(string)
		}
		if ev["state"] != "started" || ev["check"] != nil {
			continue
		}
		if ev["step"] == "agent turn after e2e" {
			turns++
			if checks != 0 || ev["harness"] != harness {
				t.Fatalf("turn after Checks or wrong harness: %s", out)
			}
		}
		if ev["step"] == "checks" {
			checks++
		}
	}
	return turns, checks, message
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

func TestAgentScratchResume(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "pi", "cursor"} {
		t.Run(harness, func(t *testing.T) {
			top, call, _, path := e2eFixture(t, e2eResumeCase{name: "red_again", harness: harness, repair: "true", row: "e2e"})
			t.Setenv("FAKE_WORKER_TMPDIR", "1")
			originalTmp := t.TempDir()
			for n, label := range []string{"failed", "resumed", "repaired"} {
				args := []string{"--harness", harness, "--env", "TMPDIR=" + originalTmp, path}
				if n > 0 {
					args = []string{"--resume", []string{"failed", "resumed"}[n-1]}
				}
				if n == 2 {
					t.Setenv("FAKE_AGENT_WORK", "touch reached")
				}
				err := Run(label, args, io.Discard, io.Discard)
				if n < 2 && (err == nil || !strings.Contains(err.Error(), "e2e:")) || n == 2 && err != nil {
					t.Fatalf("%s: %v", label, err)
				}
				worker := agentCall(t, call)
				scratch := filepath.Join(runDir(top, label), "worker-scratch")
				if !slices.Contains(worker.Env, "TMPDIR="+scratch) ||
					!strings.Contains(sentPrompt(harness, worker), "`"+scratch+"`") {
					t.Fatalf("%s: scratch missing from worker environment or prompt: %+v", label, worker)
				}
				if _, err := os.Stat(filepath.Join(scratch, "test.log")); err != nil {
					t.Fatalf("%s: scratch log missing: %v", label, err)
				}
				if n < 2 {
					checkOriginalScratchEnv(t, top, label, originalTmp)
				}
			}
		})
	}
}

func checkOriginalScratchEnv(t *testing.T, top, label, originalTmp string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir(top, label), "resume", "resume.json"))
	var saved resumePoint
	if err != nil || json.Unmarshal(b, &saved) != nil ||
		!slices.Equal(saved.Env, []string{"TMPDIR=" + originalTmp}) {
		t.Fatalf("%s: original environment not preserved: %s; %v", label, b, err)
	}
}

func e2eTurnEvents(t *testing.T, out *bytes.Buffer, harness string, want bool) {
	t.Helper()
	starts, ends := 0, 0
	for _, ev := range events(t, out) {
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
	if starts != wantCount || ends != wantCount {
		t.Fatalf("turn events=%d/%d", starts, ends)
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
		{"missing_session", "", "true", "e2e:"},
		{"old_bundle", "", "true", "e2e:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resumeE2ESafeguard(t, tc.name, tc.mode, tc.work, tc.why)
		})
	}
}

func resumeE2ESafeguard(t *testing.T, name, mode, work, why string) {
	t.Helper()
	top := repo(t, "local", atmYAML+"e2e: 'test -f reached || { echo uncovered.go:42; exit 1; }'\n")
	order := filepath.Join(t.TempDir(), "order")
	t.Setenv("FAKE_AGENT_ORDER", order)
	if name != "missing_session" {
		t.Setenv("FAKE_AGENT_STREAM", `{"session_id":"sess-1"}`+"\n")
	}
	if err := Run("failed", []string{issueFile(t, issue)}, io.Discard, io.Discard); err == nil {
		t.Fatal("want first e2e red")
	}
	if name == "old_bundle" {
		removeE2EResumeFields(t, top)
	}
	t.Setenv("FAKE_AGENT", mode)
	t.Setenv("FAKE_AGENT_WORK", work)
	var out bytes.Buffer
	err := Run("resumed", []string{"--resume", "failed"}, &out, io.Discard)
	if resumeReasonMismatch(err, why) {
		t.Fatalf("resume: %v; want %q", err, why)
	}
	wantCalls := 2
	if skippedE2ETurn(name) {
		wantCalls = 1
		if !strings.Contains(out.String(), "resume: no agent turn (") {
			t.Fatalf("missing skip reason: %s", &out)
		}
	}
	if calls := workerCalls(t, order); calls != wantCalls {
		t.Fatalf("worker calls=%d, want %d", calls, wantCalls)
	}
	if name == "invalid_report" {
		assertSavedReportReachedChecks(t, err, e2eReport(t, top, "resumed"))
	}
}

func resumeReasonMismatch(err error, why string) bool {
	return why == "" && err != nil || why != "" && (err == nil || !strings.Contains(err.Error(), why))
}

func skippedE2ETurn(name string) bool {
	return name == "missing_session" || name == "old_bundle"
}

func assertSavedReportReachedChecks(t *testing.T, err error, resumed *verdict) {
	t.Helper()
	if resumed.node("agent").Result != "passed" || resumed.node("checks").Result != "failed" ||
		resumed.FailedNode != "checks" || len(resumed.Units) != 1 || resumed.Units[0].TestFile != "a_test.sh" ||
		strings.Contains(err.Error(), "no JSON object") || strings.Contains(err.Error(), "agent left no report") {
		t.Fatalf("turn without worker JSON did not reach Checks with the saved report: %v; %+v", err, resumed)
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
	for _, key := range []string{"session_id", "failed_row", "e2e_output",
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
