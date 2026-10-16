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
const Usage = "usage: atm run (--watcher id | --follow) [--base-ref r] [--harness h] [--model m] [--effort e] " +
	"[--harness-arg a]... [--env KEY=VALUE]... <issue.md | issue number | --resume run>"

// Run is atm run with args, its flags and the issue: a file, or an issue number of origin's repository.
// Its report, briefs and logs go to .atm/runs/<label>/ (runDir): each node's start and end go to out, one JSON
// object a line, and to report.json there; the summary goes to summary. ExitCode turns its error into atm's
// exit code. With --resume, it picks up the work a failed run kept, from its failed unit's checks.
func Run(label string, args []string, out, summary io.Writer) (err error) {
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
	repo := GitHubRepo()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// The steps after the issue take the config from here.
	c, err := loadConfig(root, home, a.Agent)
	if err != nil {
		return err
	}
	a.Agent, a.root, a.dir, a.timeout = c.Agent, root, runDir(root, label), time.Duration(c.AgentTimeout)
	r := a.verdict(out)
	defer func() { r.resumable(a.dir); err = r.finish(err, summary) }()
	var i Issue
	r.step("issue", func() (map[string]any, error) {
		i, err = theIssue(from, args[len(args)-1], repo)
		r.Type, r.Watcher = i.Type, a.watcher
		ev := map[string]any{"title": i.Title, "type": i.Type}
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
		text, ev, err = writeBrief(filepath.Join(a.dir, "brief.md"), i, c, nil)
		return ev, err
	})
	byClone.Store(dir, r)
	defer byClone.Delete(dir)
	proofs, unit, pending := a.units(r, root, dir, sha, text, i, c,
		resumePoint{Issue: args[len(args)-1], Original: &i, BaseRef: *base, Base: sha}, from)
	if err := saveFollowUps(a.dir, pending); err != nil {
		return err
	}
	r.step("ponytail", func() (map[string]any, error) {
		ev, err := a.ponytail(root, dir, sha, proofs, unit, c)
		r.Reproofs, _ = ev["reproofs"].([]unitResult)
		return ev, err
	})
	a.deliver(r, root, dir, sha, *base, c.Delivery, i, proofs, c, summary)
	return r.err
}

// loadConfig is the run's config, refusing an install that reaches outside the clone before anything
// starts: the brief quotes it to the worker, who runs it in the clone, where the reach guard would kill it.
func loadConfig(root, home string, flags config.Agent) (config.Config, error) {
	c, err := config.Load(root, home, flags)
	if err == nil {
		if reach := InstallReach(c.Install, root); reach != "" {
			err = fmt.Errorf("install reaches outside the clone: %s", reach)
		}
	}
	return c, err
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
	if errors.Is(r.err, errUnmet) {
		closeCloneLease(dir)
		return nil
	}
	return release(dir, repo)
}

