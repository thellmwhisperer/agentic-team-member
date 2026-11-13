//go:build unix

package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMachineRepoConfigRunsWithoutCommittedConfig(t *testing.T) {
	bin := builtATM(t)
	for _, committed := range []bool{false, true} {
		name, base := "machine config", ""
		if committed {
			name, base = "committed config wins", atmYAML
		}
		t.Run(name, func(t *testing.T) {
			top := repo(t, "https://example.com/client/project.git", base)
			stopServing(t, top)
			cfg := atmSet(atmYAML, "install", "printf installed > installed")
			cfg = atmSet(cfg, "test", "sh a_test.sh && test -f installed")
			cfg = atmSet(cfg, "typecheck", "test -f installed")
			cfg = atmSet(cfg, "lint", "test -f installed")
			cfg += "harness: codex\nmodel: machine-repo-model\neffort: high\n" +
				"e2e: test -f installed\ncompile: test ! -e .atm.yaml\nagent_timeout: 1\ncheck_timeout: 1\n"
			if committed {
				cfg = "invalid_override_key: ignored\n"
			}
			writeMachineRepo(t, "client/project", cfg)
			cmd := exec.Command(bin, "doctor")
			cmd.Dir = top
			if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "ok .atm.yaml") {
				t.Fatalf("atm doctor: %v\n%s", err, out)
			}
			f := startFollow(t, bin, top, false, "run", "--follow", issueFile(t, issue))
			f.exit(0)
			r := e2eReport(t, top, "issue-1")
			if r.FailedNode != "" || r.node("clone").Result != "passed" || r.node("checks").Result != "passed" {
				t.Fatalf("machine-config run did not finish its checks: %+v", r)
			}
			if !committed {
				assertMachineRepoRun(t, top, f, r)
			}
		})
	}
}

func assertMachineRepoRun(t *testing.T, top string, f *followed, r *verdict) {
	t.Helper()
	var commands []string
	for _, c := range r.Commands {
		commands = append(commands, c.Name)
		if c.Result != "passed" {
			t.Fatalf("machine repository command failed: %+v", c)
		}
	}
	if strings.Join(commands, " ") != "install install test e2e typecheck lint" {
		t.Fatalf("machine repository commands: %v", commands)
	}
	if r.Agent.Harness != "codex" || r.Agent.Model != "machine-repo-model" || r.Agent.Effort != "high" {
		t.Fatalf("machine repository agent was not used: %+v", r.Agent)
	}
	if strings.Contains(f.errors(), "working tree's .atm.yaml differs") {
		t.Fatalf("machine config caused a spurious working-tree warning: %s", f.errors())
	}
	if _, err := os.Stat(filepath.Join(top, ".atm.yaml")); !os.IsNotExist(err) {
		t.Fatalf("run wrote a project config: %v", err)
	}
}

func TestMachineRepoConfigRejectsMissingOrInvalidEntries(t *testing.T) {
	bin := builtATM(t)
	for _, c := range []struct{ name, origin, key, cfg, want string }{
		{"another repository", "https://example.com/client/project.git", "client/another", atmYAML, "config incomplete"},
		{"no origin", "local", "client/project", atmYAML, "config incomplete"},
		{"unknown key", "git@example.com:client/project.git", "client/project", atmYAML + "unknown: true\n", "unknown"},
		{"missing key", "ssh://git@example.com/client/project.git/", "client/project", "test: true\n", "config incomplete"},
	} {
		t.Run(c.name, func(t *testing.T) {
			top := repo(t, c.origin, "")
			writeMachineRepo(t, c.key, c.cfg)
			out, code := preflightLaunch(t, bin, top, issueFile(t, issue))
			assertStopped(t, top, out, code, "config", c.want)
		})
	}
}

func writeMachineRepo(t *testing.T, key, cfg string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".config", "atm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	text := "harness: claude\nrepository_overrides:\n  " + key + ":\n    " +
		strings.ReplaceAll(strings.TrimSpace(cfg), "\n", "\n    ") + "\n"
	writeFixture(t, filepath.Join(dir, "config.yaml"), text)
}
