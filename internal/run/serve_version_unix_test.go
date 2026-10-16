//go:build unix

package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/buildinfo"
)

// A uses the unchanged Serve implementation and has its launcher check removed.
// Its renamed executable never changes when the symlink selects B.
func TestLauncherRejectsAnUnpatchedServeOfAnotherVersion(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "atm-main-A"), filepath.Join(dir, "atm-main-B")
	buildVersionATM(t, a, "A", true)
	buildVersionATM(t, b, "B", false)
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing-log-%t", missing), func(t *testing.T) {
			top, _ := liveLease(t)
			wait, release := holdFakeAgents(t, 1)
			old := startVersionServe(t, a, top)
			watcher := "live"
			path := issueFile(t, issue)
			if out, err := versionLaunch(a, top, watcher, path); err != nil {
				t.Fatalf("legacy launch: %s, %v", out, err)
			}
			wait()
			link := filepath.Join(t.TempDir(), "atm")
			if err := os.Symlink(b, link); err != nil {
				t.Fatal(err)
			}
			version := "A (A) test-date"
			if missing {
				version = "unknown"
				// A different pid's matching version must not authorize this serve.
				writeFixture(t, filepath.Join(top, ".atm", "serve.log"),
					"atm serve pid: 0\natm serve version: B (B) test-date\n")
			}
			out, err := versionLaunch(link, top, watcher, path)
			want := "serve " + version + " is older than atm B (B) test-date"
			if err == nil || !strings.Contains(string(out), want) {
				t.Errorf("new launcher = %q, %v; want %q", out, err, want)
			}
			runs, err := Runs(top)
			if err != nil || len(runs) != 1 || runs[0].Outcome != "running" {
				t.Errorf("old serve must keep exactly its original run: %+v, %v", runs, err)
			}
			release()
			checkVersionRun(t, top, "issue-1")
			_ = old.Process.Kill()
			_ = old.Wait()
			waitForDeadServer(t, top)
			startVersionServe(t, b, top)
			if out, err := versionLaunch(link, top, watcher, path); err != nil || string(out) != "issue-2\n" {
				t.Fatalf("matching replacement launch: %s, %v", out, err)
			}
			checkVersionRun(t, top, "issue-2")
			log, err := os.ReadFile(filepath.Join(top, ".atm", "serve.log"))
			if err != nil || !strings.Contains(string(log), "atm serve version: B (B) test-date") {
				t.Fatalf("replacement identity: %s, %v", log, err)
			}
		})
	}
}

func checkVersionRun(t *testing.T, top, label string) {
	t.Helper()
	if end, err := Attach(top, label, &bytes.Buffer{}); err != nil || end.Outcome != "passed" {
		t.Fatalf("run %q was interrupted: %+v, %v", label, end, err)
	}
}

func stampServeFixture(t *testing.T, top, version string) {
	t.Helper()
	v, c, d := buildinfo.Version, buildinfo.Commit, buildinfo.Date
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit, buildinfo.Date = v, c, d })
	buildinfo.Version, buildinfo.Commit, buildinfo.Date = version, version, "test-date"
	writeFixture(t, filepath.Join(top, ".atm", "serve.log"), fmt.Sprintf(
		"atm serve pid: %d\natm serve version: %s\n", os.Getpid(), buildinfo.String()))
}

func buildVersionATM(t *testing.T, bin, version string, base bool) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"build", "-buildvcs=false"}
	if base {
		dir := t.TempDir()
		path := filepath.Join(root, "internal", "run", "background.go")
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// This overlay restores the legacy launcher as well as preserving the
		// legacy serve. The symbol check below verifies the gate is not linked.
		old := filepath.Join(dir, "background.go")
		writeFixture(t, old, strings.ReplaceAll(string(source), `	if req.Cmd == "run" {
		if again, err := matchingServe(root, c); again || err != nil {
			return again, err
		}
	}
`, ""))
		overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{
			path: old,
		}})
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "overlay.json")
		writeFixture(t, file, string(overlay))
		args = append(args, "-overlay", file)
	}
	pkg := "github.com/thellmwhisperer/agentic-team-member/internal/buildinfo."
	flags := "-X " + pkg + "Version=" + version + " -X " + pkg + "Commit=" + version + " -X " + pkg + "Date=test-date"
	args = append(args, "-ldflags", flags, "-o", bin, "./cmd/atm")
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %s, %v", version, out, err)
	}
	if base {
		out, err := exec.Command("go", "tool", "nm", bin).CombinedOutput()
		if err != nil || bytes.Contains(out, []byte("run.matchingServe")) {
			t.Fatalf("old serve must contain no launcher version check: %v", err)
		}
	}
}

func startVersionServe(t *testing.T, bin, top string) *exec.Cmd {
	t.Helper()
	log, err := os.OpenFile(filepath.Join(top, ".atm", "serve.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	cmd := exec.Command(bin, "serve")
	cmd.Dir, cmd.Stderr = top, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	t.Cleanup(func() {
		if t.Failed() {
			data, _ := os.ReadFile(log.Name())
			t.Logf("serve log: %s", data)
		}
	})
	until(t, "serve identity", func() bool {
		data, _ := os.ReadFile(log.Name())
		return strings.Contains(string(data), fmt.Sprintf("atm serve pid: %d\natm serve version:", cmd.Process.Pid))
	})
	return cmd
}

func versionLaunch(bin, top, watcher, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "--watcher", watcher, path)
	cmd.Dir = top
	return cmd.CombinedOutput()
}
