package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

type releaseWorkflowModel struct {
	On struct {
		Push struct {
			Branches []string `yaml:"branches"`
		} `yaml:"push"`
	} `yaml:"on"`
	Concurrency struct {
		Group string `yaml:"group"`
	} `yaml:"concurrency"`
	Jobs map[string]releaseWorkflowJob `yaml:"jobs"`
}

type releaseWorkflowJob struct {
	Needs    any    `yaml:"needs"`
	If       string `yaml:"if"`
	Strategy struct {
		Matrix struct {
			Include []struct {
				GOOS   string `yaml:"goos"`
				GOARCH string `yaml:"goarch"`
			} `yaml:"include"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
	Steps []struct {
		Uses string         `yaml:"uses"`
		Run  string         `yaml:"run"`
		With map[string]any `yaml:"with"`
	} `yaml:"steps"`
}

func workflowModel(t *testing.T) releaseWorkflowModel {
	t.Helper()
	var workflow releaseWorkflowModel
	if err := yaml.Unmarshal([]byte(releaseWorkflow(t)), &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func needsList(needs any) []string {
	switch needs := needs.(type) {
	case string:
		return []string{needs}
	case []any:
		result := make([]string, 0, len(needs))
		for _, need := range needs {
			if name, ok := need.(string); ok {
				result = append(result, name)
			}
		}
		return result
	default:
		return nil
	}
}

func hasRunStep(job releaseWorkflowJob, expected ...string) bool {
	for _, step := range job.Steps {
		var active strings.Builder
		for _, line := range strings.Split(step.Run, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "#") {
				active.WriteString(line)
				active.WriteByte('\n')
			}
		}
		run := active.String()
		if run == "" {
			continue
		}
		matched := true
		for _, fragment := range expected {
			matched = matched && strings.Contains(run, fragment)
		}
		if matched {
			return true
		}
	}
	return false
}

func TestReleaseWorkflowUsesScopedConcurrencyGroup(t *testing.T) {
	workflow := workflowModel(t)
	if workflow.Concurrency.Group != "release-${{ github.ref }}" {
		t.Fatal("release workflow must scope concurrency by ref")
	}
	if len(workflow.On.Push.Branches) != 1 || workflow.On.Push.Branches[0] != "main" {
		t.Fatal("release workflow must run on push to main")
	}
}

func TestReleaseWorkflowRunsReleasePleaseFromItsConfig(t *testing.T) {
	job := workflowModel(t).Jobs["release-please"]
	if len(needsList(job.Needs)) != 0 {
		t.Fatalf("release-please needs = %v, want no dependencies", job.Needs)
	}
	found := false
	for _, step := range job.Steps {
		if step.Uses != "googleapis/release-please-action@v4" {
			continue
		}
		found = true
		if step.With["config-file"] != "release-please-config.json" ||
			step.With["manifest-file"] != ".release-please-manifest.json" ||
			step.With["token"] != "${{ secrets.AUTO_MERGE_TOKEN }}" {
			t.Fatalf("release-please action inputs = %v", step.With)
		}
		if _, ok := step.With["release-type"]; ok {
			t.Fatal("release-please action must use release-type from its config")
		}
	}
	if !found {
		t.Fatal("release-please job must use googleapis/release-please-action@v4")
	}
}

func TestReleaseWorkflowBuildStartsOnlyWhenReleaseIsCreated(t *testing.T) {
	workflow := workflowModel(t)
	job := workflow.Jobs["build-and-upload"]
	if job.If != "needs.release-please.outputs.release_created == 'true'" {
		t.Fatal("build-and-upload must run only when release-please created a release")
	}
	if !reflect.DeepEqual(needsList(job.Needs), []string{"release-please"}) {
		t.Fatalf("build-and-upload needs = %v", job.Needs)
	}
	gotTargets := make(map[string]bool, len(job.Strategy.Matrix.Include))
	for _, target := range job.Strategy.Matrix.Include {
		gotTargets[target.GOOS+"/"+target.GOARCH] = true
	}
	wantTargets := []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64"}
	if len(gotTargets) != len(wantTargets) {
		t.Fatalf("build matrix = %v, want %v", gotTargets, wantTargets)
	}
	for _, target := range wantTargets {
		if !gotTargets[target] {
			t.Fatalf("build matrix = %v, missing %s", gotTargets, target)
		}
	}
	if !hasRunStep(job,
		"CGO_ENABLED=0 GOOS=\"$GOOS\" GOARCH=\"$GOARCH\"",
		"go build -ldflags",
		"-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Version=${TAG}",
		"-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Commit=${COMMIT}",
		"-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Date=${DATE}") {
		t.Fatal("build-and-upload must run a CGO-free build with release metadata")
	}
	if !hasRunStep(job, "gh release upload \"$TAG\" \"$ARCHIVE\" --clobber") {
		t.Fatal("build-and-upload must run the release asset upload")
	}
	artifactUpload := false
	for _, step := range job.Steps {
		artifactUpload = artifactUpload || strings.HasPrefix(step.Uses, "actions/upload-artifact@")
	}
	if !artifactUpload {
		t.Fatal("build-and-upload must save archives as workflow artifacts")
	}
}

func TestReleaseArchivesAreNamedAfterAtmAndTheTag(t *testing.T) {
	workflow := workflowModel(t)
	if !hasRunStep(workflow.Jobs["build-and-upload"],
		"ARCHIVE=\"dist/atm-${TAG}-${GOOS}-${GOARCH}.zip\"",
		"ARCHIVE=\"dist/atm-${TAG}-${GOOS}-${GOARCH}.tar.gz\"") {
		t.Fatal("build-and-upload must run commands that name tagged atm archives")
	}
	checksums := workflow.Jobs["checksums"]
	if !reflect.DeepEqual(needsList(checksums.Needs), []string{"release-please", "build-and-upload"}) {
		t.Fatalf("checksums needs = %v", checksums.Needs)
	}
	if !hasRunStep(checksums, "sha256sum atm-* > ../checksums.txt") {
		t.Fatal("checksums must cover every atm-* archive")
	}
	if !hasRunStep(checksums, "gh release upload \"$TAG\" checksums.txt --clobber") {
		t.Fatal("checksums job must run the release checksum upload")
	}
}

func TestReleaseWorkflowPublishesPrereleaseOnlyAfterAssetsComplete(t *testing.T) {
	job := workflowModel(t).Jobs["finalize"]
	if !reflect.DeepEqual(needsList(job.Needs), []string{"release-please", "build-and-upload", "checksums"}) {
		t.Fatalf("finalize needs = %v", job.Needs)
	}
	wantCondition := "!cancelled() && needs.release-please.result == 'success' && " +
		"needs.build-and-upload.result == 'success' && needs.checksums.result == 'success' && " +
		"needs.release-please.outputs.release_created == 'true'"
	if strings.Join(strings.Fields(job.If), " ") != wantCondition {
		t.Fatalf("finalize condition = %q, want %q", job.If, wantCondition)
	}
	if !hasRunStep(job, "gh release edit \"$TAG\" --draft=false --prerelease=true") {
		t.Fatal("finalize must run the prerelease publication command")
	}
	if hasRunStep(job, "--latest=true") {
		t.Fatal("finalize must not mark the release latest")
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
	if !regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`).MatchString(manifest["."]) {
		t.Fatalf("manifest version = %q, want semver X.Y.Z", manifest["."])
	}
}

func TestVersionFlagPrintsBuildVersion(t *testing.T) {
	// -buildvcs=false: since Go 1.24 a build inside a git checkout stamps a pseudo-version,
	// which CurrentVersion would print.
	for _, tc := range []struct{ ldflags, want string }{
		{"", "atm dev "},
		{"-X github.com/thellmwhisperer/agentic-team-member/internal/buildinfo.Version=v1.2.3", "atm v1.2.3 "},
	} {
		name := "atm"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		bin := filepath.Join(t.TempDir(), name)
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
