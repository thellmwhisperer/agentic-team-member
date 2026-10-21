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
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thellmwhisperer/agentic-team-member/internal/config"
)

var githubURL = regexp.MustCompile(`^(?:[a-z][a-z0-9+.-]*://)?(?:[^/@]+@)?github\.com[:/]([^/]+/[^/]+?)(\.git)?/?$`)

// Usage is atm run's command line.
const Usage = "usage: atm run [--follow] [--base-ref r] [--harness h] [--model m] [--effort e] " +
	"[--harness-arg a]... [--env KEY=VALUE]... <issue.md | issue number | --resume run>"

// runAt is atm run with args, its flags and the issue: a file, or an issue number of origin's repository.
// Its report, briefs and logs go to .atm/runs/<label>/ (runDir): each node's start and end go to out, one JSON
// object a line, and to report.json there; the summary goes to summary. ExitCode turns its error into atm's
// exit code. With --resume, it picks up the work a failed run kept.
func runAt(label, ref string, args []string, out, summary io.Writer) (err error) {
	args, base, resume, a, err := parseArgs(args)
	if err != nil {
		return err
	}
	root, err := git("", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}
	from, start, err := resuming(root, *resume, base)
	if err != nil {
		return err
	}
	if from == nil {
		start = ref
	}
	repo := GitHubRepo()
	c, err := configureAgent(root, ref, label, a)
	if err != nil {
		return err
	}
	r := a.verdict(out)
	defer func() { r.resumable(a.dir); err = r.finish(err, summary) }()
	var i Issue
	r.step("issue", func() (map[string]any, error) {
		i, err = theIssue(from, args[len(args)-1], repo)
		ev := map[string]any{"title": i.Title}
		if i.Number != 0 {
			ev["number"] = i.Number
		}
		return ev, err
	})
	var dir, sha string
	defer func() { err = errors.Join(err, leave(r, dir, repo)) }()
	r.step("clone", func() (ev map[string]any, err error) {
		var install []cmdResult
		dir, sha, install, err = clone(root, start, c.Install, repo, r.live)
		return map[string]any{"clone": dir, "sha": sha, "commands": install}, err
	})
	var text string
	r.step("contract", func() (ev map[string]any, err error) {
		text, ev, err = writeBrief(filepath.Join(a.dir, "brief.md"), i, c)
		return ev, err
	})
	byClone.Store(dir, r)
	defer byClone.Delete(dir)
	a.unit(r, root, dir, sha, text, c,
		resumePoint{Issue: args[len(args)-1], Original: &i, BaseRef: *base, Base: sha}, from,
		func(p proof) {
			r.step("ponytail", func() (map[string]any, error) {
				ev, err := a.ponytail(root, dir, sha, []proof{p}, i, c)
				r.Reproofs, _ = ev["reproofs"].([]unitResult)
				return ev, err
			})
		},
		func(proof) { deliver(r, root, dir, sha, *base, c.Delivery, i, summary) })
	return r.err
}

func configureAgent(root, ref, label string, a *agent) (config.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.Config{}, err
	}
	c, _, err := baseConfig(root, ref, home, a.Agent) // the steps after the issue take the config from here
	if err != nil {
		return config.Config{}, err
	}
	a.Agent, a.root, a.dir, a.timeout = c.Agent, root, runDir(root, label), time.Duration(c.AgentTimeout)
	return c, nil
}

// theIssue is the issue of run from, as it first read it, or else arg's, read from the GitHub repository repo
// when a number.
func theIssue(from *resumePoint, arg, repo string) (Issue, error) {
	if from != nil {
		return *from.Original, nil
	}
	return readIssue(arg, repo)
}

func leave(r *verdict, dir, repo string) error {
	if r.err != nil && len(r.Decisions) > 0 {
		return nil
	}
	return release(dir, repo)
}

