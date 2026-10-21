//go:build unix

package run

import (
	"encoding/json"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withoutFollowUpsNote is the gap the historical agent leaves unfixed, as a note in its report.
const withoutFollowUpsNote = "The retry error is not reported yet."

func followUpsServer(t *testing.T, top string) string {
	t.Helper()
	return versionServer(t, top, "follow-test")
}

// versionServer is a copy of the atm of version, its background process serving top until the test ends.
func versionServer(t *testing.T, top, version string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(top, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "atm")
	buildATM(t, bin, version)
	server := startVersionServe(t, bin, top)
	t.Cleanup(func() { endServe(bin, top, server) })
	return bin
}

// A run kept at a later unit before the retirement is refused,
// before ATM restores its work or runs anything, its saved files left byte for byte. A saved first unit still
// resumes under configured checks, protected tests and commits.
func TestResumeHistoricalUnit(t *testing.T) {
	lint, delivered, call := filepath.Join(t.TempDir(), "lint"), filepath.Join(t.TempDir(), "delivered"),
		filepath.Join(t.TempDir(), "call")
	t.Setenv("ATM_TEST_LINT", lint)
	t.Setenv("ATM_TEST_COMMIT", filepath.Join(t.TempDir(), "commit"))
	t.Setenv("ATM_TEST_OUT", delivered)
	t.Setenv("FAKE_AGENT_CALL", call)
	t.Setenv("FAKE_AGENT_WORK", noteWork)
	t.Setenv("FAKE_PONYTAIL_WORK", ": > a.txt")
	t.Setenv("FAKE_PONYTAIL_REPORT", cut)
	cfg := atmSet(atmSet(atmYAML, "lint",
		`test ! -f "$ATM_TEST_LINT" && { test ! -f "$ATM_TEST_COMMIT" || git commit --allow-empty -qm lint; }`),
		"delivery", `{ git log --format=%s; cat a.txt; ls; } > "$ATM_TEST_OUT"`)
	top := gitT(t, repo(t, "https://example.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
	bin := followUpsServer(t, top)
	writeFixture(t, lint, "")
	followedRun(t, bin, top, 1, issueFile(t, followUpIssue))
	failed := lastRun(t, top, "failed")
	legacyResumePoint(t, filepath.Join(runDir(top, failed), "resume", "resume.json"))
	if err := os.Remove(call); err != nil {
		t.Fatal(err)
	}
	historicalRefused(t, bin, top, failed, delivered, call)
	// A failed configured check must still block delivery.
	followedRun(t, bin, top, 1, "--resume", failed)
	again := lastRun(t, top, "failed")
	rep := readReport(t, filepath.Join(runDir(top, again), "report.json"))
	if rep["failed_node"] != "checks" || !strings.Contains(rep["reason"].(string), "lint") {
		t.Fatalf("want configured lint failure, got %v", rep)
	}
	u := rep["units"].([]any)[0].(obj)
	if u["test_file"] != "a_test.sh" || u["base"] != savedBase(t, top, failed) ||
		u["red"] != nil || u["green"] != nil {
		t.Fatalf("want the saved unit without red/green proof, got %v", u)
	}
	if _, err := os.Stat(delivered); !os.IsNotExist(err) {
		t.Fatalf("failed checks delivered: %v", err)
	}
	if err := os.Remove(lint); err != nil {
		t.Fatal(err)
	}
	// The configured suite rejects the slop detector's cut.
	followedRun(t, bin, top, 0, "--resume", again)
	pony := reportNode(t, readReport(t, filepath.Join(runDir(top, lastRun(t, top, "passed")), "report.json")),
		"ponytail")
	if pony["kept"] != false || !strings.Contains(pony["reason"].(string), "test") {
		t.Fatalf("want configured suite to reject the cut, got %v", pony)
	}
	firstUnitGuards(t, bin, top, failed, delivered)
}

// noteWork is the agent's fix, with the note it leaves and a follow-up its report still names, as before the
// retirement.
const noteWork = fixWork + "; mkdir -p .atm/follow-ups/1; echo false > .atm/follow-ups/1/c1_test.sh; " +
	`printf '{"test_file":"a_test.sh","summary":"` + withoutFollowUpsNote + `","follow_ups":[{"title":"gap",` +
	`"red_test":"c1_test.sh","criterion":"A retry runs once after a timeout.","paths":["a.txt"]}]}' ` +
	`> .atm/fake-report.json`

// historicalRefused checks a saved second unit is refused before work runs and its saved files stay untouched.
func historicalRefused(t *testing.T, bin, top, label, delivered, call string) {
	t.Helper()
	runs, err := RecordedRuns(top)
	if err != nil {
		t.Fatal(err)
	}
	old := label + "-unit2"
	if err := os.CopyFS(runDir(top, old), os.DirFS(runDir(top, label))); err != nil {
		t.Fatal(err)
	}
	historicalBundle(t, top, old)
	kept := savedFiles(t, top, old)
	f := startFollow(t, bin, top, false, "run", "--follow", "--resume", old)
	for line := range f.lines {
		if strings.Contains(line, `"step"`) {
			t.Fatalf("a refused resume ran %s", line)
		}
	}
	_ = f.cmd.Wait()
	if code, msg := f.cmd.ProcessState.ExitCode(), f.errors(); code != 2 ||
		!strings.Contains(msg, "cannot resume run "+old+": it stopped at unit 2") ||
		!strings.Contains(msg, "follow-ups were retired, so launch the issue again") {
		t.Fatalf("want the resume refused, exit 2, got %d: %s", code, msg)
	}
	if got := savedFiles(t, top, old); !maps.Equal(got, kept) {
		t.Fatalf("a refused resume changed its saved files")
	}
	if now, err := RecordedRuns(top); err != nil || len(now) != len(runs) {
		t.Fatalf("refused resumes recorded runs: %+v, %v", now, err)
	}
	for _, path := range []string{call, call + ".ponytail", delivered} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a refused resume ran %s: %v", path, err)
		}
	}
}

// historicalBundle puts completed original work in the parent of the saved, uncommitted second unit of run label.
// Its report names the follow-up test as the old contract required; proofs contains the original test.
func historicalBundle(t *testing.T, top, label string) {
	t.Helper()
	dir := runDir(top, label)
	base := savedBase(t, top, label)
	work := filepath.Join(t.TempDir(), "history")
	gitT(t, top, "clone", "-q", top, work)
	gitT(t, work, "fetch", "-q", filepath.Join(dir, "resume", "work.bundle"), "refs/atm/resume")
	parent := gitT(t, work, "commit-tree", "FETCH_HEAD^{tree}", "-p", base,
		"-m", "fix: Retry on timeout\n\natm unit 1")
	gitT(t, work, "reset", "--hard", parent)
	original := gitT(t, work, "show", "HEAD:a_test.sh") + "\n"
	writeFixture(t, filepath.Join(work, "a.txt"), "fixed\nu2\nextra\n")
	followTest := "grep -q u2 a.txt\n"
	writeFixture(t, filepath.Join(work, "c1_test.sh"), followTest)
	tip := gitT(t, work, "commit-tree", tree(t, work), "-p", parent, "-m", "atm unit 2: its work, uncommitted")
	gitT(t, work, "update-ref", "refs/atm/resume", tip)
	gitT(t, work, "bundle", "create", filepath.Join(dir, "resume", "work.bundle"), "refs/atm/resume", "^"+base)
	editResumePoint(t, top, label, func(saved obj) {
		saved["follow_up"].(obj)["test"] = followTest
		saved["unit"] = 2
		saved["proofs"] = [][4]string{{base, "a_test.sh", "fix", original}}
		saved["report"] = obj{"test_file": "c1_test.sh", "changed_files": []string{"a.txt", "c1_test.sh"},
			"summary": withoutFollowUpsNote, "commands_run": []string{"sh c1_test.sh"},
			"follow_ups": []obj{{"title": "error", "red_test": "c2_test.sh", "criterion": "The retry error is reported."}}}
	})
}

// firstUnitGuards copies the first unit run label kept, once for each guard, with its saved work changed to break
// it, and checks the resume fails at Checks for that guard's reason, without delivery.
func firstUnitGuards(t *testing.T, bin, top, label, delivered string) {
	t.Helper()
	if err := os.Remove(delivered); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, reason string
		change       func(work, label string)
	}{
		{"original regression", "test: exit status 1", func(work, _ string) {
			writeFixture(t, filepath.Join(work, "a.txt"), "broken\n")
		}},
		{"command commit", "the commands made commits", func(string, string) {
			writeFixture(t, os.Getenv("ATM_TEST_COMMIT"), "")
		}},
	} {
		copy := label + "-" + strings.ReplaceAll(c.name, " ", "-")
		if err := os.CopyFS(runDir(top, copy), os.DirFS(runDir(top, label))); err != nil {
			t.Fatal(err)
		}
		dir, base := runDir(top, copy), savedBase(t, top, copy)
		work := filepath.Join(t.TempDir(), "saved")
		gitT(t, top, "clone", "-q", top, work)
		gitT(t, work, "fetch", "-q", filepath.Join(dir, "resume", "work.bundle"), "refs/atm/resume")
		gitT(t, work, "reset", "--hard", "FETCH_HEAD")
		c.change(work, copy)
		tip := gitT(t, work, "commit-tree", tree(t, work), "-p", base, "-m", "saved first unit")
		gitT(t, work, "update-ref", "refs/atm/resume", tip)
		gitT(t, work, "bundle", "create", filepath.Join(dir, "resume", "work.bundle"), "refs/atm/resume", "^"+base)
		followedRun(t, bin, top, 1, "--resume", copy)
		rep := readReport(t, filepath.Join(runDir(top, lastRun(t, top, "failed")), "report.json"))
		if rep["failed_node"] != "checks" || !strings.Contains(rep["reason"].(string), c.reason) {
			t.Fatalf("%s: want %s to block Checks, got %v", c.name, c.reason, rep)
		}
		if _, err := os.Stat(delivered); !os.IsNotExist(err) {
			t.Fatalf("%s: a broken first unit delivered: %v", c.name, err)
		}
	}
}

