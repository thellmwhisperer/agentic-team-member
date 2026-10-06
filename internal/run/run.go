// Package run is atm run, from inside the repository it works on: the repository root comes from git,
// the GitHub repository from origin, and everything ATM writes lives under .atm/.
package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

var githubURL = regexp.MustCompile(`^(?:[a-z][a-z0-9+.-]*://)?(?:[^/@]+@)?github\.com[:/]([^/]+/[^/]+?)(\.git)?/?$`)

// Run is atm run with args, its flags and then the issue: a file, or an issue number of origin's repository.
// Each step's start and end go to out, one JSON object a line.
func Run(args []string, out io.Writer) (err error) {
	fs := flag.NewFlagSet("atm run", flag.ContinueOnError)
	var flags config.Agent
	base := fs.String("base-ref", "main", "the ref the run starts from, resolved in the repository")
	fs.StringVar(&flags.Harness, "harness", "", "agent CLI: claude, codex, opencode or pi")
	fs.StringVar(&flags.Model, "model", "", "the agent's model")
	fs.StringVar(&flags.Effort, "effort", "", "the agent's effort")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: atm run [--base-ref r] [--harness h] [--model m] [--effort e] <issue.md | issue number>")
	}
	root, err := git("", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}
	repo := ""
	// No origin is not an error: the repository just has no GitHub issues.
	if url, err := git("", "remote", "get-url", "origin"); err == nil {
		if m := githubURL.FindStringSubmatch(url); m != nil {
			repo = m[1]
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// The steps after the issue take the config from here.
	c, err := config.Load(root, home, flags)
	if err != nil {
		return err
	}
	var i Issue
	if err := step(out, "issue", func() (map[string]any, error) {
		i, err = readIssue(fs.Arg(0), repo)
		ev := map[string]any{"title": i.Title, "type": i.Type}
		if i.Number != 0 {
			ev["number"] = i.Number
		}
		return ev, err
	}); err != nil {
		return err
	}
	if err := step(out, "contract", func() (map[string]any, error) {
		// ponytail: brief.md goes to the repository's .atm/ until node 2 gives the run its clone.
		path := filepath.Join(root, ".atm", "brief.md")
		b, err := brief(i, c)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(path), 0o755)
		}
		if err == nil {
			err = os.WriteFile(path, []byte(b), 0o644)
		}
		return map[string]any{"brief": path}, err
	}); err != nil {
		return err
	}
	var dir string
	defer func() { err = errors.Join(err, release(dir, repo)) }()
	return step(out, "clone", func() (map[string]any, error) {
		d, sha, err := clone(root, *base, c.Install, repo)
		dir = d
		return map[string]any{"clone": d, "sha": sha}, err
	})
}

// timeout bounds every command a step runs. ponytail: one fixed ceiling, and on timeout only the command
// itself is killed, not what it started; a key in .atm.yaml and a process group are the upgrades.
var timeout = 10 * time.Minute

// command runs name in dir, "" for the working directory, and returns its trimmed stdout; a failure or a
// timeout carries stderr.
func command(dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Dir, cmd.Stderr, cmd.WaitDelay = dir, &stderr, time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func git(dir string, args ...string) (string, error) {
	return command(dir, "git", args...)
}

// step writes name's start event, runs fn, and writes its end event with fn's fields or its error.
func step(out io.Writer, name string, fn func() (map[string]any, error)) error {
	emit := func(ev map[string]any) {
		ev["ts"], ev["step"] = time.Now().UTC().Format(time.RFC3339Nano), name
		b, _ := json.Marshal(ev)
		_, _ = out.Write(append(b, '\n'))
	}
	emit(map[string]any{"state": "started"})
	ev, err := fn()
	if err != nil {
		emit(map[string]any{"state": "failed", "error": err.Error()})
		return err
	}
	ev["state"] = "passed"
	emit(ev)
	return nil
}
