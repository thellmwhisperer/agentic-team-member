// Package run is atm run, from inside the repository it works on: the repository root comes from git,
// the GitHub repository from origin, and everything ATM writes lives under .atm/.
package run

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

var githubURL = regexp.MustCompile(`^(?:[a-z][a-z0-9+.-]*://)?(?:[^/@]+@)?github\.com[:/]([^/]+/[^/]+?)(\.git)?/?$`)

// Run is atm run with args, its flags and then the issue: a file, or an issue number of origin's repository.
// Each step's start and end go to out, one JSON object a line.
func Run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("atm run", flag.ContinueOnError)
	var flags config.Agent
	fs.StringVar(&flags.Harness, "harness", "", "agent CLI: claude, codex, opencode or pi")
	fs.StringVar(&flags.Model, "model", "", "the agent's model")
	fs.StringVar(&flags.Effort, "effort", "", "the agent's effort")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: atm run [--harness h] [--model m] [--effort e] <issue.md | issue number>")
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}
	repo := ""
	// No origin is not an error: the repository just has no GitHub issues.
	if url, err := git("-C", root, "remote", "get-url", "origin"); err == nil {
		if m := githubURL.FindStringSubmatch(url); m != nil {
			repo = m[1]
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// The steps after the issue take the config from here.
	if _, err := config.Load(root, home, flags); err != nil {
		return err
	}
	return step(out, "issue", func() (map[string]any, error) {
		i, err := readIssue(fs.Arg(0), repo)
		ev := map[string]any{"title": i.Title, "type": i.Type}
		if i.Number != 0 {
			ev["number"] = i.Number
		}
		return ev, err
	})
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
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