// followedRun is the events of atm run --follow with args, by the real binary bin at top, after checking its exit
// code is code.
func followedRun(t *testing.T, bin, top string, code int, args ...string) []obj {
	t.Helper()
	f := startFollow(t, bin, top, false, append([]string{"run", "--follow"}, args...)...)
	var evs []obj
	for line := range f.lines {
		var ev obj
		if json.Unmarshal([]byte(line), &ev) == nil {
			evs = append(evs, ev)
		}
	}
	if _ = f.cmd.Wait(); f.cmd.ProcessState.ExitCode() != code {
		log, _ := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
		t.Fatalf("atm run %v: exit %d, want %d: %s\n%s", args, f.cmd.ProcessState.ExitCode(), code, f.errors(), log)
	}
	for _, ev := range evs {
		if ev["step"] == "decision" && ev["state"] == "waiting" {
			stopCheckRun(t, top, ev["run"].(string))
		}
	}
	return evs
}

// lastRun is the label of the last run at top, after checking it ended as want.
func lastRun(t *testing.T, top, want string) string {
	t.Helper()
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) == 0 || runs[len(runs)-1].Outcome != want {
		t.Fatalf("want the last run %s, got %+v, %v", want, runs, err)
	}
	return runs[len(runs)-1].Run
}

