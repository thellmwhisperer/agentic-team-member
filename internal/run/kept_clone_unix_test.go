//go:build unix

package run

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeptCloneIsRemovedOnceItsPRCloses(t *testing.T) {
	for _, state := range []string{"MERGED", "CLOSED"} {
		t.Run(state, func(t *testing.T) {
			bin, top, path, prState := keptCloneRepo(t, "")
			first := keptCloneRun(t, bin, top, path, 0)
			exist(t, first, first+".delivered", first+".lock")
			second := keptCloneRun(t, bin, top, path, 0)
			exist(t, first, first+".delivered", first+".lock", second)
			write(t, prState, state+"\n")
			keptCloneRun(t, bin, top, path, 0)
			for _, clone := range []string{first, second} {
				for _, suffix := range []string{"", ".delivered", ".lock"} {
					if _, err := os.Lstat(clone + suffix); !os.IsNotExist(err) {
						t.Fatalf("kept clone remains after PR %s: %s: %v", state, clone+suffix, err)
					}
				}
				assertCloneCleanup(t, top, clone, "removed")
			}
			if left := clones(t, top); len(left) != 0 {
				t.Fatalf("closed PR clones remain: %s", strings.Join(left, ", "))
			}
		})
	}
}

// The PR closes during the next run, after its initial sweep.
func TestKeptCloneIsRecheckedAtRunEnd(t *testing.T) {
	bin, top, path, _ := keptCloneRepo(t,
		`if test -f "$ATM_TEST_SOURCE/close-pr"; then echo CLOSED > "$ATM_TEST_PR_STATE"; fi`)
	first := keptCloneRun(t, bin, top, path, 0)
	exist(t, first)
	write(t, filepath.Join(top, "close-pr"), "")
	keptCloneRun(t, bin, top, path, 0)
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("clone survived the end sweep: %v", err)
	}
	assertCloneCleanup(t, top, first, "removed")
}

func TestKeptCloneSweepPreservesPendingAndResumableDelivery(t *testing.T) {
	bin, top, path, prState := keptCloneRepo(t,
		`if test -f "$ATM_TEST_SOURCE/fail-delivery"; then echo delivery-finding; exit 1; fi`)
	write(t, filepath.Join(top, "fail-delivery"), "")
	pending, label := question(t, bin, top, "delivery-finding", 4, "run", "--follow", path)
	write(t, prState, "CLOSED\n")
	if err := os.Remove(filepath.Join(top, "fail-delivery")); err != nil {
		t.Fatal(err)
	}
	// Also leave non-clones, an undelivered clone, and run entries without reports.
	write(t, filepath.Join(top, ".atm", "runs", "plain-file"), "")
	if err := os.Mkdir(filepath.Join(top, ".atm", "runs", "no-report"), 0o755); err != nil {
		t.Fatal(err)
	}
	undelivered := oldClone(t, top, false, false)
	keptCloneRun(t, bin, top, path, 0)
	exist(t, pending, pending+".delivered", undelivered)
	stopCheckRun(t, top, label)
	if r := e2eReport(t, top, label); !r.Resumable {
		t.Fatalf("stopped delivery is not resumable: %+v", r)
	}
	keptCloneRun(t, bin, top, path, 0)
	exist(t, pending, pending+".delivered", undelivered)
	assertCloneCleanup(t, top, pending, "kept")
	// A report from before clone paths were recorded still protects its delivered commit.
	legacyPath := filepath.Join(runDir(top, label), "report.json")
	b, err := os.ReadFile(legacyPath)
	var legacy map[string]any
	if err != nil || json.Unmarshal(b, &legacy) != nil || legacy["clone"] != pending {
		t.Fatalf("missing clone path in report: %s, %v", b, err)
	}
	delete(legacy, "clone")
	b, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	write(t, legacyPath, string(b))
	keptCloneRun(t, bin, top, path, 0)
	exist(t, pending, pending+".delivered", undelivered)
	assertCloneCleanup(t, top, pending, "kept")
}

