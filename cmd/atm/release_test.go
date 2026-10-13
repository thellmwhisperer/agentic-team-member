package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func releaseWorkflow(t *testing.T) string {
	t.Helper()
	return readFile(t, "../../.github/workflows/release.yml")
}

// jobBlock is the text of one top-level job: from its header to the next job.
func jobBlock(t *testing.T, content, name string) string {
	t.Helper()
	header := "\n  " + name + ":\n"
	start := strings.Index(content, header)
	if start < 0 {
		t.Fatalf("could not locate %s job in workflow", name)
	}
	rest := content[start+len(header):]
	if next := regexp.MustCompile(`\n  [^ #\n]`).FindStringIndex(rest); next != nil {
		return rest[:next[0]]
	}
	return rest
}

func TestReleaseWorkflowUsesScopedConcurrencyGroup(t *testing.T) {
	content := releaseWorkflow(t)
	if !strings.Contains(content, "group: release-${{ github.ref }}") {
		t.Fatal("release workflow must scope concurrency by ref")
	}
	if !strings.Contains(content, "on:\n  push:\n    branches:\n      - main\n") {
		t.Fatal("release workflow must run on push to main")
	}
}

func TestReleaseWorkflowRunsReleasePleaseFromItsConfig(t *testing.T) {
	block := jobBlock(t, releaseWorkflow(t), "release-please")
	for _, want := range []string{
		"googleapis/release-please-action@v4",
		"config-file: release-please-config.json",
		"manifest-file: .release-please-manifest.json",
		"token: ${{ secrets.AUTO_MERGE_TOKEN }}",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("release-please job must contain %q", want)
		}
	}
	for _, unwanted := range []string{"needs:", "release-type:"} {
		if strings.Contains(block, unwanted) {
			t.Fatalf("release-please job must not contain %q", unwanted)
		}
	}
}

func TestReleaseWorkflowBuildStartsOnlyWhenReleaseIsCreated(t *testing.T) {
	block := jobBlock(t, releaseWorkflow(t), "build-and-upload")
	if !strings.Contains(block, "if: needs.release-please.outputs.release_created == 'true'") {
		t.Fatal("build-and-upload must run only when release-please created a release")
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			if !strings.Contains(block, "- goos: "+goos+"\n            goarch: "+goarch+"\n") {
				t.Fatalf("build-and-upload matrix must build %s/%s", goos, goarch)
			}
		}
	}
	for _, want := range []string{
		"CGO_ENABLED=0",
		"-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Version=${TAG}",
		"gh release upload \"$TAG\" \"$ARCHIVE\" --clobber",
		"actions/upload-artifact@",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("build-and-upload must contain %q", want)
		}
	}
}

func TestReleaseArchivesAreNamedAfterAtmAndTheTag(t *testing.T) {
	content := releaseWorkflow(t)
	archives := regexp.MustCompile(`[\w$/{}.-]*\.(?:tar\.gz|zip)`).FindAllString(content, -1)
	if len(archives) == 0 {
		t.Fatal("release workflow names no archive")
	}
	for _, archive := range archives {
		if !strings.HasPrefix(filepath.Base(archive), "atm-${TAG}-") {
			t.Fatalf("archive %q must start with atm-${TAG}-", archive)
		}
	}
	if !strings.Contains(jobBlock(t, content, "checksums"), "sha256sum atm-* > ") {
		t.Fatal("checksums must cover every atm-* archive")
	}
}

func TestReleaseWorkflowPublishesPrereleaseOnlyAfterAssetsComplete(t *testing.T) {
	block := jobBlock(t, releaseWorkflow(t), "finalize")
	for _, want := range []string{
		"- release-please", "- build-and-upload", "- checksums",
		"!cancelled()",
		"needs.release-please.result == 'success'",
		"needs.build-and-upload.result == 'success'",
		"needs.checksums.result == 'success'",
		"needs.release-please.outputs.release_created == 'true'",
		"gh release edit \"$TAG\" --draft=false --prerelease=true",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("finalize job must contain %q", want)
		}
	}
	if strings.Contains(block, "--latest=true") {
		t.Fatal("finalize must never mark a release latest")
	}
}

func TestReleasePleaseConfigDraftsAndForcesTags(t *testing.T) {
	var cfg struct {
		BootstrapSHA string `json:"bootstrap-sha"`
		Packages     map[string]struct {
			ReleaseType          string `json:"release-type"`
			BumpMinorPreMajor    bool   `json:"bump-minor-pre-major"`
			BumpPatchForMinorPre bool   `json:"bump-patch-for-minor-pre-major"`
			Draft                bool   `json:"draft"`
			ForceTagCreation     bool   `json:"force-tag-creation"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(readFile(t, "../../release-please-config.json")), &cfg); err != nil {
		t.Fatal(err)
	}
	pkg, ok := cfg.Packages["."]
	if !ok || pkg.ReleaseType != "go" ||
		!pkg.BumpMinorPreMajor || !pkg.BumpPatchForMinorPre || !pkg.Draft || !pkg.ForceTagCreation {
		t.Fatalf("packages = %+v, want '.' as a go release, drafted, with forced tags and pre-major bumps", cfg.Packages)
	}
	if cfg.BootstrapSHA != "0c4d0224dc6e77e996c02bd508b0a8618f6d6072" {
		t.Fatalf("bootstrap-sha = %q", cfg.BootstrapSHA)
	}
	var manifest map[string]string
	if err := json.Unmarshal([]byte(readFile(t, "../../.release-please-manifest.json")), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["."] != "0.0.0" {
		t.Fatalf("manifest = %v, want . at 0.0.0", manifest)
	}
}

func TestVersionFlagPrintsBuildVersion(t *testing.T) {
	// -buildvcs=false: since Go 1.24 a build inside a git checkout stamps a pseudo-version,
	// which CurrentVersion would print.
	for _, tc := range []struct{ ldflags, want string }{
		{"", "atm dev "},
		{"-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Version=v1.2.3", "atm v1.2.3 "},
	} {
		bin := filepath.Join(t.TempDir(), "atm")
		build := exec.Command("go", "build", "-buildvcs=false", "-ldflags", tc.ldflags, "-o", bin, ".")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("go build: %v\n%s", err, out)
		}
		out, err := exec.Command(bin, "--version").CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), tc.want) {
			t.Fatalf("atm --version = %q, %v; want prefix %q", out, err, tc.want)
		}
	}
}