// parseArgs is atm run's command line: its flags and the issue, in any order. It returns them as flags, then the
// issue, last.
func parseArgs(args []string) (ordered []string, base, resume *string, a *agent, err error) {
	fs := flag.NewFlagSet("atm run", flag.ContinueOnError)
	base = fs.String("base-ref", "main", "the fetched origin branch whose commit the run starts from")
	resume = fs.String("resume", "", "a run that failed after its agent: its issue again, from its agent's work")
	a = &agent{}
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), Usage+"\n\n"+preflightHelp+"\n\nFlags:")
		fs.PrintDefaults()
	}
	fs.BoolVar(&a.follow, "follow", false, "print the run's events as JSON until it ends")
	a.flags(fs)
	// flag stops at the first argument that is not one: the issue, after which the flags go on.
	for err = fs.Parse(args); err == nil && fs.NArg() > 0; err = fs.Parse(fs.Args()[1:]) {
		if ordered != nil {
			return nil, nil, nil, nil, errors.New(Usage)
		}
		i := len(args) - fs.NArg()
		ordered = append(append(args[:i:i], args[i+1:]...), args[i])
	}
	switch {
	case err == nil && ordered == nil && *resume != "": // the issue comes from the run it resumes
		ordered = args
	case err == nil && ordered == nil:
		err = errors.New(Usage)
	}
	return ordered, base, resume, a, err
}

// proof records a unit's base and reported test content for the ponytail test guard.
type proof struct {
	base, test, testContent string
	missing                 bool
}

// unit runs the run's one unit in clone dir, from base commit sha, under config c, on brief text, then its
// checks and the steps after, each given the checks' proof. The unit goes to the report, and its agent's work to
// keep, for atm run --resume. A run that resumes from puts the work it kept back in dir, in place of its unit's
// agent. A failure of the checks or a step after asks the launcher; after a fix turn the checks run again, or
// the delivery alone when it failed.
func (a agent) unit(r *verdict, root, dir, sha, text string, c config.Config, keep resumePoint,
	from *resumePoint, after ...func(proof)) {
	head := sha
	var report map[string]any
	test, resumed := "", ""
	r.step("agent", func() (map[string]any, error) {
		var err error
		var ev map[string]any
		if from != nil {
			resumed = from.run
			head, ev, err = a.resumeUnit(r, root, dir, *from, &keep)
		} else {
			ev, err = a.runUnit(dir, text, &keep)
		}
		ev["unit"] = 1
		report, _ = ev["report"].(map[string]any)
		r.node("agent").Report, _ = ev["report"].(string)
		test, _ = report["test_file"].(string)
		return ev, err
	})
	var p proof
	steps := append([]func(proof){func(proof) {
		r.step("checks", func() (map[string]any, error) {
			var ev map[string]any
			var err error
			p, ev, err = checkUnit(a.dir, dir, head, test, report, c)
			return ev, err
		})
	}}, after...)
	for n := 0; n < len(steps); n++ {
		if r.err == nil {
			if n == 0 {
				r.keep(a.dir, dir, keep, 1, report)
			}
			steps[n](p)
		}
		failed := r.FailedNode
		answer := a.checkDecision(r)
		if answer == "fix" {
			if next := a.fixCheck(r, dir, text, &keep); next != nil {
				report = next
				test, _ = report["test_file"].(string)
			}
			n = -1 // repaired work must pass Checks and Ponytail before delivery
			continue
		}
		if n == 0 {
			r.Units = nil // the checks of the last fix
			r.recordUnit(1, head, test, resumed)
		}
		if answer == "stop" || r.err != nil {
			break
		}
		if failed == "agent" { // approved agent failure still needs Checks
			n = -1
		}
	}
}

func (r *verdict) recordUnit(n int, head, test, resumed string) {
	r.Units = append(r.Units, unitResult{Unit: n, Base: head, TestFile: test,
		Result: outcome(r.err), ResumedFrom: resumed, span: r.node("checks").span,
		AgentModel: r.node("agent").AgentModel})
}

// checkUnit runs configured checks and records the reported test for the ponytail guard.
func checkUnit(runDir, dir, head, test string, report map[string]any, c config.Config) (proof,
	map[string]any, error) {
	ev := map[string]any{"unit": 1}
	missing := report == nil && test == ""
	result, err := checks(dir, runDir, head, test, missing, c)
	for key, value := range result {
		ev[key] = value
	}
	var testContent []byte
	if test != "" {
		var readErr error
		testContent, readErr = readTest(dir, test)
		if err == nil {
			err = readErr
		}
	}
	return proof{head, test, string(testContent), missing}, ev, err
}

