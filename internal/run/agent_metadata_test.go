package run

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAgentMetadataRequestedAndObserved(t *testing.T) {
	for _, tc := range []struct{ harness, stream, observed string }{
		{"claude", `{"type":"system","subtype":"init","model":"m2"}`, "m2"},
		{"pi", `{"type":"message_end","message":{"role":"assistant","provider":"xai","model":"grok-4.7"}}`,
			"xai/grok-4.7"},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			root := repo(t, t.TempDir(), atmYAML)
			t.Setenv("FAKE_AGENT_STREAM", tc.stream+"\n")
			args := []string{"--harness", tc.harness, "--model", "m1", "--effort", "high", "--harness-arg", "--x",
				issueFile(t, issue)}
			if err := Run("metadata", args, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			r := readReport(t, filepath.Join(root, ".atm", "runs", "metadata", "report.json"))
			want := obj{"harness": tc.harness, "model": "m1", "effort": "high", "harness_args": []any{"--x"}}
			if !reflect.DeepEqual(r["agent"], want) {
				t.Errorf("requested agent = %v; want %v", r["agent"], want)
			}
			for _, name := range []string{"agent", "ponytail"} {
				if n := reportNode(t, r, name); n["agent_model"] != tc.observed {
					t.Errorf("%s agent_model = %v; want %q", name, n["agent_model"], tc.observed)
				}
			}
		})
	}
}

func TestAgentMetadataConfigLayers(t *testing.T) {
	for _, tc := range []struct {
		name, global, project string
		flags                 []string
		want                  obj
	}{
		{"defaults", "", "", nil,
			obj{"harness": "claude", "model": "", "effort": "", "harness_args": []any{}}},
		{"global", "harness: pi\nmodel: global\neffort: low\n", "", nil,
			obj{"harness": "pi", "model": "global", "effort": "low", "harness_args": []any{}}},
		{"project", "harness: pi\nmodel: global\neffort: low\n", "harness: claude\nmodel: project\n", nil,
			obj{"harness": "claude", "model": "project", "effort": "low", "harness_args": []any{}}},
		{"flags", "harness: pi\nmodel: global\neffort: low\n", "harness: claude\nmodel: project\n",
			[]string{"--harness", "codex", "--model", "flag", "--effort", "high", "--harness-arg", "--x",
				"--harness-arg", "value"},
			obj{"harness": "codex", "model": "flag", "effort": "high", "harness_args": []any{"--x", "value"}}},
		{"empty project", "model: global\neffort: low\n", "model: ''\neffort: ''\n", nil,
			obj{"harness": "claude", "model": "", "effort": "", "harness_args": []any{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := repo(t, t.TempDir(), atmYAML+tc.project)
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(home, ".config", "atm")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.global), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := Run("layers", append(tc.flags, issueFile(t, issue)), io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			r := readReport(t, filepath.Join(root, ".atm", "runs", "layers", "report.json"))
			if !reflect.DeepEqual(r["agent"], tc.want) {
				t.Errorf("effective agent = %v; want %v", r["agent"], tc.want)
			}
			if n := reportNode(t, r, "agent"); n["agent_model"] != "" {
				t.Errorf("unnamed observed model = %v; want empty", n["agent_model"])
			}
		})
	}
}

func TestAgentMetadataFailedInvocation(t *testing.T) {
	root := repo(t, t.TempDir(), atmYAML)
	t.Setenv("FAKE_AGENT", "fail")
	t.Setenv("FAKE_AGENT_STREAM", `{"type":"system","subtype":"init","model":"m2"}`+"\n")
	if err := Run("failed", []string{"--harness", "claude", "--model", "m1", issueFile(t, issue)},
		io.Discard, io.Discard); err == nil {
		t.Fatal("fake agent should fail")
	}
	r := readReport(t, filepath.Join(root, ".atm", "runs", "failed", "report.json"))
	if n := reportNode(t, r, "agent"); n["agent_model"] != "m2" || n["result"] != "failed" {
		t.Errorf("failed agent = %v", n)
	}
	if n := reportNode(t, r, "ponytail"); n["agent_model"] != "" || n["result"] != "not run" {
		t.Errorf("unrun slop detector = %v", n)
	}
}

func TestAgentMetadataHistoryLiveAndReloaded(t *testing.T) {
	root := repo(t, t.TempDir(), atmYAML)
	stop := background(t, root)
	wait, release := holdFakeAgents(t, 1)
	o, err := startWithWatcher(t, root, []string{"--harness", "claude", "--model", "m1", "--effort", "high",
		"--harness-arg", "--x", issueFile(t, issue)})
	if err != nil {
		t.Fatal(err)
	}
	wait()
	check := func(runs []Outcome, err error) {
		t.Helper()
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs = %v, %v", runs, err)
		}
		b, err := json.Marshal(runs[0])
		if err != nil {
			t.Fatal(err)
		}
		var r obj
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		want := obj{"harness": "claude", "model": "m1", "effort": "high", "harness_args": []any{"--x"}}
		if !reflect.DeepEqual(r["agent"], want) {
			t.Errorf("history agent = %v; want %v", r["agent"], want)
		}
	}
	check(Runs(root))
	check(RecordedRuns(root))
	release()
	if _, err := Attach(root, o.Run, io.Discard); err != nil {
		t.Fatal(err)
	}
	check(Runs(root))
	stop()
	check(RecordedRuns(root))
	reloaded, err := history(root)
	if err != nil || len(reloaded) != 1 {
		t.Fatalf("history = %v, %v", reloaded, err)
	}
	check([]Outcome{reloaded[0].Outcome}, nil)
}

