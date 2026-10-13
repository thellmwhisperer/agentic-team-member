package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// init is a fake codex, this test binary under that name, when FAKE_AGENT is resume: the implementing agent
// writes done.txt and a line to FAKE_AGENT_CALLS, the slop detector cuts nothing.
func init() {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") != "codex" || os.Getenv("FAKE_AGENT") != "resume" {
		return
	}
	args := os.Args[1:]
	skill, report := "ponytail", `{"test_file": "", "changed_files": ["done.txt"], "summary": "s", "follow_ups": []}`
	if strings.Contains(args[len(args)-1], "`ponytail-review`") {
		skill, report = "ponytail-review", `{"findings": [], "summary": "Lean already."}`
	} else {
		f, err := os.OpenFile(os.Getenv("FAKE_AGENT_CALLS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = fmt.Fprintln(f, "agent")
			err = errors.Join(err, f.Close(), os.WriteFile("done.txt", []byte("done\x00\n"), 0o600))
		}
		if err != nil {
			os.Exit(1)
		}
	}
	fmt.Printf(`{"type": "item.completed", "item": {"id": "1", "type": "command_execution", `+
		`"command": "cat .agents/skills/%s/SKILL.md", "status": "completed", "exit_code": 0}}`+"\n", skill)
	if err := os.WriteFile(args[slices.Index(args, "-o")+1], []byte(report), 0o600); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// resumeRepo is a repository whose agent is the fake codex of init, and whose .atm.yaml runs install and test.
func resumeRepo(t *testing.T, install, test string) (dir, calls string) {
	t.Helper()
	dir = setupConcurrentRunRepo(t)
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(bin, "codex")
	if runtime.GOOS == "windows" {
		codex += ".exe"
	}
	if err := os.Symlink(exe, codex); err != nil {
		if err := os.Link(exe, codex); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_AGENT", "resume")
	calls = filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_AGENT_CALLS", calls)
	writeResumeConfig(t, dir, install, test)
	if err := os.WriteFile(filepath.Join(dir, "chore.md"), []byte("# Chore\nType: chore\nDo it.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, calls
}

func writeResumeConfig(t *testing.T, dir, install, test string) {
	t.Helper()
	config := fmt.Sprintf("harness: codex\ninstall: %q\ntest: %q\ntypecheck: \"\"\nlint: \"\"\n"+
		"test_file: \"\"\ntest_patterns: []\ndocs_patterns: []\ndelivery: \"\"\n", install, test)
	if err := os.WriteFile(filepath.Join(dir, ".atm.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

// axiRun runs atm axi run --json with args, failing the test unless it exits with code, and returns its outcome.
func axiRun(t *testing.T, code int, args ...string) map[string]string {
	t.Helper()
	dir, err := toplevel()
	if err != nil {
		t.Fatal(err)
	}
	watcher(t, dir, "resume-test", 0)
	var o map[string]string
	out := atmIn(t, code, append([]string{"axi", "run", "--json", "--watcher", "resume-test"}, args...)...)
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

// failsToResume says whether atm axi run --resume label fails with an error that says why.
func failsToResume(t *testing.T, label, why string) bool {
	t.Helper()
	dir, err := toplevel()
	if err != nil {
		t.Fatal(err)
	}
	watcher(t, dir, "resume-test", 0)
	_, err = atm(t, "axi", "run", "--watcher", "resume-test", "--resume", label)
	return err != nil && strings.Contains(err.Error(), why)
}

func TestResumePicksUpTheAgentsWorkFromChecks(t *testing.T) {
	dir, calls := resumeRepo(t, "", "false")
	failed := axiRun(t, 1, "chore.md")
	if failed["run"] != "chore-1" || failed["failed_node"] != "checks" ||
		!strings.Contains(failed["next_step"], "atm run --resume chore-1") {
		t.Fatalf("the failed run: %v", failed)
	}
	var report struct {
		Resumable     bool `json:"resumable"`
		ResumableUnit int  `json:"resumable_unit"`
	}
	if err := json.Unmarshal([]byte(read(t, failed["report"])), &report); err != nil || !report.Resumable ||
		report.ResumableUnit != 1 {
		t.Fatalf("report.json of the failed run: %+v, %v", report, err)
	}

	writeResumeConfig(t, dir, "", "test -f done.txt")
	passed := axiRun(t, 0, "--resume", "chore-1")
	if passed["run"] != "chore-2" || passed["outcome"] != "passed" {
		t.Fatalf("the resumed run: %v", passed)
	}
	if got := read(t, calls); got != "agent\n" {
		t.Fatalf("the agent ran %d times, want once", strings.Count(got, "\n"))
	}
	var resumed struct {
		Units []struct {
			ResumedFrom string `json:"resumed_from"`
		} `json:"units"`
	}
	if err := json.Unmarshal([]byte(read(t, passed["report"])), &resumed); err != nil || len(resumed.Units) != 1 ||
		resumed.Units[0].ResumedFrom != "chore-1" {
		t.Fatalf("report.json of the resumed run: %+v, %v", resumed, err)
	}

	// A base commit the repository no longer has.
	kept := filepath.Join(dir, ".atm", "runs", "chore-1", "resume", "resume.json")
	b := strings.Replace(read(t, kept), `"base": "`, `"base": "0000000`, 1)
	if err := os.WriteFile(kept, []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
	if !failsToResume(t, "chore-1", "no longer has its base commit") {
		t.Fatal("atm axi run --resume of a run whose base is gone does not say so")
	}
}

func TestResumeOfARunThatFailedAtContractIsNotResumable(t *testing.T) {
	// install, in the clone, leaves a directory where the run's brief goes: the contract fails.
	resumeRepo(t, "mkdir ../../runs/chore-1/brief.md", "true")
	if failed := axiRun(t, 2, "chore.md"); failed["failed_node"] != "contract" ||
		strings.Contains(failed["next_step"], "--resume") {
		t.Fatalf("the failed run: %v", failed)
	}
	if !failsToResume(t, "chore-1", "not resumable") {
		t.Fatal("atm axi run --resume of a run that failed at contract does not say it is not resumable")
	}
	if !failsToResume(t, "chore-9", "no run chore-9") {
		t.Fatal("atm axi run --resume of no run does not say so")
	}
}