func (a agent) runUnit(dir, text string, keep *resumePoint) (map[string]any, error) {
	w := a // its scratch stays out of what keep saves
	ev := map[string]any{}
	_, err := w.reviewScratch("worker") // the one writeBrief names
	if err == nil {
		ev, err = w.run(dir, "worker", "ponytail", text, testFile)
	}
	// A diff never goes for its report's format: without one, the unit goes to Checks with no test file.
	if errors.As(err, &reportError{}) {
		ev["report"], err = "missing", nil
		delete(ev, "exit")
		delete(ev, "tail")
	}
	keep.worker(a, ev)
	return ev, err
}

// deliver runs the delivery command line in clone, ATM_REPORT naming report.json, which is on disk already.
var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// deliver puts the run's work in clone, at base commit sha, on branch atm/<slug>-<timestamp> (onBranch),
// rewrites report.json with its SHA and runs the delivery command line in clone, its output on screen and in
// delivery-output.txt next to report.json. ATM never pushes: that is line's business.
// line has no ceiling: it lasts as long as what it hands the work to. SIGINT and SIGTERM cancel its
// process group. Its exit status is the delivery result.
// ponytail: line gets the clone after the slop detector.
func deliver(r *verdict, root, clone, sha, base, line string, i Issue, screen io.Writer) {
	if line == "" {
		if r.err == nil {
			r.node("delivery").Result = "skipped"
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r.step("delivery", func() (map[string]any, error) {
		branch, cuts, tombs, err := prepareDelivery(r, root, clone, sha, i)
		ev := map[string]any{"branch": branch}
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
		t, _ := screen.(terminal)
		var tail string
		// Delivery owns the attached terminal. Its row carries state and time only:
		// emitting the command's output in a nested check event would duplicate it.
		r.live(map[string]any{"check": "command", "state": "started"})
		took := timed(func() {
			tail, err = tee(ctx, t, io.MultiWriter(f, screen), clone, line, "ATM_TITLE="+i.Title, "ATM_ISSUE="+issue,
				"ATM_ISSUE_TEXT="+i.Text, "ATM_INTENT="+intent(i.Text, tombs), "ATM_BRANCH="+branch,
				"ATM_CLONE="+clone, "ATM_REPORT="+r.path, "ATM_PONYTAIL="+cuts)
		})
		r.live(map[string]any{"check": "command", "state": outcome(err), "duration_ms": took.DurationMS})
		ev["commands"] = []cmdResult{{"delivery", line, outcome(err), tail, took}}
		// Record the delivered branch before deciding whether this run can remove its clone.
		return ev, errors.Join(wrap(err, "delivery"), os.WriteFile(clone+".delivered", []byte(branch+" "+base+"\n"),
			0o644))
	})
}

func prepareDelivery(r *verdict, root, clone, sha string, i Issue) (branch, cuts, tombs string, err error) {
	s := nonSlug.ReplaceAllString(strings.ToLower(i.Title), "-")
	branch = "atm/" + cmp.Or(strings.Trim(s[:min(len(s), 40)], "-"), "run") + "-" +
		strings.TrimPrefix(filepath.Base(clone), "atm-run-") // the clone's timestamp, as unique
	_, err = row(r.live, "commit", func() error {
		cuts, tombs, err = onBranch(root, clone, sha, branch, i)
		if err == nil {
			err = foreign(root, clone, sha)
		}
		if err == nil {
			r.HeadSHA, err = git(clone, "rev-parse", "HEAD")
		}
		return err
	})
	if err == nil {
		err = r.write()
	}
	return branch, cuts, tombs, err
}

// unitMessage uses one conventional prefix for every issue, without classifying it.
func unitMessage(i Issue, n int) string {
	return fmt.Sprintf("chore: %s\n\natm unit %d", i.Title, n)
}

// onBranch puts clone, at base commit sha, on a new branch name, what is left uncommitted committed as the
// unit of issue i under the identity of the repository at root, and points clone's origin at root's. cuts are
// the slop detector's findings, one a line, and tombs the tombstones it committed, when it committed a cut.
func onBranch(root, clone, sha, name string, i Issue) (cuts, tombs string, err error) {
	origin, err := git(root, "remote", "get-url", "origin")
	if err != nil {
		return "", "", fmt.Errorf("no origin to deliver to: %w", err)
	}
	head, err := git(clone, "rev-parse", "HEAD")
	if err == nil && head != sha {
		cuts, err = git(clone, "log", "-1", "--format=%b") // the ponytail commit's body: its findings
	}
	if err == nil && head != sha {
		tombs, err = git(clone, "show", "--name-only", "--format=", "HEAD", "--", ".slop/tombstones/")
	}
	if err == nil {
		_, err = git(clone, "checkout", "-q", "-B", name) // again after a fix
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
				unitMessage(i, n+1))...)
		}
	}
	if err == nil {
		_, err = git(clone, "remote", "set-url", "origin", origin)
	}
	return cuts, tombs, err
}

