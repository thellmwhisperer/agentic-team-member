package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// isolated is env with HOME in a temp dir whose ~/.config/atm/config.yaml is global, when global is not empty.
func isolated(t *testing.T, env []string, global string) []string {
	t.Helper()
	home := t.TempDir()
	if global != "" {
		writeFile(t, filepath.Join(home, ".config", "atm", "config.yaml"), global)
	}
	return append(env, "HOME="+home, "USERPROFILE="+home)
}

// gitOnly is env whose PATH holds git and nothing else: no agent CLI, no gh.
func gitOnly(t *testing.T, env []string) []string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(dir, "git"+exe)); err != nil {
		t.Fatal(err)
	}
	return append(env, "PATH="+dir)
}

func TestInitTwiceChangesNothing(t *testing.T) {
	repo := target(t)
	env, _ := fakes(t, "fixing")
	env = isolated(t, env, "")
	var before []string
	for range 2 {
		if out, exit := atm(t, repo, env, "init"); exit != 0 {
			t.Fatalf("exit %d:\n%s", exit, out)
		}
		before = append(before, readFile(t, filepath.Join(repo, ".atm.yaml"))+readFile(t, filepath.Join(repo, ".gitignore")))
	}
	if before[0] != before[1] {
		t.Errorf("a second atm init changed .atm.yaml or .gitignore:\n%s\n---\n%s", before[0], before[1])
	}
	if text := readFile(t, filepath.Join(repo, ".atm.yaml")); !strings.Contains(text, "#") {
		t.Errorf("the .atm.yaml atm init writes has no comments:\n%s", text)
	}
	// The file init writes is one the config loader accepts.
	if out, exit := atm(t, repo, env, "doctor"); exit != 0 {
		t.Errorf("atm doctor after atm init exits %d:\n%s", exit, out)
	}
}

func TestDoctorChecksGitAndTheAgent(t *testing.T) {
	repo := target(t)
	env, _ := fakes(t, "fixing")
	env = isolated(t, env, "")
	out, exit := atm(t, repo, env, "doctor")
	if exit != 0 || len(strings.Split(strings.TrimSpace(out), "\n")) != 2 ||
		!regexp.MustCompile(`(?m)^ok\s+git\b`).MatchString(out) ||
		!regexp.MustCompile(`(?m)^ok\s+claude\b`).MatchString(out) {
		t.Errorf("exit %d, want 0 and one ok line for git and one for claude, no SCM without origin:\n%s", exit, out)
	}
	out, exit = atm(t, repo, gitOnly(t, env), "doctor")
	if exit == 0 || !regexp.MustCompile(`(?m)^FAIL\s+claude\b`).MatchString(out) ||
		!regexp.MustCompile(`(?m)^ok\s+git\b`).MatchString(out) {
		t.Errorf("exit %d, want non-zero and a FAIL line for claude:\n%s", exit, out)
	}
}

func TestDoctorChecksGhWhenOriginIsGitHub(t *testing.T) {
	repo := target(t)
	git(t, repo, "remote", "add", "origin", "git@github.com:octo/calc.git")
	env, _ := fakes(t, "fixing")
	env = isolated(t, env, "")
	out, exit := atm(t, repo, env, "doctor")
	if exit != 0 || !regexp.MustCompile(`(?m)^ok\s+gh\b.*octo/calc`).MatchString(out) {
		t.Errorf("exit %d, want 0 and an ok line for gh naming octo/calc:\n%s", exit, out)
	}
	out, exit = atm(t, repo, gitOnly(t, env), "doctor")
	if exit == 0 || !regexp.MustCompile(`(?m)^FAIL\s+gh\b`).MatchString(out) {
		t.Errorf("exit %d, want non-zero and a FAIL line for gh:\n%s", exit, out)
	}
}

// #150 point 5: built-in defaults, then ~/.config/atm/config.yaml, then .atm.yaml, then flags.
func TestConfigLayersOverrideInOrder(t *testing.T) {
	repo := target(t)
	writeFile(t, filepath.Join(repo, ".atm.yaml"), "model: project\n")
	env, _ := fakes(t, "fixing")
	env = isolated(t, env, "harness: codex\nmodel: global\neffort: low\n")
	out, exit := atm(t, repo, env, "doctor", "--effort", "high")
	if exit != 0 || !regexp.MustCompile(`(?m)^ok\s+codex\b.*model project, effort high`).MatchString(out) {
		t.Errorf("exit %d, want codex from the global config, model from .atm.yaml, effort from the flag:\n%s",
			exit, out)
	}
	writeFile(t, filepath.Join(repo, ".atm.yaml"), "modle: typo\n")
	if out, exit := atm(t, repo, env, "doctor"); exit == 0 {
		t.Errorf("a .atm.yaml key nobody reads exits 0:\n%s", out)
	}
}
