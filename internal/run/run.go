// Package run is atm run, from inside the repository it works on: the repository root comes from git,
// the GitHub repository from origin, and everything ATM writes lives under .atm/.
package run

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

var githubURL = regexp.MustCompile(`^(?:[a-z][a-z0-9+.-]*://)?(?:[^/@]+@)?github\.com[:/]([^/]+/[^/]+?)(\.git)?/?$`)

// Usage is atm run's command line.
const Usage = "usage: atm run [--base-ref r] [--harness h] [--model m] [--effort e] [--harness-arg a]... " +
	"[--env KEY=VALUE]... <issue.md | issue number>"

// Run is atm run with args, its flags and then the issue: a file, or an issue number of origin's repository.
// Each node's start and end go to out, one JSON object a line, and to .atm/report.json; the summary goes to
// summary. ExitCode turns its error into atm's exit code.
func Run(args []string, out, summary io.Writer) (err error) {
	fs := flag.NewFlagSet("atm run", flag.ContinueOnError)
	base := fs.String("base-ref", "main", "the ref the run starts from, resolved in the repository")
	var a agent
	a.flags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(Usage)
	}
	root, err := git("", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}
	repo := githubRepo()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// The steps after the issue take the config from here.
	c, err := config.Load(root, home, a.Agent)
	if err != nil {
		return err
	}
	a.Agent = c.Agent
	r := newVerdict(root, out)
	defer func() { err = r.finish(err, summary) }()
	var i Issue
	r.step("issue", func() (map[string]any, error) {
		i, err = readIssue(fs.Arg(0), repo)
		r.Type = i.Type
		ev := map[string]any{"title": i.Title, "type": i.Type}
		if i.Number != 0 {
			ev["number"] = i.Number
		}
		return ev, err
	})
	var text string
	r.step("contract", func() (ev map[string]any, err error) {
		text, ev, err = writeBrief(filepath.Join(root, ".atm", "brief.md"), i, c, nil)
		return ev, err
	})
	var dir, sha string
	defer func() { err = errors.Join(err, release(dir, repo)) }()
	r.step("clone", func() (ev map[string]any, err error) {
		dir, sha, err = clone(root, *base, c.Install, repo)
		return map[string]any{"clone": dir, "sha": sha}, err
	})
	test, unit, pending := a.units(r, root, dir, sha, text, i, c)
	if err := saveFollowUps(root, pending); err != nil {
		return err
	}
	r.step("ponytail", func() (map[string]any, error) { return a.ponytail(root, dir, sha, test, unit, c) })
	deliver(r, root, dir, sha, *base, c.Delivery, i, summary)
	return r.err
}

// units runs the units of the run in clone dir, from base commit sha, for issue i under config c: the first on
// brief text, each next one, up to maxUnits, on the first follow-up on the issue the one before proved. It
// returns the last unit's test, the issue as that unit is proven, a chained one by its red test, and the
// accepted follow-ups it did not chain.
func (a agent) units(r *verdict, root, dir, sha, text string, i Issue, c config.Config) (test string, unit Issue,
	pending []followUp) {
	unit, head := i, sha
	var f *followUp // the follow-up a chained unit makes pass
	for n := 1; ; n++ {
		var report map[string]any
		r.step("agent", func() (map[string]any, error) {
			name, text := "worker", text
			if f != nil {
				var err error
				if head, err = chain(root, dir, n-1, i.Title, *f); err != nil {
					return nil, err
				}
				unit.Type, name = "feature", "worker-unit"+strconv.Itoa(n)
				path := filepath.Join(root, ".atm", "brief-unit-"+strconv.Itoa(n)+".md")
				if text, _, err = writeBrief(path, unit, c, f); err != nil {
					return nil, err
				}
			}
			ev, err := a.run(dir, name, "ponytail", text, testFile)
			ev["unit"] = n
			report, _ = ev["report"].(map[string]any)
			test, _ = report["test_file"].(string)
			if f != nil {
				test = f.RedTest
			}
			return ev, err
		})
		var in []followUp
		r.step("checks", func() (map[string]any, error) {
			ev := map[string]any{"unit": n}
			if f != nil {
				if b, err := os.ReadFile(filepath.Join(dir, test)); err != nil || string(b) != f.Test {
					return ev, fmt.Errorf("unit %d edited its red test %s, which it must make pass as it is", n, test)
				}
			}
			result, err := checks(dir, head, unit.Type, test, c)
			for key, value := range result {
				ev[key] = value
			}
			if err != nil {
				return ev, err
			}
			var out []followUp
			in, out, ev["follow_ups"], err = followUps(dir, report, i.Body, c.TestFile)
			pending = append(pending, out...)
			return ev, err
		})
		if r.err != nil || len(in) == 0 {
			return test, unit, pending
		}
		if n == maxUnits {
			return test, unit, append(pending, in...)
		}
		f, pending = &in[0], append(pending, in[1:]...)
	}
}

