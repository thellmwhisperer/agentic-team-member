//go:build unix

package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPreflight launches the real atm run on what a run stands on and, for each check, on what fails it: the
// launch stops before any clone, run directory or agent, names the check and why, and exits 2 (#506).
func TestPreflight(t *testing.T) {
	bin := builtATM(t)
	for name, check := range map[string]func(*testing.T, string){"config": preflightConfig,
		"base green": preflightBaseGreen, "worker files": preflightWorkerFiles, "delivery": preflightDelivery,
		"help lists the checks": preflightHelpLists, "resume": preflightResume,
		"fetched base": preflightFetchedBase, "clone relative resources": preflightRelativeResources,
		"pi resources": preflightPiResources, "shell word": preflightShellWord,
		"fetch fails": preflightFetchFails, "resume resource base": preflightResumeResources} {
		t.Run(name, func(t *testing.T) { check(t, bin) })
	}
}

// preflightConfig is the config check: .atm.yaml as main has it, the working tree's only warned about.
func preflightConfig(t *testing.T, bin string) {
	t.Run("from the base ref", func(t *testing.T) {
		root := repo(t, "local", strings.Replace(atmYAML, "delivery: \"\"\n", "", 1))
		writeConfig(t, root, atmYAML)
		out, code := preflightLaunch(t, bin, root, issueFile(t, issue))
		assertStopped(t, root, out, code, "config", gitT(t, root, "rev-parse", "main")+":.atm.yaml lacks delivery")
	})
	t.Run("only in the working tree", func(t *testing.T) {
		root := repo(t, "local", "")
		writeConfig(t, root, atmYAML)
		out, code := preflightLaunch(t, bin, root, issueFile(t, issue))
		base := gitT(t, root, "rev-parse", "main")
		assertStopped(t, root, out, code, "config", fmt.Sprintf(`base ref %q: git show %s:.atm.yaml`, base, base))
	})
	t.Run("a base ref that does not resolve", func(t *testing.T) {
		root := repo(t, "local", atmYAML)
		out, code := preflightLaunch(t, bin, root, "--base-ref", "nope", issueFile(t, issue))
		assertStopped(t, root, out, code, "config", "nope")
	})
	t.Run("a differing working tree warns", func(t *testing.T) {
		root := repo(t, "local", atmYAML)
		root = gitT(t, root, "rev-parse", "--show-toplevel") // the socket's path as atm has it
		writeConfig(t, root, "harness: [\n")
		if err := os.MkdirAll(filepath.Join(root, ".atm"), 0o755); err != nil {
			t.Fatal(err)
		}
		server := startVersionServe(t, bin, root)
		// A graceful exit flushes the real binary's coverage counters.
		defer func() { _ = os.Remove(socket(root)); _ = server.Wait() }()
		out, code := preflightLaunch(t, bin, root, issueFile(t, issue))
		warning := "preflight: config: warning: the working tree's .atm.yaml differs from main's, which the run uses\n"
		if code != 0 || strings.Count(out, warning) != 1 ||
			!strings.Contains(out, "preflight: base green: skipped, origin is not on GitHub\n") {
			t.Fatalf("exit %d, want the run launched after one warning, got:\n%s", code, out)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		end, err := Attach(root, lines[len(lines)-1], &bytes.Buffer{})
		if err != nil || end.Outcome != "passed" {
			t.Fatalf("want the run passed on main's .atm.yaml, got %+v, %v", end, err)
		}
	})
}

// preflightBaseGreen is the base green check: one gh call on main's head.
func preflightBaseGreen(t *testing.T, bin string) {
	for _, c := range []struct{ name, runs, why string }{
		{"red", `[{"workflowName":"Go","conclusion":"failure"}]`, "Go on "},
		{"no completed run", `[]`, "no completed CI run on "},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "git@github.com:owner/repo.git", atmYAML)
			args := fakeGH(t, c.runs, false)
			out, code := preflightLaunch(t, bin, root, issueFile(t, issue))
			assertStopped(t, root, out, code, "base green", c.why)
			got, _ := os.ReadFile(args)
			want := "run list --repo owner/repo --commit " + gitT(t, root, "rev-parse", "main") +
				" --status completed --json workflowName,conclusion"
			if string(got) != want {
				t.Fatalf("gh args %q, want %q", got, want)
			}
		})
	}
	t.Run("GitHub does not answer", func(t *testing.T) {
		root := repo(t, "git@github.com:owner/repo.git", atmYAML)
		fakeGH(t, "", true)
		out, code := preflightLaunch(t, bin, root, issueFile(t, issue))
		assertStopped(t, root, out, code, "base green", "gh run list")
	})
	t.Run("each workflow's latest run counts", func(t *testing.T) {
		root := repo(t, "git@github.com:owner/repo.git", atmSet(atmYAML, "delivery", "no-such-delivery"))
		fakeGH(t, `[{"workflowName":"Go","conclusion":"success"},{"workflowName":"Go","conclusion":"failure"}]`,
			false)
		out, code := preflightLaunch(t, bin, root, issueFile(t, issue))
		assertStopped(t, root, out, code, "delivery", "no-such-delivery")
	})
}

