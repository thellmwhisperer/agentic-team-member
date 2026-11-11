//go:build unix

package run

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
		`if test -f "$ATM_TEST_SOURCE/fail-delivery"; then git commit --allow-empty -qm delivery-commit || exit; echo delivery-finding; exit 1; fi`)
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
	if r := e2eReport(t, top, label); r.HeadSHA == gitT(t, pending, "rev-parse", "HEAD") ||
		r.HeadSHA != gitT(t, pending, "rev-parse", "HEAD^") {
		t.Fatalf("delivery did not advance beyond its recorded commit: %+v", r)
	}
	keptCloneRun(t, bin, top, path, 0)
	exist(t, pending, pending+".delivered", undelivered)
	assertCloneCleanup(t, top, pending, "kept")
	legacyPath := filepath.Join(runDir(top, label), "report.json")
	b, err := os.ReadFile(legacyPath)
	var legacy verdict
	if err != nil || json.Unmarshal(b, &legacy) != nil || legacy.node("clone").ClonePath != pending {
		t.Fatalf("missing clone path in report: %s, %v", b, err)
	}
	legacy.node("clone").ClonePath = ""
	b, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	write(t, legacyPath, string(b))
	keptCloneRun(t, bin, top, path, 0)
	exist(t, pending, pending+".delivered", undelivered)
	assertCloneCleanup(t, top, pending, "kept")
}

func TestKeptCloneSweepRemovesResumedCloneWithoutRemovingOriginal(t *testing.T) {
	t.Setenv("FAKE_AGENT_WORK", "if test -f first-turn; then touch repaired; else "+fixWork+"; touch first-turn; fi")
	bin, top, path, prState := keptCloneRepo(t,
		`if test -f "$ATM_TEST_SOURCE/fail-delivery" && ! test -f repaired; then `+
			`git commit --allow-empty -qm delivery-commit || exit; echo delivery-finding; exit 1; fi`)
	configPath := filepath.Join(top, ".atm.yaml")
	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	write(t, configPath, atmSet(string(cfg), "lint", `test ! -f repaired || test -f "$ATM_TEST_SOURCE/resume-ready"`))
	gitT(t, top, "add", ".atm.yaml")
	gitT(t, top, "commit", "-qm", "configure resume check")
	write(t, filepath.Join(top, "fail-delivery"), "")
	original, label := question(t, bin, top, "delivery-finding", 4, "run", "--follow", path)
	answerFailedCheck(t, bin, top, label, "fix", 1)
	stopCheckRun(t, top, label)
	prior := e2eReport(t, top, label)
	if !prior.Resumable || prior.FailedNode != "checks" || len(prior.Decisions) != 2 ||
		prior.Decisions[0].Answer != "fix" || prior.Decisions[1].Answer != "stop" ||
		prior.node("clone").ClonePath != original || !filepath.IsAbs(prior.node("clone").ClonePath) {
		t.Fatalf("repair did not keep a resumable clone with its absolute path: %+v", prior)
	}
	write(t, filepath.Join(top, "resume-ready"), "")
	resumed := resumedLatestAttemptClone(t, followedRun(t, bin, top, 0, "--resume", label))
	if resumed == "" || resumed == original {
		t.Fatalf("resume did not create a separate clone: %q", resumed)
	}
	if r := e2eReport(t, top, lastRun(t, top, "passed")); r.node("clone").ClonePath != resumed {
		t.Fatalf("resumed report does not identify its own clone: %+v", r)
	}
	gitT(t, resumed, "merge-base", "--is-ancestor", prior.HeadSHA, "HEAD")
	exist(t, original, original+".delivered", resumed, resumed+".delivered")
	write(t, prState, "CLOSED\n")
	if err := os.Remove(filepath.Join(top, "fail-delivery")); err != nil {
		t.Fatal(err)
	}
	keptCloneRun(t, bin, top, path, 0)
	exist(t, original, original+".delivered", original+".lock")
	for _, suffix := range []string{"", ".delivered", ".lock"} {
		if _, err := os.Lstat(resumed + suffix); !os.IsNotExist(err) {
			t.Fatalf("completed resumed clone remains after its PR closed: %s: %v", resumed+suffix, err)
		}
	}
	assertCloneCleanup(t, top, original, "kept")
	assertCloneCleanup(t, top, resumed, "removed")
	if r := e2eReport(t, top, label); !r.Resumable || r.node("clone").ClonePath != original {
		t.Fatalf("sweep changed the original resumable run: %+v", r)
	}
}

