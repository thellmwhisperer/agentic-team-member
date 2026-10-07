package main

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func readYAML(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func TestGoReleaserBuildsAtmForTheFiveTargets(t *testing.T) {
	var cfg struct {
		Builds []struct {
			Main    string
			Targets []string
		}
		Archives []map[string]any
		Checksum map[string]any
	}
	readYAML(t, "../../.goreleaser.yaml", &cfg)

	if len(cfg.Builds) != 1 || cfg.Builds[0].Main != "./cmd/atm" {
		t.Fatalf("builds = %+v, want one build of ./cmd/atm", cfg.Builds)
	}
	want := map[string]bool{"darwin_arm64": true, "darwin_amd64": true, "linux_amd64": true, "linux_arm64": true, "windows_amd64": true}
	got := make(map[string]bool, len(cfg.Builds[0].Targets))
	for _, target := range cfg.Builds[0].Targets {
		got[target] = true
	}
	if len(cfg.Builds[0].Targets) != len(want) || !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	if len(cfg.Archives) == 0 || cfg.Checksum == nil {
		t.Fatalf("archives = %v, checksum = %v; want both", cfg.Archives, cfg.Checksum)
	}
}

func TestReleaseWorkflowRunsOnlyMakeTargetsOnVTags(t *testing.T) {
	var wf struct {
		On struct {
			Push struct{ Tags []string }
		}
		Jobs map[string]struct {
			Steps []struct{ Run string }
		}
	}
	readYAML(t, "../../.github/workflows/release.yml", &wf)

	if !reflect.DeepEqual(wf.On.Push.Tags, []string{"v*"}) {
		t.Fatalf("tags = %v, want [v*]", wf.On.Push.Tags)
	}
	var runs []string
	for _, job := range wf.Jobs {
		for _, step := range job.Steps {
			if step.Run != "" {
				fields := strings.Fields(step.Run)
				if len(fields) < 2 || fields[0] != "make" {
					t.Fatalf("run step %q does not invoke make with a target", step.Run)
				}
				runs = append(runs, fields[1:]...)
			}
		}
	}
	if !contains(runs, "release") {
		t.Fatalf("make targets = %q, want release", runs)
	}
}

func TestReleaseCheckRunsGoReleaserInSnapshotMode(t *testing.T) {
	cmd := exec.Command("make", "-n", "release-check")
	cmd.Dir = "../.."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n release-check: %v\n%s", err, output)
	}
	fields := strings.Fields(string(output))
	hasGoReleaser := false
	for _, field := range fields {
		if strings.Contains(field, "goreleaser") {
			hasGoReleaser = true
			break
		}
	}
	if !hasGoReleaser || !contains(fields, "release") || !contains(fields, "--snapshot") {
		t.Fatalf("make -n release-check = %q, want GoReleaser release --snapshot", string(output))
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