// legacyResumePoint adds to the resume.json at path what a run kept before the retirement: a chained follow-up
// with its approved scope, a mandatory pending one and a spent budget.
func legacyResumePoint(t *testing.T, path string) {
	t.Helper()
	var saved obj
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &saved) != nil {
		t.Fatalf("resume.json: %v\n%s", err, b)
	}
	saved["follow_up"] = obj{"title": "gap", "red_test": "c1_test.sh", "criterion": "A retry runs once after a timeout.",
		"paths": []string{"a.txt"}, "test": "false\n", "verified": true,
		"scope": obj{"paths": []string{"a.txt", "c1_test.sh"}, "approved": true}}
	saved["pending"] = []obj{{"title": "error", "red_test": "c2_test.sh", "criterion": "The retry error is reported.",
		"test": "false\n", "verified": true}}
	saved["gross_lines"] = 500
	b, _ := json.Marshal(saved)
	writeFixture(t, path, string(b))
}

// savedBase is the base commit run label kept in its resume.json.
func savedBase(t *testing.T, top, label string) string {
	t.Helper()
	var saved struct{ Base string }
	b, err := os.ReadFile(filepath.Join(runDir(top, label), "resume", "resume.json"))
	if err != nil || json.Unmarshal(b, &saved) != nil {
		t.Fatalf("resume.json: %v\n%s", err, b)
	}
	return saved.Base
}

// editResumePoint rewrites the resume.json run label kept, as edit changes it.
func editResumePoint(t *testing.T, top, label string, edit func(obj)) {
	t.Helper()
	path := filepath.Join(runDir(top, label), "resume", "resume.json")
	var saved obj
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &saved) != nil {
		t.Fatalf("resume.json: %v\n%s", err, b)
	}
	edit(saved)
	b, _ := json.Marshal(saved)
	writeFixture(t, path, string(b))
}

// savedFiles is the content of the report.json, resume.json and work.bundle run label kept, by path.
func savedFiles(t *testing.T, top, label string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"report.json", "resume/resume.json", "resume/work.bundle"} {
		b, err := os.ReadFile(filepath.Join(runDir(top, label), name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(b)
	}
	return files
}

// tree is the work in clone, every file, as a tree.
func tree(t *testing.T, clone string) string {
	t.Helper()
	tree, err := snapshot(clone)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// replayed serves at top's socket one attach of run old-run: it replays events, then the run's passed outcome,
// and says on the channel returned whether it could.
func replayed(t *testing.T, top string, events ...string) <-chan error {
	t.Helper()
	listener, err := net.Listen("unix", short(socket(top)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = c.Close() }()
		var req request
		if err := json.NewDecoder(c).Decode(&req); err != nil {
			done <- err
			return
		}
		enc := json.NewEncoder(c)
		for _, event := range events {
			if err := enc.Encode(reply{Frame: Frame{Event: json.RawMessage(event)}}); err != nil {
				done <- err
				return
			}
		}
		done <- enc.Encode(reply{Run: &Outcome{Run: "old-run", Outcome: "passed", Ended: time.Now()}})
	}()
	return done
}
