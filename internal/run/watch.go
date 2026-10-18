package run

import (
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

var beat, remind = 10 * time.Second, 10 * time.Minute

// gateStatus is `no-mistakes axi status` in clone, until ctx ends: the state of the gate on its branch.
func gateStatus(ctx context.Context, clone string) (string, error) {
	if _, err := os.Stat(clone); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "no-mistakes", "axi", "status")
	cmd.Dir = clone
	out, err := cmd.Output()
	return string(out), err
}

// watchGate hands say the lines of the gate of run label in clone, every beat, until it merged or failed, or the
// run ended without one.
func watchGate(
	ctx context.Context,
	root, clone string,
	branch <-chan string,
	label string,
	ended <-chan struct{},
	say func(label, event string),
) {
	var g gate
	var deliveredBranch string
	t := time.NewTicker(beat)
	defer t.Stop()
	for !g.failedFirst(root, label) {
		select {
		case deliveredBranch = <-branch:
		default:
		}
		status, err := gateStatus(ctx, clone)
		var lines []string
		done := false
		if err == nil {
			lines, done = g.events(status, time.Now())
		} else if errors.Is(err, os.ErrNotExist) && deliveredBranch != "" {
			state, stateErr := gatePRState(root, deliveredBranch)
			if stateErr == nil {
				lines, done = g.prState(state)
			}
		}
		for _, l := range lines {
			say(label, l)
		}
		select {
		case <-ended:
			done = done || err == nil && !g.seen
		default:
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// failedFirst says whether the delivery of run label failed before g saw a gate and before its command, which
// starts the gate, ran: none comes.
func (g *gate) failedFirst(root, label string) bool {
	var report verdict
	b, _ := os.ReadFile(filepath.Join(runDir(root, label), "report.json"))
	return !g.seen && json.Unmarshal(b, &report) == nil && report.FailedNode == "delivery" &&
		!slices.ContainsFunc(report.Commands, func(c cmdResult) bool { return c.Name == "delivery" })
}

func gatePRState(root, branch string) (string, error) {
	return ghPR(context.Background(), root, branch, "--json", "state", "--jq", ".state")
}

// ghPR is `gh pr view pr` with args, of the GitHub repository of root's origin, until ctx ends.
func ghPR(ctx context.Context, root, pr string, args ...string) (string, error) {
	origin, err := git(root, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	repo := githubURL.FindStringSubmatch(origin)
	if repo == nil {
		return "", errors.New("repository has no GitHub origin")
	}
	return commandCtx(ctx, nil, root, root, "gh", append([]string{"pr", "view", pr, "--repo", repo[1]}, args...)...)
}

// prView is what `gh pr view --json url,state,commits` says of a PR.
type prView struct {
	URL, State string
	Commits    []struct{ OID, MessageHeadline string }
}

// ci is p's CI repair commits, those whose subject starts `no-mistakes(ci):`, in order.
func (p prView) ci() (cs []ciCommit) {
	for _, c := range p.Commits {
		if strings.HasPrefix(c.MessageHeadline, "no-mistakes(ci):") {
			cs = append(cs, ciCommit{c.OID, c.MessageHeadline})
		}
	}
	return cs
}

// gate is what a watcher printed of a gate.
type gate struct {
	seen                          bool // a step of it
	pr, waiting                   string
	waited, told                  time.Time
	fixing, green, merged, failed bool
}

// gateState is what `no-mistakes axi status` says of a gate: its PR, the first step that waits for approval or a
// decision and the first that failed, and whether it has steps, CI is being fixed or green, and the PR merged;
// read, also the findings of its gate and its PR's CI repair commits, or the error that kept them unread.
type gateState struct {
	pr, waiting, failed, status, reason string // reason is the top-level error: line, unquoted
	seen, fixing, green, merged, closed bool
	hasPRState                          bool
	findings                            []gateFinding
	ci                                  []ciCommit
	steps                               []gateStep
	err                                 error
}

// gateStep is one row of a gate's steps: its name, no-mistakes status and duration.
type gateStep struct {
	name, status string
	ms           int64
}

// gateNodes are the no-mistakes steps a run keeps below delivery, in that order.
var gateNodes = []string{"review", "test", "document", "lint", "push", "pr", "ci"}

func gateStepName(name string) bool { return slices.Contains(gateNodes, name) }

// gateFinding is a finding of a gate as no-mistakes reports it.
type gateFinding struct{ id, severity, file, description string }

// ciCommit is a commit no-mistakes pushed to repair CI: its full SHA and subject.
type ciCommit struct{ sha, subject string }

// findingsTable is the header of the gate's findings in `no-mistakes axi status`, whose rows are CSV.
const findingsTable = "{id,severity,file,action,description}:"

func parseGate(status string) (s gateState) {
	expectedFindings := 0
	parked := false // its fix_review step waits for a decision
	table, depth := "", 0
	for _, l := range strings.Split(status, "\n") {
		trimmed := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		flatStep := table == "steps" && indent == depth && strings.Contains(trimmed, ",") &&
			!strings.Contains(trimmed, ":")
		if table != "" && (indent > depth || flatStep) {
			if table == "steps" {
				s.step(trimmed, parked)
			} else {
				s.finding(trimmed)
			}
			continue
		}
		key, value, isKey := strings.Cut(trimmed, ": ")
		table, depth = "", indent
		switch {
		case strings.HasPrefix(trimmed, "steps["):
			table = "steps"
		case strings.HasPrefix(trimmed, "findings["):
			table = "findings"
			expectedFindings += s.findingCount(trimmed)
		case isKey:
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			}
			parked = s.field(key, value, indent, parked)
		}
	}
	if len(s.findings) != expectedFindings {
		s.err = cmp.Or(s.err, fmt.Errorf("findings has %d rows, want %d", len(s.findings), expectedFindings))
	}
	return s
}

func (s *gateState) field(key, value string, indent int, parked bool) bool {
	switch key {
	case "status":
		s.status = value
	case "pr":
		s.pr = value
	case "pr_state":
		s.hasPRState = true
		s.merged, s.closed = value == "merged", value == "closed"
	case "awaiting_agent":
		return strings.HasPrefix(value, "parked")
	case "error":
		if indent == 0 {
			s.reason = value
		}
	}
	return parked
}

func (s *gateState) findingCount(header string) int {
	count, valid := strings.CutSuffix(strings.TrimPrefix(header, "findings["), "]"+findingsTable)
	n, err := strconv.Atoi(count)
	if !valid || err != nil || n < 0 {
		s.err = cmp.Or(s.err, fmt.Errorf("findings are not %s: %q", findingsTable, header))
		return 0
	}
	return n
}

// finding adds to s a row of its gate's findings, `id,severity,file,action,description`, quoted where CSV quotes.
func (s *gateState) finding(row string) {
	f, err := csv.NewReader(strings.NewReader(row)).Read()
	if err == nil && len(f) != 5 {
		err = fmt.Errorf("finding %q has %d fields, want 5", row, len(f))
	}
	if err != nil {
		s.err = cmp.Or(s.err, err)
		return
	}
	s.findings = append(s.findings, gateFinding{f[0], f[1], f[2], f[4]})
}

// Read reads the gate of clone's branch through `no-mistakes axi status` and
// `gh pr view`: its state, the findings of the step it waits at, if one does, and its PR's CI repair commits,
// those whose subject starts `no-mistakes(ci):`, in order. It changes nothing; nor s, when it fails.
func (s *gateState) Read(clone string) error {
	var r gateState
	if err := r.read(context.Background(), clone); err != nil {
		return err
	}
	*s = r
	return nil
}

// errNoGate is a gate read whose status has no gate.
var errNoGate = errors.New("no-mistakes axi status has no gate")

// read retains parsed gate state even if the later commit read fails, so an observed gate failure
// cannot be lost to a transient gh error. Read wraps this with all-or-nothing updates for other callers.
func (s *gateState) read(ctx context.Context, clone string) error {
	status, err := gateStatus(ctx, clone)
	if err != nil {
		return err
	}
	r := parseGate(status)
	switch {
	case r.err != nil:
		return r.err
	case !r.seen:
		return errNoGate
	case r.waiting == "":
		r.findings = nil
	}
	*s = r
	if strings.HasPrefix(r.pr, "http") {
		var pr prView
		out, err := commandCtx(ctx, nil, clone, clone, "gh", "pr", "view", r.pr, "--json", "commits")
		if err == nil {
			err = json.Unmarshal([]byte(out), &pr)
		}
		if err != nil {
			return err
		}
		s.ci = pr.ci()
	}
	return nil
}

// step adds to s a row of the gate's steps, `step,status,...`, of a run parked or not.
func (s *gateState) step(row string, parked bool) {
	step, rest, _ := strings.Cut(row, ",")
	state, rest, _ := strings.Cut(rest, ",")
	_, dur, _ := strings.Cut(rest, ",")
	ms, _ := strconv.ParseInt(dur, 10, 64)
	s.steps = append(s.steps, gateStep{step, state, ms})
	s.seen = true
	switch {
	case state == "awaiting_approval" || parked && state == "fix_review":
		s.waiting = cmp.Or(s.waiting, step)
	case state == "failed":
		s.failed = cmp.Or(s.failed, step)
	case step == "ci":
		s.fixing, s.green = state == "fixing", state == "completed"
	}
}

// events is the lines status, the gate's `no-mistakes axi status`, adds at now to those g printed; done once the
// gate merged or failed.
func (g *gate) events(status string, now time.Time) (lines []string, done bool) {
	s := parseGate(status)
	if !s.seen { // no gate, or no answer: nothing changed
		return nil, g.merged || g.failed
	}
	g.seen = true
	if strings.HasPrefix(s.pr, "http") && s.pr != g.pr {
		g.pr = s.pr
		lines = append(lines, "pr "+s.pr)
	}
	edge := func(was *bool, is bool, line string) {
		if is && !*was {
			lines = append(lines, line)
		}
		*was = is
	}
	edge(&g.fixing, s.fixing, "gate fixing CI")
	edge(&g.green, s.green, "ci green")
	edge(&g.merged, s.merged, "merged")
	edge(&g.failed, s.failed != "", "gate failed: "+s.failed)
	switch {
	case s.waiting == "":
		g.waiting = ""
	case s.waiting != g.waiting:
		g.waiting, g.waited, g.told = s.waiting, now, now
		lines = append(lines, "gate waiting: "+s.waiting)
	case now.Sub(g.told) >= remind:
		g.told = now
		lines = append(lines, fmt.Sprintf("still waiting %dm: %s", int(now.Sub(g.waited).Minutes()), s.waiting))
	}
	return lines, g.merged || g.failed
}

func (g *gate) prState(state string) (lines []string, done bool) {
	switch strings.TrimSpace(state) {
	case "MERGED":
		if !g.merged {
			g.merged = true
			lines = append(lines, "merged")
		}
		return lines, true
	case "CLOSED":
		if !g.failed {
			g.failed = true
			lines = append(lines, "gate failed: pr")
		}
		return lines, true
	default:
		return nil, false
	}
}

// followGate polls the gate of clone every beat until the PR merges or closes, or a step fails.
// ponytail: one beat-wide poll; a push model if status grows a watch.
func followGate(ctx context.Context, r *verdict, root, clone, branch string, command <-chan error) (bool, error) {
	t := time.NewTicker(beat)
	defer t.Stop()
	for {
		done, err := applyGate(ctx, r, root, clone, branch)
		if errors.Is(err, errNoGate) {
			err = nil
		}
		if done || err != nil {
			return true, err
		}
		// A delivery that failed before the gate showed a step started no gate: nothing left to follow.
		if command == nil && r.err != nil &&
			!slices.ContainsFunc(r.Nodes, func(n *node) bool { return gateStepName(n.Name) }) {
			return true, nil
		}
		select {
		case <-ctx.Done(): // cancelled, not finished: the delivery fails as the command would
			return false, ctx.Err()
		case err := <-command:
			return false, err
		case <-t.C:
		}
	}
}

func applyGate(ctx context.Context, r *verdict, root, clone, branch string) (bool, error) {
	status, err := gateStatus(ctx, clone)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return prEnded(ctx, root, branch)
	case errors.Is(err, exec.ErrNotFound):
		return false, errNoGate
	case err != nil:
		return false, nil
	}
	g := parseGate(status)
	if g.err != nil {
		return false, nil
	}
	if !g.seen {
		return false, errNoGate
	}
	r.noteGate(g)
	done, err := g.outcome(r)
	// A completed gate with skipped CI may never publish pr_state; follow its PR directly.
	if !done && err == nil && g.status == "completed" && !g.hasPRState &&
		slices.ContainsFunc(g.steps, func(s gateStep) bool { return s.name == "ci" && s.status == "skipped" }) {
		return prEnded(ctx, root, branch)
	}
	return done, err
}

func (g gateState) outcome(r *verdict) (bool, error) {
	switch {
	case g.failed != "":
		r.FailedNode = g.failed
		r.Reason = cmp.Or(g.reason, "no-mistakes step "+g.failed+" failed")
		r.gateNode(g.failed, "failed", r.ensureNode(g.failed).DurationMS, r.Reason) // a no-op when already failed
		return false, errors.New(r.Reason)
	case g.closed:
		return true, errPRClosed
	case g.merged:
		return true, nil
	default:
		return false, nil
	}
}

var errPRClosed = errors.New("PR closed")

func prEnded(ctx context.Context, root, branch string) (bool, error) {
	if branch == "" {
		return false, nil
	}
	state, err := ghPR(ctx, root, branch, "--json", "state", "--jq", ".state")
	if err != nil {
		return false, nil
	}
	switch strings.TrimSpace(state) {
	case "MERGED":
		return true, nil
	case "CLOSED":
		return true, errPRClosed
	default:
		return false, nil
	}
}

func (r *verdict) noteGate(g gateState) {
	got := map[string]gateStep{}
	for _, s := range g.steps {
		got[s.name] = s
	}
	for _, name := range gateNodes {
		s, ok := got[name]
		if !ok {
			continue
		}
		why := ""
		if s.status == "failed" {
			why = "no-mistakes step " + name + " failed"
		}
		r.gateNode(name, gateResult(name, s.status, g), s.ms, why)
	}
}

func gateResult(name, status string, g gateState) string {
	switch {
	case name == g.waiting:
		return "waiting"
	case status == "completed":
		return "passed"
	case status == "failed" || status == "pending" || status == "skipped":
		return status
	}
	return "running"
}