func TestAgentMetadataFirstModelPerInvocation(t *testing.T) {
	root := repo(t, t.TempDir(), atmYAML)
	_, _, _, a, err := parseArgs([]string{"--harness", "claude", "--model", "requested", "issue.md"})
	if err != nil {
		t.Fatal(err)
	}
	a.dir = t.TempDir()
	r := newVerdict(a.dir, io.Discard)
	t.Setenv("FAKE_AGENT_WORK", "true")
	for i, model := range []string{"first-unit", "second-unit", ""} {
		lines := ""
		if model != "" {
			lines = `{"type":"system","subtype":"init","model":"` + model + `"}` + "\n" +
				`{"type":"system","subtype":"init","model":"later"}` + "\n"
		}
		t.Setenv("FAKE_AGENT_STREAM", lines)
		r.step("agent", func() (map[string]any, error) {
			return a.run(root, "worker", "ponytail", "Task type: fix.", testFile)
		})
		r.recordUnit(i+1, "base", "a_test.sh", "fix", "", nil)
	}
	t.Setenv("FAKE_AGENT_STREAM", `{"type":"system","subtype":"init","model":"slop-model"}`+"\n")
	r.step("ponytail", func() (map[string]any, error) {
		return a.run(root, "ponytail", "ponytail-review", "Use `ponytail-review`.", func(map[string]any) error {
			return nil
		})
	})
	if r.err != nil {
		t.Fatal(r.err)
	}
	rep := readReport(t, r.path)
	for i, model := range []string{"first-unit", "second-unit", ""} {
		if unit := rep["units"].([]any)[i].(obj); unit["agent_model"] != model {
			t.Errorf("unit %d model = %v; want %q", i+1, unit["agent_model"], model)
		}
	}
	if n := reportNode(t, rep, "ponytail"); n["agent_model"] != "slop-model" {
		t.Errorf("slop model = %v", n["agent_model"])
	}
}

