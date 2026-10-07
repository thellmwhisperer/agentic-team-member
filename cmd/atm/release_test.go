package main

import (
	"os"
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
	want := []string{"darwin_arm64", "darwin_amd64", "linux_amd64", "linux_arm64", "windows_amd64"}
	if got := cfg.Builds[0].Targets; !reflect.DeepEqual(got, want) {
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
				runs = append(runs, step.Run)
			}
		}
	}
	if !reflect.DeepEqual(runs, []string{"make release"}) {
		t.Fatalf("run steps = %q, want only make release", runs)
	}
}

func TestReleaseCheckRunsGoReleaserInSnapshotMode(t *testing.T) {
	data, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	_, recipe, ok := strings.Cut(string(data), "\nrelease-check:")
	if !ok || !strings.Contains(strings.SplitN(recipe, "\n\n", 2)[0], "release --snapshot") {
		t.Fatalf("Makefile has no release-check target running release --snapshot:\n%s", data)
	}
}