func TestKeptCloneSweepRecordsErrorsAndKeepsWork(t *testing.T) {
	for _, mode := range []string{"bad-report", "bad-lock", "unreadable-runs", "unreadable-clones"} {
		t.Run(mode, func(t *testing.T) {
			delivery := ""
			if strings.HasPrefix(mode, "unreadable-") {
				part := strings.TrimPrefix(mode, "unreadable-")
				delivery = `if test -f "$ATM_TEST_SOURCE/restrict"; then chmod 300 "$ATM_TEST_SOURCE/.atm/` + part + `"; fi`
			}
			bin, top, path, prState := keptCloneRepo(t, delivery)
			first := keptCloneRun(t, bin, top, path, 0)
			write(t, prState, "CLOSED\n")
			prepareCloneSweepError(t, top, first, mode)
			code := 0
			if mode == "unreadable-clones" {
				code = 2 // its own release cannot open the parent directory
			}
			keptCloneRun(t, bin, top, path, code)
			for _, part := range []string{"runs", "clones"} {
				if err := os.Chmod(filepath.Join(top, ".atm", part), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "bad-report" || mode == "bad-lock" {
				exist(t, first, first+".delivered")
			}
			assertCloneCleanup(t, top, "", "error")
		})
	}
}

func prepareCloneSweepError(t *testing.T, top, first, mode string) {
	t.Helper()
	switch mode {
	case "bad-report":
		write(t, filepath.Join(top, ".atm", "runs", "issue-1", "report.json"), "not JSON")
	case "bad-lock":
		if err := os.Remove(first + ".lock"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(first+".lock", 0o755); err != nil {
			t.Fatal(err)
		}
	default:
		write(t, filepath.Join(top, "restrict"), "")
		// Restore access even when an assertion fails before the normal restoration.
		t.Cleanup(func() {
			for _, part := range []string{"runs", "clones"} {
				_ = os.Chmod(filepath.Join(top, ".atm", part), 0o755)
			}
		})
	}
}

func keptCloneRepo(t *testing.T, delivery string) (bin, top, path, prState string) {
	t.Helper()
	cfg := atmSet(atmYAML, "delivery", `git config atm.test-origin "$(git remote get-url origin)"; `+
		`git config atm.test-fetch "$ATM_TEST_SOURCE"; `+delivery+`; true`)
	// Avoid an empty shell statement when no extra delivery command is needed.
	cfg = strings.ReplaceAll(cfg, "; ;", ";")
	top = gitT(t, repo(t, "https://github.com/owner/repo.git", cfg), "rev-parse", "--show-toplevel")
	t.Setenv("ATM_TEST_SOURCE", top)
	prState = filepath.Join(t.TempDir(), "pr-state")
	write(t, prState, "OPEN\n")
	t.Setenv("ATM_TEST_PR_STATE", prState)
	fake := t.TempDir()
	write(t, filepath.Join(fake, "gh"), `#!/bin/sh
case "$1 $2" in
  "run list") echo '[{"workflowName":"test","conclusion":"success"}]' ;;
  "pr view") cat "$ATM_TEST_PR_STATE" ;;
  *) exit 9 ;;
esac
`)
	if err := os.Chmod(filepath.Join(fake, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	bin = versionServer(t, top, "kept-clone-test")
	return bin, top, issueFile(t, issue), prState
}

func keptCloneRun(t *testing.T, bin, top, path string, code int) string {
	t.Helper()
	cmd := exec.Command(bin, "run", "--follow", path)
	cmd.Dir = top
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if actual := cmd.ProcessState.ExitCode(); actual != code {
		t.Fatalf("atm run exit %d, want %d: %v\n%s\n%s", actual, code, err, out, &stderr)
	}
	for _, ev := range events(t, bytes.NewReader(out)) {
		if ev["step"] == "clone" && ev["state"] == "passed" {
			return ev["clone"].(string)
		}
	}
	t.Fatalf("no clone event: %s", out)
	return ""
}

func assertCloneCleanup(t *testing.T, top, clone, result string) {
	t.Helper()
	runs, err := RecordedRuns(top)
	if err != nil || len(runs) == 0 {
		t.Fatalf("recorded runs: %+v, %v", runs, err)
	}
	report := readReport(t, filepath.Join(runDir(top, runs[len(runs)-1].Run), "report.json"))
	decisions, _ := report["clone_cleanup"].([]any)
	for _, entry := range decisions {
		d := entry.(map[string]any)
		if result == "error" && d["error"] != nil || d["path"] == clone && d["result"] == result {
			return
		}
	}
	t.Fatalf("cleanup decision %s for %s not recorded: %+v", result, clone, decisions)
}
