package run

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

// preflightHelp is what atm run checks before it hands a run to the background process (#506).
const preflightHelp = `Before the run, atm run checks what it stands on and exits 2 at the first check that fails,
with no clone, no run directory and no agent:
  config        .atm.yaml as the base ref has it, which the run uses; a working tree's that differs gets a warning
  base green    the latest completed CI run of each workflow on the base ref's head passed or was skipped, as GitHub says;
                skipped when origin is not on GitHub
  worker files  each file a --harness-arg flag names, as --settings or --skill, exists in the base tree (saved base on resume) or is an absolute readable file
  delivery      the first word of delivery is a command sh finds: on PATH, or sh's own, as if or cd`

// fileFlags are the harness flags whose value is a file the agent reads. A value that starts with { is inline JSON.
var fileFlags = []string{"--settings", "--skill"}

// preflight checks, in the repository at root, what a run from base ref ref stands on, its agent a with the
// harness arguments args: the first check that fails is the error, which names it.
func preflight(root, ref, workerBase string, a agent, args []string) (string, error) {
	var c config.Config
	var sha string
	for _, check := range []struct {
		name string
		fn   func() error
	}{
		{"config", func() error {
			home, err := os.UserHomeDir()
			if err == nil {
				_, err = git(root, "fetch", "-q", "origin", "+refs/heads/"+ref+":refs/remotes/origin/"+ref)
			}
			if err == nil {
				sha, err = git(root, "rev-parse", "--verify", "refs/remotes/origin/"+ref+"^{commit}")
			}
			var b string
			if err == nil {
				c, b, err = baseConfig(root, sha, home, a.Agent)
			}
			if wt, _ := os.ReadFile(filepath.Join(root, ".atm.yaml")); err == nil && string(wt) != b {
				fmt.Fprintf(os.Stderr, "preflight: config: warning: the working tree's .atm.yaml differs from %s's, "+
					"which the run uses\n", ref)
			}
			return err
		}},
		{"base green", func() error { return baseGreen(sha) }},
		{"worker files", func() error { return workerFiles(root, cmp.Or(workerBase, sha), args) }},
		{"delivery", func() error { // sh runs it: its first word is a command sh finds, on PATH or its own
			word := strings.TrimLeftFunc(c.Delivery, unicode.IsSpace)
			var quote rune
			var first strings.Builder
			for _, r := range word {
				if quote != 0 {
					if r == quote {
						quote = 0
					} else {
						first.WriteRune(r)
					}
				} else if r == '\'' || r == '"' {
					quote = r
				} else if unicode.IsSpace(r) || strings.ContainsRune(";&|<>()", r) {
					break
				} else {
					first.WriteRune(r)
				}
			}
			word = first.String()
			if strings.TrimSpace(c.Delivery) == "" {
				word = "true"
			}
			if _, err := command("", "sh", "-c", `command -v "$1"`, "sh", word); err != nil {
				return fmt.Errorf("%q is not on PATH nor a word of sh", word)
			}
			return nil
		}},
	} {
		if err := check.fn(); err != nil {
			return "", fmt.Errorf("preflight: %s: %w", check.name, err)
		}
	}
	return sha, nil
}

// baseConfig is the configuration of the repository at root with .atm.yaml as base ref ref has it, whatever
// the working tree's says, and that file.
func baseConfig(root, ref, home string, flags config.Agent) (config.Config, string, error) {
	b, err := gitRaw(root, "show", ref+":.atm.yaml")
	if err != nil {
		return config.Config{}, "", fmt.Errorf("base ref %q: %w", ref, err)
	}
	c, err := config.Parse(ref+":.atm.yaml", []byte(b), home, flags)
	return c, b, err
}

// baseGreen is why commit sha is not green: the latest completed run of one of its workflows did not pass, or
// none completed, as GitHub says in one call. A repository whose origin is not on GitHub skips it, saying so.
func baseGreen(sha string) error {
	repo := GitHubRepo()
	if repo == "" {
		fmt.Fprintln(os.Stderr, "preflight: base green: skipped, origin is not on GitHub")
		return nil
	}
	out, err := command("", "gh", "run", "list", "--repo", repo, "--commit", sha, "--status", "completed",
		"--json", "workflowName,conclusion")
	var runs []struct{ WorkflowName, Conclusion string }
	if err == nil {
		err = json.Unmarshal([]byte(out), &runs)
	}
	if err == nil && len(runs) == 0 {
		err = fmt.Errorf("no completed CI run on %s", sha)
	}
	seen := map[string]bool{}
	for _, r := range runs { // newest first: each workflow's first is its latest
		if !seen[r.WorkflowName] && r.Conclusion != "success" && r.Conclusion != "skipped" {
			return fmt.Errorf("%s on %s: %s", r.WorkflowName, sha, r.Conclusion)
		}
		seen[r.WorkflowName] = true
	}
	return err
}

// workerFiles is why a file one of the harness arguments args names after a file flag, as --settings f or
// --settings=f, cannot be read.
func workerFiles(root, ref string, args []string) error {
	for i, arg := range args {
		name, path, inline := strings.Cut(arg, "=")
		if !inline && i+1 < len(args) {
			path = args[i+1]
		}
		if !slices.Contains(fileFlags, name) || name == "--settings" && strings.HasPrefix(path, "{") {
			continue
		}
		if !filepath.IsAbs(path) {
			if _, err := git(root, "cat-file", "-e", ref+":"+filepath.ToSlash(filepath.Clean(path))); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		_ = f.Close()
	}
	return nil
}