// deliver runs the delivery command line in clone, ATM_REPORT naming report.json, which is on disk already.
var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// deliver puts the run's work in clone, at base commit sha, on branch atm/<slug>-<timestamp> (onBranch),
// rewrites report.json with its SHA and runs the delivery command line in clone, its output on screen and in
// delivery-output.txt next to report.json. ATM never pushes: that is line's business.
// ponytail: line gets the clone after the slop detector, under sh's timeout; a ceiling of its own is the upgrade.
func deliver(r *verdict, root, clone, sha, base, line string, i Issue, screen io.Writer) {
	if line == "" {
		if r.err == nil {
			r.node("delivery").Result = "skipped"
		}
		return
	}
	r.step("delivery", func() (map[string]any, error) {
		s := nonSlug.ReplaceAllString(strings.ToLower(i.Title), "-")
		branch := "atm/" + cmp.Or(strings.Trim(s[:min(len(s), 40)], "-"), "run") + "-" +
			strings.TrimPrefix(filepath.Base(clone), "atm-run-") // the clone's timestamp, as unique
		ev := map[string]any{"branch": branch}
		cuts, err := onBranch(root, clone, sha, branch, i.Title)
		if err == nil {
			err = os.WriteFile(clone+".delivered", []byte(branch+" "+base+"\n"), 0o644)
		}
		if err == nil {
			r.HeadSHA, err = git(clone, "rev-parse", "HEAD")
		}
		if err == nil {
			err = r.write()
		}
		if err != nil {
			return ev, err
		}
		f, err := os.Create(filepath.Join(filepath.Dir(r.path), "delivery-output.txt"))
		if err != nil {
			return ev, err
		}
		defer func() { _ = f.Close() }()
		issue := ""
		if i.Number != 0 {
			issue = strconv.Itoa(i.Number)
		}
		tail, err := tee(io.MultiWriter(f, screen), clone, line, "ATM_TITLE="+i.Title, "ATM_ISSUE="+issue,
			"ATM_BRANCH="+branch, "ATM_CLONE="+clone, "ATM_REPORT="+r.path, "ATM_PONYTAIL="+cuts)
		ev["commands"] = []cmdResult{{"delivery", line, outcome(err), tail}}
		return ev, wrap(err, "delivery")
	})
}

// onBranch puts clone, at base commit sha, on a new branch name, what is left uncommitted committed as the
// unit under the identity of the repository at root, and points clone's origin at root's. cuts are the slop
// detector's findings, one a line, when it committed a cut.
func onBranch(root, clone, sha, name, title string) (cuts string, err error) {
	origin, err := git(root, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("no origin to deliver to: %w", err)
	}
	head, err := git(clone, "rev-parse", "HEAD")
	if err == nil && head != sha {
		cuts, err = git(clone, "log", "-1", "--format=%b") // the ponytail commit's body: its findings
	}
	if err == nil {
		_, err = git(clone, "checkout", "-q", "-b", name)
	}
	var left string
	if err == nil {
		left, err = git(clone, "status", "--porcelain")
	}
	if err == nil && left != "" {
		var id []string
		if id, err = identity(root); err == nil {
			_, err = git(clone, "add", "-A")
		}
		var n int
		if err == nil {
			commits, e := git(clone, "rev-list", "--count", sha+"..HEAD")
			if e != nil {
				err = e
			} else {
				n, err = strconv.Atoi(commits)
			}
		}
		if err == nil {
			_, err = git(clone, append(id, "commit", "-q", "--no-verify", "--cleanup=verbatim", "-m",
				fmt.Sprintf("atm unit %d: %s", n+1, title))...)
		}
	}
	if err == nil {
		_, err = git(clone, "remote", "set-url", "origin", origin)
	}
	return cuts, err
}

// githubRepo is the owner/name of the GitHub repository origin names, "" when it names none. No origin is not
// an error: the repository just has no GitHub issues.
func githubRepo() string {
	url, err := git("", "remote", "get-url", "origin")
	if m := githubURL.FindStringSubmatch(url); err == nil && m != nil {
		return m[1]
	}
	return ""
}

// writeBrief writes at path the brief for issue i under config c, whose unit makes follow-up next pass, if any.
func writeBrief(path string, i Issue, c config.Config, next *followUp) (string, map[string]any, error) {
	// ponytail: the briefs go to the repository's .atm/ until node 2 gives the run its clone.
	b, err := brief(i, c, next)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		err = os.WriteFile(path, []byte(b), 0o644)
	}
	return b, map[string]any{"brief": path}, err
}

// timeout bounds every command a step runs. ponytail: one fixed ceiling, and on timeout command kills only
// the command itself, not what it started, while sh kills its group; a key in .atm.yaml and a process group
// for command are the upgrades.
var timeout = 10 * time.Minute

// agentTimeout bounds the agent. ponytail: one fixed ceiling; a flag or a key in .atm.yaml is the upgrade.
var agentTimeout = 30 * time.Minute

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
		err = fmt.Errorf("timed out after %s", human(timeout))
	}
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func git(dir string, args ...string) (string, error) {
	return command(dir, "git", args...)
}