// preflightWorkerFiles is the worker files check: each --settings file exists and is readable.
func preflightWorkerFiles(t *testing.T, bin string) {
	missing := filepath.Join(t.TempDir(), "settings.json")
	unreadable := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(unreadable, []byte("{}"), 0o000); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, path string
		args       []string
	}{
		{"missing", missing, []string{"--harness-arg", "--settings", "--harness-arg", missing}},
		{"missing after =", missing, []string{"--harness-arg", "--settings=" + missing}},
		{"unreadable", unreadable, []string{"--harness-arg", "--settings", "--harness-arg", unreadable}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := repo(t, "local", atmYAML)
			out, code := preflightLaunch(t, bin, root, append(c.args, issueFile(t, issue))...)
			assertStopped(t, root, out, code, "worker files", "--settings: open "+c.path)
		})
	}
	t.Run("present, or inline JSON", func(t *testing.T) {
		present := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(present, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		root := repo(t, "local", atmSet(atmYAML, "delivery", "no-such-delivery --now"))
		out, code := preflightLaunch(t, bin, root, "--harness-arg", "--settings", "--harness-arg", present,
			"--harness-arg", `--settings={"model":"m"}`, issueFile(t, issue))
		assertStopped(t, root, out, code, "delivery", "no-such-delivery")
	})
}

// preflightDelivery is the delivery check: its first word is a command.
func preflightDelivery(t *testing.T, bin string) {
	root := repo(t, "local", atmSet(atmYAML, "delivery", "no-such-delivery --now && true"))
	out, code := preflightLaunch(t, bin, root, issueFile(t, issue))
	assertStopped(t, root, out, code, "delivery", `"no-such-delivery"`)
}

// preflightHelpLists is atm run --help naming each check.
func preflightHelpLists(t *testing.T, bin string) {
	root := repo(t, "local", atmYAML)
	out, _ := preflightLaunch(t, bin, root, "--help")
	for _, check := range []string{"\n  config ", "\n  base green ", "\n  worker files ", "\n  delivery "} {
		if !strings.Contains(out, check) {
			t.Fatalf("atm run --help lacks %q:\n%s", check, out)
		}
	}
}

// preflightResume is the checks of atm run --resume, on the harness arguments of the run it resumes.
func preflightResume(t *testing.T, bin string) {
	root := repo(t, "local", atmYAML)
	missing := filepath.Join(t.TempDir(), "settings.json")
	runs := filepath.Join(root, ".atm", "runs")
	keepResumable(t, filepath.Join(runs, "failed-1"), resumePoint{Issue: "7", BaseRef: "main",
		Base: gitT(t, root, "rev-parse", "main"), Unit: 1, Args: []string{"--settings", missing},
		Original: &Issue{Title: "Retry on timeout", Type: "fix", Text: issue}})
	out, code := preflightLaunch(t, bin, root, "--resume", "failed-1")
	assertNoStartedRun(t, root, out, code, 1, "preflight: worker files: --settings: open "+missing)
}

