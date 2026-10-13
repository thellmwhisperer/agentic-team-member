//go:build unix

package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeAtm is a script that answers --version as atm at tag would, or an exit 1 when tag is empty.
func fakeAtm(tag string) string {
	if tag == "" {
		return "#!/bin/sh\nexit 1\n"
	}
	return "#!/bin/sh\necho 'atm " + tag + " (abc1234) 2026-10-13T00:00:00Z'\n"
}

// fakeRelease serves tag the way GitHub serves a release #262 publishes: the API, the archive and checksums.txt.
// A wrong sum is checksums.txt naming another archive's sha256.
func fakeRelease(t *testing.T, tag, binary string, wrongSum bool) {
	t.Helper()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "atm", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(binary))
	_ = tw.Close()
	_ = zw.Close()
	name := fmt.Sprintf("atm-%s-%s-%s.tar.gz", tag, runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(gz.Bytes())
	if wrongSum {
		sum = sha256.Sum256([]byte("another archive"))
	}
	files := map[string][]byte{name: gz.Bytes(), "checksums.txt": []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const releases = "/repos/thellmwhisperer/agentic-team-member/releases/"
		if r.URL.Path == releases+"latest" || r.URL.Path == releases+"tags/"+tag {
			assets := []map[string]string{}
			for n := range files {
				assets = append(assets, map[string]string{"name": n, "browser_download_url": srv.URL + "/download/" + n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": assets})
			return
		}
		if b, ok := files[strings.TrimPrefix(r.URL.Path, "/download/")]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ATM_GITHUB_API", srv.URL)
}

// installed is an atm at tag in a new directory, outside any git repository, which is the working directory.
func installed(t *testing.T, tag string) string {
	t.Helper()
	t.Chdir(t.TempDir())
	target := filepath.Join(t.TempDir(), "atm")
	if err := os.WriteFile(target, []byte(fakeAtm(tag)), 0o755); err != nil {
		t.Fatal(err)
	}
	return target
}

func assertVersion(t *testing.T, bin, tag string) {
	t.Helper()
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version exited with error: %v", bin, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "atm" || fields[1] != tag {
		t.Fatalf("%s --version should report literal tag %q, got %q", bin, tag, out)
	}
}

func TestUpdateReplacesTheBinaryWithTheLatestRelease(t *testing.T) {
	target := installed(t, "v0.1.0")
	fakeRelease(t, "v0.2.0", fakeAtm("v0.2.0"), false)
	var out bytes.Buffer
	if err := update(&out, "v0.1.0", target); err != nil {
		t.Fatalf("update: %v\n%s", err, out.String())
	}
	assertVersion(t, target, "v0.2.0")
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 1 {
		t.Fatalf("update left %v next to atm", entries)
	}
}

func TestUpdateWithAWrongChecksumTouchesNothing(t *testing.T) {
	target := installed(t, "v0.1.0")
	fakeRelease(t, "v0.2.0", fakeAtm("v0.2.0"), true)
	var out bytes.Buffer
	if err := update(&out, "v0.1.0", target); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("update with a wrong sum: %v", err)
	}
	assertUntouched(t, target, "v0.1.0")
}

func TestUpdateRollsBackANewBinaryThatDoesNotAnswer(t *testing.T) {
	target := installed(t, "v0.1.0")
	fakeRelease(t, "v0.2.0", fakeAtm(""), false)
	var out bytes.Buffer
	if err := update(&out, "v0.1.0", target); err == nil || !strings.Contains(err.Error(), "previous") {
		t.Fatalf("update to a binary that does not answer: %v", err)
	}
	assertUntouched(t, target, "v0.1.0")
}

func TestUpdateRefusesToOverwriteAPreviousBinary(t *testing.T) {
	target := installed(t, "v0.1.0")
	previous := target + ".previous"
	knownGood := "known-good previous atm"
	if err := os.WriteFile(previous, []byte(knownGood), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := swap(target, []byte(fakeAtm("v0.2.0")), "v0.2.0"); err == nil || !strings.Contains(err.Error(), previous) {
		t.Fatalf("update with existing backup: %v", err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != fakeAtm("v0.1.0") {
		t.Fatalf("current atm changed: %q, %v", b, err)
	}
	if b, err := os.ReadFile(previous); err != nil || string(b) != knownGood {
		t.Fatalf("previous atm changed: %q, %v", b, err)
	}
}

func TestUpdateWhenAlreadyLatestChangesNothing(t *testing.T) {
	target := installed(t, "v0.2.0")
	fakeRelease(t, "v0.2.0", fakeAtm("v0.2.0"), false)
	var out bytes.Buffer
	if err := update(&out, "v0.2.0", target); err != nil || !strings.Contains(out.String(), "v0.2.0") {
		t.Fatalf("update when already latest: %v\n%s", err, out.String())
	}
	assertUntouched(t, target, "v0.2.0")
}

func TestUpdateRefusesADevBuild(t *testing.T) {
	for _, current := range []string{"dev", "v0.1.0-3-gabc1234"} {
		target := installed(t, current)
		fakeRelease(t, "v0.2.0", fakeAtm("v0.2.0"), false)
		var out bytes.Buffer
		err := update(&out, current, target)
		if err == nil || !strings.Contains(err.Error(), current) || !strings.Contains(err.Error(), "v0.2.0") ||
			!strings.Contains(err.Error(), "install.sh") {
			t.Fatalf("update of %s: %v", current, err)
		}
		assertUntouched(t, target, current)
	}
}

func TestUpdateRefusesWhileARunGoes(t *testing.T) {
	dir := setupConcurrentRunRepo(t)
	watcher(t, dir, "w", 0)
	_, release := setupBarrierAgent(t)
	if out := atmIn(t, 0, "run", "--watcher", "w", "retry.md"); out != "retry-1\n" {
		t.Fatalf("atm run: %q", out)
	}
	target := filepath.Join(t.TempDir(), "atm")
	if err := os.WriteFile(target, []byte(fakeAtm("v0.1.0")), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeRelease(t, "v0.2.0", fakeAtm("v0.2.0"), false)
	var out bytes.Buffer
	if err := update(&out, "v0.1.0", target); err == nil || !strings.Contains(err.Error(), "retry-1") {
		t.Fatalf("update while retry-1 runs: %v", err)
	}
	assertUntouched(t, target, "v0.1.0")
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var runs struct{ Runs []map[string]string }
		if err := json.Unmarshal([]byte(atmIn(t, 0, "axi", "runs", "--json")), &runs); err != nil {
			t.Fatal(err)
		}
		if len(runs.Runs) == 1 && runs.Runs[0]["outcome"] == "failed" {
			return
		}
	}
	t.Fatal("retry-1 did not finish after releasing the agent")
}

func assertUntouched(t *testing.T, target, tag string) {
	t.Helper()
	if b, err := os.ReadFile(target); err != nil || string(b) != fakeAtm(tag) {
		t.Fatalf("atm was touched: %q, %v", b, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(target)); len(entries) != 1 {
		t.Fatalf("left %v next to atm", entries)
	}
}

// installSh runs the repository's install.sh into a new prefix, the fake release server's.
func installSh(t *testing.T, prefix string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join("..", "..", "install.sh"))
	cmd.Env = append(os.Environ(), "ATM_PREFIX="+prefix)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallShInstallsTheLatestRelease(t *testing.T) {
	fakeRelease(t, "v0.2.0", fakeAtm("v0.2.0"), false)
	prefix := filepath.Join(t.TempDir(), "bin")
	if out, err := installSh(t, prefix); err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	assertVersion(t, filepath.Join(prefix, "atm"), "v0.2.0")
}

func TestInstallShRefusesAWrongChecksumWritingNothing(t *testing.T) {
	fakeRelease(t, "v0.2.0", fakeAtm("v0.2.0"), true)
	prefix := t.TempDir()
	if out, err := installSh(t, prefix); err == nil || !strings.Contains(out, "sha256") {
		t.Fatalf("install.sh with a wrong sum: %v\n%s", err, out)
	}
	if entries, _ := os.ReadDir(prefix); len(entries) != 0 {
		t.Fatalf("install.sh wrote %v", entries)
	}
}

func TestInstallShLeavesAForeignAtmAlone(t *testing.T) {
	fakeRelease(t, "v0.2.0", fakeAtm("v0.2.0"), false)
	prefix := t.TempDir()
	foreign := "#!/bin/sh\necho 'automated teller machine'\n"
	if err := os.WriteFile(filepath.Join(prefix, "atm"), []byte(foreign), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := installSh(t, prefix); err == nil || !strings.Contains(out, "not atm") {
		t.Fatalf("install.sh over a foreign atm: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(prefix, "atm")); string(b) != foreign {
		t.Fatalf("install.sh replaced a foreign atm with %q", b)
	}
}

// TestPublishedReleaseAnswersItsVersion is the real artefact: ATM_RELEASE_TAG's archive for this machine,
// from GitHub, verified against its checksums.txt, answers --version with that tag.
func TestPublishedReleaseAnswersItsVersion(t *testing.T) {
	tag := os.Getenv("ATM_RELEASE_TAG")
	if tag == "" {
		t.Skip("ATM_RELEASE_TAG names no published release")
	}
	rel, err := release("tags/" + tag)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := rel.binary()
	if err != nil {
		t.Fatal(err)
	}
	atm := filepath.Join(t.TempDir(), "atm")
	if err := os.WriteFile(atm, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	assertVersion(t, atm, tag)
}