// parseArgs is atm run's command line: its flags and the issue, in any order. It returns them as flags, then the
// issue, last.
func parseArgs(args []string) (ordered []string, base, resume *string, a *agent, err error) {
	fs := flag.NewFlagSet("atm run", flag.ContinueOnError)
	base = fs.String("base-ref", "main", "the ref the run starts from, resolved in the repository")
	resume = fs.String("resume", "", "a run that failed after its agent: its issue again, from its agent's work")
	a = &agent{}
	fs.StringVar(&a.watcher, "watcher", "", "the atm watch the run is bound to")
	fs.BoolVar(&a.follow, "follow", false, "be the run's watcher and print its events as JSON until it ends")
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

// proof is how a unit was proven: on its base commit, with its test's content and task type.
type proof struct{ base, test, typ, testContent string }

// units runs the units of the run in clone dir, from base commit sha, for issue i under config c: the first on
// brief text, each next one, up to maxUnits, on the first follow-up on the issue the one before proved. It
// returns each unit's proof, the issue as the last unit is proven, a chained one by its red test, and the
// follow-ups it did not chain. The units after the first change grossBudget lines at most, added plus deleted,
// and a criterion of the issue left unmet blocks the run (unmet). Each unit goes to the report, and its agent's
// work to keep, for atm run --resume. A run that resumes from puts the work it kept back in dir, in place of its
// unit's agent.
func (a agent) units(r *verdict, root, dir, sha, text string, i Issue, c config.Config, keep resumePoint,
	from *resumePoint) (proofs []proof, unit Issue, pending []followUp) {
	unit, head, n := i, sha, 1
	var f *followUp      // the follow-up a chained unit makes pass
	test, spent := "", 0 // spent: the gross lines of the units after the first
	if from != nil {
		n, f, proofs, pending = from.Unit, from.FollowUp, from.proofs(), append([]followUp(nil), from.Pending...)
		spent = from.Gross
		if f != nil {
			unit.Type = "feature"
		}
	}
	for ; ; n++ {
		var report map[string]any
		resumed := ""
		r.step("agent", func() (map[string]any, error) {
			var err error
			var ev map[string]any
			if from != nil {
				resumed, ev = from.run, map[string]any{"report": from.Report, "resumed_from": from.run}
				if err = before(dir); err == nil { // the work it puts back leaves the ignored files out
					head, err = putBack(root, dir, *from)
				}
				from = nil
			} else {
				head, unit, text, ev, err = a.runUnit(root, dir, head, text, unit, f, n, i, c)
			}
			if ev == nil {
				return nil, err
			}
			ev["unit"] = n
			report, _ = ev["report"].(map[string]any)
			test, _ = report["test_file"].(string)
			if f != nil {
				test = f.RedTest
			}
			return ev, err
		})
		r.keep(a.dir, dir, keep, n, report, f, proofs, pending, spent)
		var in []followUp
		var ev map[string]any
		r.step("checks", func() (map[string]any, error) {
			var err error
			var p proof
			var out []followUp
			p, in, out, ev, err = checkUnit(a.dir, dir, head, test, unit.Type, f, n, spent, report, i.Body, c)
			proofs = append(proofs, p)
			pending = append(pending, out...)
			spent, _ = ev["gross_lines"].(int) // none on unit 1, the primary work
			if err == nil {
				pending, in, err = a.scoped(root, dir, head, n, i, pending, in)
				if errors.Is(err, errUnmet) && r.saved != 0 {
					_, bundleErr := git(dir, "bundle", "create", filepath.Join(a.dir, "resume", "work.bundle"),
						"refs/atm/resume", "HEAD", "^"+keep.Base)
					err = errors.Join(err, bundleErr)
				}
			}
			return ev, err
		})
		r.recordUnit(n, head, test, unit.Type, resumed, ev)
		if r.err != nil || len(in) == 0 {
			return proofs, unit, pending
		}
		f, pending = &in[0], append(pending, in[1:]...)
	}
}

func (r *verdict) recordUnit(n int, head, test, typ, resumed string, ev map[string]any) {
	red, _ := ev["red"].(try)
	green, _ := ev["green"].(try)
	proofSpan, _ := ev["proof_span"].(span)
	r.Units = append(r.Units, unitResult{Unit: n, Base: head, TestFile: test, Type: typ,
		Red: red, Green: green, Result: outcome(r.err), ResumedFrom: resumed, span: proofSpan,
		AgentModel: r.node("agent").AgentModel})
}

// checkUnit checks unit n, on commit head in clone dir. A unit after the first that changes a path its follow-up
// f's scope review did not approve, or takes the gross lines of those before it, spent, over grossBudget, fails
// with f as a blocking child: its contract, the unit's diff, the cause and head, its dependency.
func checkUnit(
	runDir, dir, head, test, typ string, f *followUp, n, spent int, report map[string]any, body string,
	c config.Config,
) (proof, []followUp, []followUp, map[string]any, error) {
	ev := map[string]any{"unit": n}
	// A unit that fails its checks still hands over its follow-ups, unverified, a problem with them noted
	// after its checks' reason.
	failed := func(err error, children ...followUp) (proof, []followUp, []followUp, map[string]any, error) {
		out, verdicts, bad := unverified(dir, report)
		ev["follow_ups"] = verdicts
		if len(children) > 0 {
			out = append(children, out...)
		}
		return proof{}, nil, out, ev, errors.Join(err, bad)
	}
	if f != nil {
		if b, err := os.ReadFile(filepath.Join(dir, test)); err != nil || string(b) != f.Test {
			return failed(fmt.Errorf("unit %d edited its red test %s, which it must make pass as it is", n, test))
		}
	}
	if children, err := unitBounds(dir, head, f, n, spent, ev); err != nil {
		return failed(err, children...)
	}
	result, err := checks(dir, runDir, head, typ, test, c)
	for key, value := range result {
		ev[key] = value
	}
	if children, boundErr := unitBounds(dir, head, f, n, spent, ev); boundErr != nil {
		return failed(errors.Join(err, boundErr), children...)
	}
	if err != nil {
		return failed(err)
	}
	var testContent []byte
	if typ == "fix" || typ == "feature" || typ == "greenfield" || typ == "tests" {
		testContent, err = os.ReadFile(filepath.Join(dir, test))
		if err != nil {
			return failed(err)
		}
	}
	p := proof{head, test, typ, string(testContent)}
	in, out, followUpEvent, err := followUps(dir, report, body, c.TestFile)
	ev["follow_ups"] = followUpEvent
	return p, in, out, ev, err
}

func unitBounds(dir, head string, f *followUp, n, spent int, ev map[string]any) ([]followUp, error) {
	if f == nil {
		return nil, nil
	}
	g, diff, cause, err := beyond(dir, head, *f, n, spent)
	if err != nil {
		return nil, err
	}
	ev["gross_lines"] = spent + g
	if cause == "" {
		return nil, nil
	}
	child := *f
	child.Diff, child.Dependency, child.Cause = diff, head, cause
	return []followUp{child}, errors.New(child.Cause)
}

func (a agent) runUnit(
	root, dir, head, text string, unit Issue, f *followUp, n int, i Issue, c config.Config,
) (string, Issue, string, map[string]any, error) {
	name := "worker"
	if f != nil {
		var err error
		head, err = chain(root, dir, n-1, i, *f)
		if err != nil {
			return head, unit, text, nil, err
		}
		unit.Type, name = "feature", "worker-unit"+strconv.Itoa(n)
		path := filepath.Join(a.dir, "brief-unit-"+strconv.Itoa(n)+".md")
		text, _, err = writeBrief(path, unit, c, f)
		if err != nil {
			return head, unit, text, nil, err
		}
	}
	ev, err := a.run(dir, name, "ponytail", text, testFile)
	return head, unit, text, ev, err
}

// deliver runs the delivery command line in clone, ATM_REPORT naming report.json, which is on disk already.
var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// deliver puts the run's work in clone, at base commit sha, on branch atm/<slug>-<timestamp> (onBranch),
// has the agent review that commit against issue i (reviewContract), rewrites report.json with its SHA and, once
// approved, runs the delivery command line in clone, its output on screen and in delivery-output.txt next to
// report.json. ATM never pushes: that is line's business.
// line has no ceiling: it lasts as long as what it hands the work to. SIGINT and SIGTERM cancel its
// process group on Unix or process tree on Windows. A no-mistakes hand-off does not end the run: it follows
// that gate until the PR merges or closes, or a step fails.
// ponytail: line gets the clone after the slop detector.
func (a agent) deliver(r *verdict, root, clone, sha, base, line string, i Issue, history []proof,
	c config.Config, screen io.Writer) {
	if line == "" {
		if r.err == nil {
			r.node("delivery").Result = "skipped"
		}
		return
	}
	var handed string
	var gateDone bool
	var gateErr error
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r.step("delivery", func() (map[string]any, error) {
		branch, cuts, tombs, err := a.prepareDelivery(r, root, clone, sha, i, history, c)
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
		var took span
		// Delivery owns the attached terminal. Its node event is the screen row; emitting
		// a nested check event here would arrive after the terminal output and duplicate it.
		command := func() error {
			var commandErr error
			took, commandErr = row(func(map[string]any) {}, "delivery", func() error {
				tail, commandErr = tee(ctx, t, io.MultiWriter(f, screen), clone, line, "ATM_TITLE="+i.Title, "ATM_ISSUE="+issue,
					"ATM_ISSUE_TEXT="+i.Text, "ATM_INTENT="+intent(i.Text, tombs), "ATM_BRANCH="+branch,
					"ATM_CLONE="+clone, "ATM_REPORT="+r.path, "ATM_PONYTAIL="+cuts)
				return commandErr
			})
			return commandErr
		}
		if strings.Contains(line, "no-mistakes") {
			handed = branch
			gateDone, err, gateErr = r.deliverToGate(ctx, stop, root, clone, branch, command)
		} else {
			err = command()
		}
		ev["commands"] = []cmdResult{{"delivery", line, outcome(err), tail, took}}
		if gateDone {
			err = nil
		}
		// Once it ended: one whose background process died never did, and its clone goes.
		return ev, errors.Join(wrap(err, "delivery"), os.WriteFile(clone+".delivered", []byte(branch+" "+base+"\n"),
			0o644))
	})
	if handed != "" && !gateDone {
		_, gateErr = followGate(ctx, r, root, clone, handed, nil)
	}
	r.err = errors.Join(r.err, gateErr)
	if errors.Is(gateErr, errPRClosed) {
		r.err, r.Reason = gateErr, gateErr.Error()
	}
}

func (r *verdict) deliverToGate(ctx context.Context, stop context.CancelFunc, root, clone, branch string,
	command func() error) (gateDone bool, commandErr, gateErr error) {
	done := make(chan error, 1)
	go func() { done <- command() }()
	gateDone, gateErr = followGate(ctx, r, root, clone, branch, done)
	if gateDone {
		stop()
		commandErr = <-done
	} else {
		commandErr, gateErr = gateErr, nil
	}
	return gateDone, commandErr, gateErr
}

func (a agent) prepareDelivery(r *verdict, root, clone, sha string, i Issue, history []proof,
	c config.Config) (branch, cuts, tombs string, err error) {
	s := nonSlug.ReplaceAllString(strings.ToLower(i.Title), "-")
	branch = "atm/" + cmp.Or(strings.Trim(s[:min(len(s), 40)], "-"), "run") + "-" +
		strings.TrimPrefix(filepath.Base(clone), "atm-run-") // the clone's timestamp, as unique
	cuts, tombs, err = onBranch(root, clone, sha, branch, i)
	if err == nil {
		err = foreign(root, clone, sha)
	}
	if err == nil {
		r.HeadSHA, err = git(clone, "rev-parse", "HEAD")
	}
	if err == nil {
		var repairs repairEvidence
		r.HeadSHA, repairs, err = a.reviewedDelivery(clone, sha, r.HeadSHA, tombs, i, root, history, c)
		for _, attempt := range repairs.Attempts {
			r.Reproofs = attempt.Reproofs
			r.Commands = append(r.Commands, attempt.Commands...)
		}
	}
	if err == nil {
		err = r.write()
	}
	return branch, cuts, tombs, err
}

// conventional maps a task type to its conventional commit type, which release-please reads.
var conventional = map[string]string{"fix": "fix", "feature": "feat", "greenfield": "feat", "refactor": "refactor",
	"tests": "test", "docs": "docs", "chore": "chore"}

// unitMessage is the commit message of unit n of issue i: a conventional commit, a chained unit a feature.
func unitMessage(i Issue, n int) string {
	typ := i.Type
	if n > 1 {
		typ = "feature"
	}
	return fmt.Sprintf("%s: %s\n\natm unit %d", conventional[typ], i.Title, n)
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

// writeBrief writes at path the brief for issue i under config c, whose unit makes follow-up next pass, if any.
func writeBrief(path string, i Issue, c config.Config, next *followUp) (string, map[string]any, error) {
	b, err := brief(i, c, next)
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

// timeout bounds every command a step runs but the delivery command. ponytail: one fixed ceiling, and on
// timeout command kills only the command itself, not what it started, while sh kills its group; a key in
// .atm.yaml and a process group for command are the upgrades.
var timeout = 10 * time.Minute

// agentTimeout bounds the agent when .atm.yaml sets no agent_timeout.
var agentTimeout = 30 * time.Minute

// command runs name in dir, "" for the working directory, and returns its trimmed stdout; a failure or a
// timeout carries stderr.
func command(dir, name string, args ...string) (string, error) {
	return commandFor(dir, dir, name, args...)
}

func commandFor(lockDir, dir, name string, args ...string) (string, error) {
	out, err := commandForRaw(lockDir, dir, name, args...)
	return strings.TrimSpace(out), err
}

func commandForRaw(lockDir, dir, name string, args ...string) (string, error) {
	return commandIn(nil, lockDir, dir, name, args...)
}

// commandIn is commandForRaw with stdin, nil for none.
func commandIn(stdin io.Reader, lockDir, dir, name string, args ...string) (string, error) {
	return commandCtx(context.Background(), stdin, lockDir, dir, name, args...)
}

// commandCtx is commandIn until parent ends.
func commandCtx(parent context.Context, stdin io.Reader, lockDir, dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Dir, cmd.Stdin, cmd.Stderr, cmd.WaitDelay = dir, stdin, &stderr, time.Second
	if err := inheritCloneLease(cmd, lockDir); err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if ctx.Err() != nil && parent.Err() == nil {
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
	return commandForRaw(dir, dir, "git", args...)
}