func TestKeptCloneSweepPreservesUnidentifiedLegacyRun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		resumable bool
		answer    string
		keep      bool
	}{
		{"resumable", true, "stop", true},
		{"waiting", false, "", true},
		{"stopped", false, "stop", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, top, path, prState := keptCloneRepo(t, "")
			first := keptCloneRun(t, bin, top, path, 0)
			legacy := verdict{FailedNode: "checks", Resumable: tc.resumable,
				Nodes:     []*node{{Name: "clone", Result: "passed"}},
				Decisions: []Decision{{Node: "checks", Answer: tc.answer}}}
			b, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			dir := runDir(top, "legacy")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "report.json"), string(b))
			write(t, prState, "CLOSED\n")
			keptCloneRun(t, bin, top, path, 0)
			if tc.keep {
				exist(t, first, first+".delivered", first+".lock")
				assertCloneCleanup(t, top, first, "kept")
			} else {
				if _, err := os.Lstat(first); !os.IsNotExist(err) {
					t.Fatalf("stopped legacy run blocked clone cleanup: %v", err)
				}
				assertCloneCleanup(t, top, first, "removed")
			}
		})
	}
}

func TestKeptCloneSweepPreservesDeliveryThatStopsDuringSweep(t *testing.T) {
	for _, phase := range []string{"start", "end"} {
		t.Run(phase, func(t *testing.T) {
			bin, top, path, prState := keptCloneRepo(t,
				`if test -f "$ATM_TEST_SOURCE/hold-delivery"; then `+
					`rm "$ATM_TEST_SOURCE/hold-delivery"; echo "$ATM_REPORT" > "$ATM_TEST_SOURCE/delivery-report"; `+
					`read answer < "$ATM_TEST_SOURCE/delivery-gate"; echo delivery-finding; exit 1; fi; `+
					`if test -f "$ATM_TEST_SOURCE/block-sweep"; then `+
					`mkfifo "$ATM_TEST_SOURCE/.atm/runs/zz-block/report.json"; fi`)
			gate := filepath.Join(top, "delivery-gate")
			if err := syscall.Mkfifo(gate, 0o600); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(top, "hold-delivery"), "")
			held := startFollow(t, bin, top, false, "run", "--follow", path)
			var report []byte
			until(t, "held delivery", func() bool {
				report, _ = os.ReadFile(filepath.Join(top, "delivery-report"))
				return len(report) > 0
			})
			label := filepath.Base(filepath.Dir(strings.TrimSpace(string(report))))
			prior := e2eReport(t, top, label)
			pending := prior.node("clone").ClonePath
			if pending == "" || prior.FailedNode != "" || prior.node("delivery").Result != "running" {
				t.Fatalf("delivery was not active before the sweep: %+v", prior)
			}
			write(t, prState, "CLOSED\n")
			block := filepath.Join(top, ".atm", "runs", "zz-block", "report.json")
			if err := os.MkdirAll(filepath.Dir(block), 0o755); err != nil {
				t.Fatal(err)
			}
			if phase == "start" {
				if err := syscall.Mkfifo(block, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, filepath.Join(top, "block-sweep"), "")
			}
			next := startFollow(t, bin, top, false, "run", "--follow", path)
			var pipe *os.File
			until(t, "sweep reading reports", func() bool {
				fd, err := syscall.Open(block, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err == nil {
					pipe = os.NewFile(uintptr(fd), block)
				}
				return err == nil
			})
			t.Cleanup(func() { _ = pipe.Close() })
			if err := os.Remove(block); err != nil {
				t.Fatal(err)
			}
			write(t, block, "{}")
			fd, err := syscall.Open(gate, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			release := os.NewFile(uintptr(fd), gate)
			if _, err := release.WriteString("stop\n"); err != nil {
				t.Fatal(err)
			}
			if err := release.Close(); err != nil {
				t.Fatal(err)
			}
			held.exit(4)
			stopCheckRun(t, top, label)
			if r := e2eReport(t, top, label); !r.Resumable {
				t.Fatalf("stopped delivery is not resumable: %+v", r)
			}
			if _, err := pipe.WriteString("{}"); err != nil {
				t.Fatal(err)
			}
			if err := pipe.Close(); err != nil {
				t.Fatal(err)
			}
			next.exit(0)
			exist(t, pending, pending+".delivered", pending+".lock")
			if got := reader(t, pending)("a.txt"); got != "fixed" {
				t.Fatalf("sweep lost stopped delivery's work: %q", got)
			}
		})
	}
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