// writeConfig writes config as the working tree's .atm.yaml, which main does not have.
func writeConfig(t *testing.T, root, config string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".atm.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

// preflightLaunch is atm run with args, from root, bin's output and exit code.
func preflightLaunch(t *testing.T, bin, root string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"run"}, args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

// stdout is cmd's standard output, its standard error after it when cmd fails: what atm run prints on standard
// error, as its preflight's lines, is not its run's label.
func stdout(cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		out = append(out, exit.Stderr...)
	}
	return out, err
}

// assertStopped fails the test unless the launch exited 2 naming check and why, and left no .atm behind: no
// clone, no run directory, no background process to start an agent.
func assertStopped(t *testing.T, root, out string, code int, check, why string) {
	t.Helper()
	if code != 2 || !strings.Contains(out, "atm: preflight: "+check+": ") || !strings.Contains(out, why) {
		t.Fatalf("exit %d, want exit 2 at %s naming %q, got:\n%s", code, check, why, out)
	}
	if _, err := os.Stat(filepath.Join(root, ".atm")); err == nil {
		t.Fatalf("the stopped launch left .atm behind:\n%s", out)
	}
}

func assertNoStartedRun(t *testing.T, root, out string, code, wantRuns int, why string) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(root, ".atm", "runs"))
	_, cloned := os.Stat(filepath.Join(root, ".atm", "clones"))
	_, served := os.Stat(filepath.Join(root, ".atm", "serve.log"))
	if code != 2 || !strings.Contains(out, why) || len(entries) != wantRuns || cloned == nil || served == nil {
		t.Fatalf("exit %d, %d runs, want %d and %q without clone or server, got:\n%s",
			code, len(entries), wantRuns, why, out)
	}
}