// intent is the issue text for the delivery's reviewer, followed, when the slop detector committed tombs, one
// path a line, by a section naming them as ATM's own record, outside the issue's scope.
func intent(text, tombs string) string {
	if tombs == "" {
		return text
	}
	return strings.TrimRight(text, "\n") + "\n\n## Files ATM adds\n\n`" + strings.ReplaceAll(tombs, "\n", "`, `") +
		"`: ATM's slop detector records each cut it kept here, as slopslint tombstones. They are not part of the " +
		"issue's scope surface and never a split trigger."
}

// foreign is an error naming the first commit from base commit sha to HEAD in clone whose author or committer
// is not the identity of the repository at root.
func foreign(root, clone, sha string) error {
	log, err := git(clone, "log", "--format=%h %an <%ae>, %cn <%ce>", sha+"..HEAD")
	if err != nil || log == "" {
		return err
	}
	id, err := identity(root)
	if err != nil {
		return err
	}
	v := func(n int) string { _, s, _ := strings.Cut(id[n], "="); return s } // id is -c key=value pairs
	want := fmt.Sprintf("%s <%s>, %s <%s>", v(1), v(3), v(5), v(7))
	for _, line := range strings.Split(log, "\n") {
		if h, who, _ := strings.Cut(line, " "); line != "" && who != want {
			return fmt.Errorf("commit %s is by %s, not the repository's identity %s", h, who, want)
		}
	}
	return nil
}

// GitHubRepo is the owner/name of the GitHub repository origin names, "" when it names none. No origin is not
// an error: the repository just has no GitHub issues.
func GitHubRepo() string {
	url, err := git("", "remote", "get-url", "origin")
	if m := githubURL.FindStringSubmatch(url); err == nil && m != nil {
		return m[1]
	}
	return ""
}

// writeBrief writes at path the brief for issue i under config c. Its scratch is the worker's, next to path, in
// the run's directory.
func writeBrief(path string, i Issue, c config.Config) (string, map[string]any, error) {
	b, err := brief(i, c, filepath.Join(filepath.Dir(path), "worker-scratch"))
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		err = os.WriteFile(path, []byte(b), 0o644)
	}
	return b, map[string]any{"brief": path}, err
}

// runDir is the directory of run label in the repository at root, which nothing removes.
func runDir(root, label string) string { return filepath.Join(root, ".atm", "runs", label) }

// timeout is the default limit for checks commands and the limit for other non-agent, non-delivery commands.
// ponytail: command kills only the command itself; a process group, as sh uses, is the upgrade.
var timeout = 10 * time.Minute

// agentTimeout bounds the agent when .atm.yaml sets no agent_timeout.
var agentTimeout = 30 * time.Minute

// command runs name in dir, "" for the working directory, and returns its trimmed stdout; a failure or a
// timeout carries stderr.
func command(dir, name string, args ...string) (string, error) {
	out, err := commandIn(nil, dir, name, args...)
	return strings.TrimSpace(out), err
}

// commandIn is command with stdin, nil for none, and its stdout as it came.
func commandIn(stdin io.Reader, dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Dir, cmd.Stdin, cmd.Stderr, cmd.WaitDelay = dir, stdin, &stderr, time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", Human(timeout))
	}
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func git(dir string, args ...string) (string, error) {
	return command(dir, "git", args...)
}

func gitRaw(dir string, args ...string) (string, error) {
	return commandIn(nil, dir, "git", args...)
}
