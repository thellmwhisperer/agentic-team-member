package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The template repo copies must not race a detached git maintenance that its commit started.
func TestRepoTemplateCommitDoesNotStartMaintenance(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace2.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "maintenance.auto")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	dir, err := initRepo()
	if dir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	if err != nil {
		t.Fatal(err)
	}
	commits, children := traced(t, trace)
	for _, args := range children {
		if strings.Contains(args, "maintenance") {
			t.Errorf("template creation started git maintenance: %s", args)
		}
	}
	if commits != 1 {
		t.Fatalf("traced %d commits, want 1: the trace did not observe template creation", commits)
	}
	if b := gitT(t, dir, "branch", "--show-current"); b != "main" {
		t.Errorf("branch %q, want main", b)
	}
	if n := gitT(t, dir, "rev-list", "--count", "HEAD"); n != "1" {
		t.Errorf("%s commits, want 1", n)
	}
	for name, want := range map[string]string{"a.txt": "broken\n", "a_test.sh": "test -f a.txt\n"} {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
}

// traced counts the git commits in a TRACE2 event file and lists the child commands git started.
func traced(t *testing.T, trace string) (commits int, children []string) {
	t.Helper()
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		args := strings.Join(ev.Argv, " ")
		switch {
		case ev.Event == "start" && strings.Contains(args, " commit "):
			commits++
		case ev.Event == "child_start":
			children = append(children, args)
		}
	}
	return commits, children
}