// keepResumable writes, in run directory dir, a run that failed at checks with p kept to resume.
func keepResumable(t *testing.T, dir string, p resumePoint) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "resume"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(p)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "resume", "resume.json"), b, 0o600)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "report.json"), []byte(`{"resumable":true}`), 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func preflightFinished(t *testing.T, bin, root string, args ...string) Outcome {
	t.Helper()
	root = gitT(t, root, "rev-parse", "--show-toplevel")
	if err := os.MkdirAll(filepath.Join(root, ".atm"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := startVersionServe(t, bin, root)
	defer endServe(bin, root, server)
	out, code := preflightLaunch(t, bin, root, args...)
	if code != 0 {
		t.Fatalf("launch exit %d: %s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	end, err := Attach(root, lines[len(lines)-1], &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	return end
}

func preflightFetchedBase(t *testing.T, bin string) {
	for _, name := range []string{"config", "CI", "clone and background config", "custom branch", "resume"} {
		t.Run(name, func(t *testing.T) {
			root, remote, old, latest := staleBaseFixture(t, name)
			path := issueFile(t, issue)
			switch name {
			case "config":
				out, code := preflightLaunch(t, bin, root, path)
				assertStopped(t, root, out, code, "config", latest+":.atm.yaml lacks delivery")
			case "CI":
				gitT(t, root, "remote", "set-url", "origin", "git@github.com:owner/repo.git")
				localFetch(t, root, "git@github.com:owner/repo.git", remote)
				log := fakeGH(t, `[{"workflowName":"Go","conclusion":"failure"}]`, false)
				out, code := preflightLaunch(t, bin, root, path)
				assertStopped(t, root, out, code, "base green", latest)
				got, err := os.ReadFile(log)
				if err != nil || !strings.Contains(string(got), "--commit "+latest+" ") {
					t.Fatalf("CI queried %q: %v", got, err)
				}
			default:
				args := []string{path}
				if name == "custom branch" {
					args = append([]string{"--base-ref", "release"}, args...)
				}
				want := latest
				if name == "resume" {
					want = old
					keepResumable(t, filepath.Join(root, ".atm", "runs", "failed-1"), resumePoint{
						Issue: path, BaseRef: "main", Base: old, Unit: 1,
						Original: &Issue{Title: "Retry on timeout", Type: "fix", Text: issue}})
					args = []string{"--resume", "failed-1"}
				}
				end := preflightFinished(t, bin, root, args...)
				got, err := os.ReadFile(filepath.Join(root, "observed-head"))
				if end.FailedNode != "clone" || !strings.Contains(end.Reason, "37") || err != nil ||
					strings.TrimSpace(string(got)) != want {
					t.Fatalf("want fetched config and clone %s, got %+v, head %q, %v", want, end, got, err)
				}
			}
			if got := gitT(t, root, "rev-parse", "main"); got != old {
				t.Fatalf("local main moved: %s", got)
			}
		})
	}
}

func staleBaseFixture(t *testing.T, name string) (root, remote, old, latest string) {
	t.Helper()
	root = repo(t, "local", atmYAML)
	old = gitT(t, root, "rev-parse", "main")
	remote = filepath.Join(t.TempDir(), "remote")
	gitT(t, root, "clone", "-q", root, remote)
	gitT(t, root, "remote", "set-url", "origin", remote)
	if got := gitT(t, root, "rev-parse", "origin/main"); got != old {
		t.Fatalf("initial remote head %s, want %s", got, old)
	}
	config := atmYAML
	switch name {
	case "config":
		config = strings.Replace(atmYAML, "delivery: \"\"\n", "", 1)
	case "clone and background config", "custom branch", "resume":
		config = atmSet(atmYAML, "install", "git rev-parse HEAD > "+filepath.Join(root, "observed-head")+"; exit 37")
	}
	writeConfig(t, remote, config)
	gitT(t, remote, "add", ".atm.yaml")
	gitT(t, remote, "commit", "-q", "--allow-empty", "-m", "remote base")
	latest = gitT(t, remote, "rev-parse", "main")
	if name == "custom branch" {
		gitT(t, remote, "branch", "release", latest)
	}
	if got := gitT(t, root, "rev-parse", "origin/main"); got != old {
		t.Fatalf("fixture fetched %s before launch", got)
	}
	return root, remote, old, latest
}

func preflightRelativeResources(t *testing.T, bin string) {
	for _, flag := range []string{"--settings", "--skill"} {
		for _, equals := range []bool{false, true} {
			for _, tracked := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/equals=%t/tracked=%t", flag, equals, tracked), func(t *testing.T) {
					root := repo(t, "local", atmSet(atmYAML, "delivery", "no-such-delivery"))
					if err := os.WriteFile(filepath.Join(root, "resource"), []byte("{}"), 0o600); err != nil {
						t.Fatal(err)
					}
					launch := root
					if tracked {
						gitT(t, root, "add", "resource")
						gitT(t, root, "commit", "-q", "-m", "worker resource")
						launch = filepath.Join(root, "subdir")
						if err := os.Mkdir(launch, 0o755); err != nil {
							t.Fatal(err)
						}
					}
					args := []string{"--harness", "pi", "--harness-arg", flag, "--harness-arg", "resource"}
					if equals {
						args = []string{"--harness", "pi", "--harness-arg", flag + "=resource"}
					}
					out, code := preflightLaunch(t, bin, launch, append(args, issueFile(t, issue))...)
					check, why := "worker files", flag+": git cat-file"
					if tracked {
						check, why = "delivery", "no-such-delivery"
					}
					assertStopped(t, root, out, code, check, why)
				})
			}
		}
	}
}

func preflightPiResources(t *testing.T, bin string) {
	for _, path := range []string{filepath.Join(t.TempDir(), "SKILL.md"), "{missing-skill}"} {
		t.Run(path, func(t *testing.T) {
			root := repo(t, "local", atmYAML)
			out, code := preflightLaunch(t, bin, root, "--harness", "pi", "--harness-arg", "--skill="+path, issueFile(t, issue))
			assertStopped(t, root, out, code, "worker files", "--skill:")
		})
	}
	t.Run("saved relative skill", func(t *testing.T) {
		root := repo(t, "local", atmYAML)
		runs := filepath.Join(root, ".atm", "runs")
		keepResumable(t, filepath.Join(runs, "failed-1"), resumePoint{Issue: "7", BaseRef: "main",
			Base: gitT(t, root, "rev-parse", "main"), Unit: 1, Args: []string{"--skill", "missing-skill"},
			Original: &Issue{Title: "Retry on timeout", Type: "fix", Text: issue}})
		out, code := preflightLaunch(t, bin, root, "--resume", "failed-1")
		assertNoStartedRun(t, root, out, code, 1, "preflight: worker files: --skill:")
	})
}

func preflightShellWord(t *testing.T, bin string) {
	for _, line := range []string{
		"true; echo done", "'true'; echo done", `"true" && echo done`, "  true|cat", "true&",
		"true>/dev/null", "true</dev/null", "true()", "if true; then true; fi", "cd .",
	} {
		t.Run(line, func(t *testing.T) {
			config := atmSet(strings.Replace(atmYAML, `delivery: ""`, "delivery: "+strconv.Quote(line), 1), "install", "exit 37")
			root := repo(t, "local", config)
			end := preflightFinished(t, bin, root, issueFile(t, issue))
			if end.FailedNode != "clone" || !strings.Contains(end.Reason, "37") {
				t.Fatalf("shell word rejected: %+v", end)
			}
		})
	}
	t.Run("never executes delivery during preflight", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "executed")
		root := repo(t, "local", atmSet(atmSet(atmYAML, "delivery", "true; touch "+marker), "install", "exit 37"))
		end := preflightFinished(t, bin, root, issueFile(t, issue))
		if end.FailedNode != "clone" {
			t.Fatalf("unexpected outcome: %+v", end)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("delivery executed during preflight: %v", err)
		}
	})
}