func TestAgentMetadataHistoryWithoutSocket(t *testing.T) {
	root := repo(t, t.TempDir(), atmYAML)
	if err := os.MkdirAll(filepath.Join(root, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &bgRun{Outcome: Outcome{Run: "metadata-1", Outcome: "running",
		Report: filepath.Join(root, ".atm", "runs", "metadata-1", "report.json")}, more: make(chan struct{})}
	s := &server{root: root, runs: []*bgRun{r}}
	s.run(r, []string{"--harness", "pi", "--model", "m1", "--effort", "high",
		"--harness-arg", "--x", issueFile(t, issue)})
	if r.Outcome.Outcome != "passed" {
		t.Fatal(r.Reason)
	}
	runs, err := RecordedRuns(root)
	if err != nil || len(runs) != 1 {
		t.Fatalf("history = %v, %v", runs, err)
	}
	b, err := json.Marshal(runs[0])
	if err != nil {
		t.Fatal(err)
	}
	var saved obj
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	rep := readReport(t, r.Report)
	want := obj{"harness": "pi", "model": "m1", "effort": "high", "harness_args": []any{"--x"}}
	if !reflect.DeepEqual(rep["agent"], want) || !reflect.DeepEqual(saved["agent"], want) {
		t.Fatalf("report agent = %v; history agent = %v; want %v", rep["agent"], saved["agent"], want)
	}
	checkLegacyMetadataHistory(t, root)
}

func checkLegacyMetadataHistory(t *testing.T, root string) {
	t.Helper()
	old := []byte(`{"run":"legacy","outcome":"passed","ended":"2026-10-13T12:00:00Z"}` + "\n")
	f, err := os.OpenFile(filepath.Join(root, ".atm", "runs.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.Write(old)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append history: %v, %v", writeErr, closeErr)
	}
	reloaded, err := history(root)
	if err != nil || len(reloaded) != 2 {
		t.Fatalf("reload = %v, %v", reloaded, err)
	}
	b, err := json.Marshal(reloaded[1].Outcome)
	if err != nil {
		t.Fatal(err)
	}
	// Decode into a fresh map: absent fields must not acquire attribution on reload.
	var legacy obj
	if err := json.Unmarshal(b, &legacy); err != nil || legacy["agent"] != nil {
		t.Fatalf("legacy history = %s, %v", b, err)
	}
}

func TestAgentMetadataStreamShapes(t *testing.T) {
	for _, tc := range []struct{ name, harness, lines, want string }{
		{"claude first init", "claude", "{\"type\":\"system\",\"subtype\":\"init\",\"model\":\"m2\"}\n" +
			`{"type":"system","subtype":"init","model":"later"}`, "m2"},
		{"pi first assistant", "pi", `{"type":"message_end","message":{"role":"user","model":"wrong"}}` + "\n" +
			`{"type":"message_update","message":{"role":"assistant","model":"partial"}}` + "\n" +
			`{"type":"message_end","message":{"role":"assistant","provider":"xai","model":"grok-4.7"}}` + "\n" +
			`{"type":"message_end","message":{"role":"assistant","provider":"xai","model":"later"}}`, "xai/grok-4.7"},
		{"pi no provider", "pi", `{"type":"message_end","message":{"role":"assistant","model":"m2"}}`, "m2"},
		{"pi no model", "pi", `{"type":"message_end","message":{"role":"assistant","provider":"xai"}}`, ""},
		{"codex named", "codex", `{"type":"thread.started","model":"observed-codex"}`, "observed-codex"},
		{"codex unnamed", "codex", `{"type":"thread.started","thread_id":"id"}`, ""},
		{"opencode named", "opencode", `{"type":"step_start","model":"observed-opencode"}`, "observed-opencode"},
		{"opencode unnamed", "opencode", `{"type":"step_start","part":{"type":"step-start"}}`, ""},
		{"tool input ignored", "claude", `{"type":"assistant","message":{"content":[` +
			`{"type":"tool_use","name":"Read","input":{"model":"wrong"}}]}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := repo(t, t.TempDir(), atmYAML)
			t.Setenv("FAKE_AGENT_STREAM", tc.lines+"\n")
			t.Setenv("FAKE_AGENT_WORK", "true")
			_, _, _, a, err := parseArgs([]string{"--harness", tc.harness, "--model", "requested", "issue.md"})
			if err != nil {
				t.Fatal(err)
			}
			a.dir = t.TempDir()
			ev, err := a.run(root, "worker", "ponytail", "Task type: fix.", testFile)
			if err != nil {
				t.Fatal(err)
			}
			if ev["agent_model"] != tc.want {
				t.Errorf("model = %v; want %q", ev["agent_model"], tc.want)
			}
		})
	}
}