func preflightFetchFails(t *testing.T, bin string) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%t", resume), func(t *testing.T) {
			root := repo(t, "local", atmYAML)
			gitT(t, root, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing-remote"))
			args := []string{issueFile(t, issue)}
			if resume {
				keepResumable(t, filepath.Join(root, ".atm", "runs", "failed-1"), resumePoint{
					Issue: "7", BaseRef: "main", Base: gitT(t, root, "rev-parse", "main"), Unit: 1,
					Original: &Issue{Title: "Retry on timeout", Type: "fix", Text: issue}})
				args = []string{"--resume", "failed-1"}
			}
			out, code := preflightLaunch(t, bin, root, args...)
			if !resume {
				assertStopped(t, root, out, code, "config", "git fetch")
			}
			wantRuns := 0
			if resume {
				wantRuns = 1
			}
			assertNoStartedRun(t, root, out, code, wantRuns, "preflight: config: git fetch")
		})
	}
}

func preflightResumeResources(t *testing.T, bin string) {
	for _, flag := range []string{"--settings", "--skill"} {
		for _, equals := range []bool{false, true} {
			for _, saved := range []bool{false, true} {
				for _, present := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/equals=%t/saved=%t/present=%t", flag, equals, saved, present), func(t *testing.T) {
						root, base := resumeResourceFixture(t, present)
						resourceArgs := []string{flag, "worker.json"}
						if equals {
							resourceArgs = []string{flag + "=worker.json"}
						}
						p := resumePoint{Issue: "7", BaseRef: "main", Base: base, Unit: 1,
							Original: &Issue{Title: "Retry on timeout", Type: "fix", Text: issue}}
						args := []string{"--resume", "failed-1"}
						if saved {
							p.Args = resourceArgs
						} else {
							for _, arg := range resourceArgs {
								args = append(args, "--harness-arg", arg)
							}
						}
						runs := filepath.Join(root, ".atm", "runs")
						keepResumable(t, filepath.Join(runs, "failed-1"), p)
						out, code := preflightLaunch(t, bin, root, args...)
						want := "preflight: worker files: " + flag + ":"
						if present {
							want = "preflight: delivery:"
						}
						assertNoStartedRun(t, root, out, code, 1, want)
					})
				}
			}
		}
	}
}

func resumeResourceFixture(t *testing.T, present bool) (root, base string) {
	t.Helper()
	root = repo(t, "local", atmSet(atmYAML, "delivery", "no-such-delivery"))
	resource := filepath.Join(root, "worker.json")
	if present {
		if err := os.WriteFile(resource, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitT(t, root, "add", "worker.json")
		gitT(t, root, "commit", "-q", "-m", "saved resource")
	}
	base = gitT(t, root, "rev-parse", "main")
	if present {
		gitT(t, root, "rm", "-q", "worker.json")
	} else {
		if err := os.WriteFile(resource, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitT(t, root, "add", "worker.json")
	}
	gitT(t, root, "commit", "-q", "-m", "change current resource")
	return root, base
}
