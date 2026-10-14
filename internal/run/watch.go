package run

import (
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A watcher, atm watch, prints one line per event of the runs bound to it, atm run --watcher <id>, until it is
// killed: a session reads them under a monitor that wakes it on each line. While it lives, its lease,
// .atm/watch/<id>.json, says so.

// beat is how often a watcher rewrites its lease and looks for runs and gates; a lease older than stale is a dead
// watcher's; a gate that keeps waiting is printed again every remind.
var beat, stale, remind = 10 * time.Second, 30 * time.Second, 10 * time.Minute

var errNoWatcher = errors.New(
	"no watcher: start atm watch in the session that launches this run and pass --watcher <id>")

type lease struct {
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Heartbeat time.Time `json:"heartbeat"`
}

func leasePath(root, id string) string { return filepath.Join(root, ".atm", "watch", id+".json") }

// write puts the lease of watcher id in the repository at root, through a file renamed over it, never half.
func (l lease) write(root, id string) error {
	b, _ := json.Marshal(l)
	path := leasePath(root, id)
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err == nil {
		err = os.WriteFile(path+".tmp", b, 0o644)
	}
	if err == nil {
		err = os.Rename(path+".tmp", path)
	}
	return err
}

// Watching says whether watcher id of the repository at root lives: its process runs and its heartbeat is fresh.
func Watching(root, id string) bool {
	var l lease
	b, err := os.ReadFile(leasePath(root, id))
	// On Windows a read fails for a moment while the watcher renames its new lease over the file: read again.
	// ponytail: 10 tries 10 ms apart, so a lease really gone costs 100 ms there.
	for i := 0; err != nil && runtime.GOOS == "windows" && i < 10; i++ {
		time.Sleep(10 * time.Millisecond)
		b, err = os.ReadFile(leasePath(root, id))
	}
	return err == nil && id == filepath.Base(id) && json.Unmarshal(b, &l) == nil && pidAlive(l.PID) &&
		time.Since(l.Heartbeat) < stale
}

// Watchers is the live watchers of the repository at root.
func Watchers(root string) (ids []string) {
	paths, _ := filepath.Glob(leasePath(root, "*"))
	for _, p := range paths {
		if id := strings.TrimSuffix(filepath.Base(p), ".json"); Watching(root, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// Watch is atm watch on the repository at root until ctx ends: `watching as <id>`, then, after adopt's state when
// it takes run adopt over, a line per event of each run bound to it, its time and the run first, and of its
// no-mistakes gate once it reaches delivery.
// ponytail: it asks for the runs every beat, which keeps the background process from ending idle.
func Watch(ctx context.Context, root, adopt string, w io.Writer) error {
	since := time.Now()
	id := strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(since.Unix(), 36)
	l := lease{os.Getpid(), since, since}
	if err := l.write(root, id); err != nil {
		return err
	}
	defer func() { _ = os.Remove(leasePath(root, id)) }()
	lines := make(chan string)
	say := func(label, event string) {
		select {
		case lines <- stamp(label, event):
		case <-ctx.Done():
		}
	}
	if _, err := fmt.Fprintln(w, "watching as", id); err != nil {
		return err
	}
	if err := adoptRun(root, adopt, id, w); err != nil {
		return err
	}
	followed := map[string]bool{}
	t := time.NewTicker(beat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case line := <-lines:
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err // no one reads it: its runs learn it is gone
			}
		case <-t.C:
			l.Heartbeat = time.Now()
			_ = l.write(root, id) // a missed beat is caught up at the next
			runs, _ := Runs(root)
			for _, o := range runs {
				if o.Watcher == id && !followed[o.Run] {
					followed[o.Run] = true
					go follow(ctx, root, o.Run, since, say)
				}
			}
		}
	}
}

// stamp is a watcher's line: the time, run label and event.
func stamp(label, event string) string {
	return time.Now().Format(time.TimeOnly) + " " + label + " " + event
}

// adoptRun binds run label, if any, to watcher id and prints its state on w.
func adoptRun(root, label, id string, w io.Writer) error {
	if label == "" {
		return nil
	}
	var o Outcome
	err := call(root, request{Cmd: "adopt", Run: label, Watcher: id}, nil, func(r reply) { o = *r.Run })
	if err == nil {
		_, err = fmt.Fprintln(w, stamp(o.Run, "adopted: "+o.Outcome+at(cmp.Or(o.FailedNode, o.Step))))
	}
	return err
}

// follow hands say each step run label starts after since, then how it finished, and its gate's lines.
func follow(ctx context.Context, root, label string, since time.Time, say func(label, event string)) {
	var clone string
	branch := make(chan string, 1)
	ended := make(chan struct{})
	o, _ := Follow(root, label, nil, nil, func(f Frame) {
		var ev struct{ Step, State, Clone, TS, Branch string }
		if f.Line == nil || json.Unmarshal(f.Event, &ev) != nil { // a node's start or end has a line
			return
		}
		clone = cmp.Or(ev.Clone, clone)
		if ev.Branch != "" {
			select {
			case branch <- ev.Branch:
			default:
			}
		}
		if ev.State != "started" {
			return
		}
		if t, _ := time.Parse(time.RFC3339Nano, ev.TS); !t.Before(since) { // an adopted run's past is its state
			say(label, strings.Replace(ev.Step, "issue", "started", 1))
		}
		if ev.Step == "delivery" {
			go watchGate(ctx, root, clone, branch, label, ended, say)
		}
	})
	close(ended)
	switch {
	case o.Ended.Before(since):
	case o.Outcome == "passed":
		say(label, "finished: passed")
	default:
		failed := o.FailedNode
		if failed == "delivery" {
			failed = "Delivery"
		}
		say(label, "finished: failed"+at(failed))
	}
}

// at is " at step", or "" for no step.
func at(step string) string { return strings.TrimSuffix(" at "+step, " at ") }

// gateStatus is `no-mistakes axi status` in clone: the state of the gate on its branch.
func gateStatus(clone string) (string, error) {
	if _, err := os.Stat(clone); err != nil {
		return "", err
	}
	cmd := exec.Command("no-mistakes", "axi", "status")
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
		status, err := gateStatus(clone)
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
	origin, err := git(root, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	repo := githubURL.FindStringSubmatch(origin)
	if repo == nil {
		return "", errors.New("repository has no GitHub origin")
	}
	return command(root, "gh", "pr", "view", branch, "--repo", repo[1], "--json", "state", "--jq", ".state")
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
	pr, waiting, failed         string
	seen, fixing, green, merged bool
	findings                    []gateFinding
	ci                          []ciCommit
	err                         error
}

// gateFinding is a finding of a gate as no-mistakes reports it.
type gateFinding struct{ id, severity, file, description string }

// ciCommit is a commit no-mistakes pushed to repair CI: its full SHA and subject.
type ciCommit struct{ sha, subject string }

// findingsTable is the header of the gate's findings in `no-mistakes axi status`, whose rows are CSV.
const findingsTable = "{id,severity,file,action,description}:"

func parseGate(status string) (s gateState) {
	expectedFindings := 0
	parked := false       // its fix_review step waits for a decision
	table, depth := "", 0 // the table whose rows, deeper than its header, follow
	for _, l := range strings.Split(status, "\n") {
		trimmed := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if table != "" && indent > depth {
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
			count, valid := strings.CutSuffix(strings.TrimPrefix(trimmed, "findings["), "]"+findingsTable)
			n, err := strconv.Atoi(count)
			if !valid || err != nil || n < 0 {
				s.err = cmp.Or(s.err, fmt.Errorf("findings are not %s: %q", findingsTable, trimmed))
			} else {
				expectedFindings += n
			}
		case isKey:
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			}
			switch key {
			case "pr":
				s.pr = value
			case "pr_state":
				s.merged = value == "merged"
			case "awaiting_agent":
				parked = strings.HasPrefix(value, "parked")
			}
		}
	}
	if len(s.findings) != expectedFindings {
		s.err = cmp.Or(s.err, fmt.Errorf("findings has %d rows, want %d", len(s.findings), expectedFindings))
	}
	return s
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

// Read is what the follow stream reads of the gate of clone's branch, through `no-mistakes axi status` and
// `gh pr view`: its state, the findings of the step it waits at, if one does, and its PR's CI repair commits,
// those whose subject starts `no-mistakes(ci):`, in order. It changes nothing; nor s, when it fails.
func (s *gateState) Read(clone string) error {
	status, err := gateStatus(clone)
	if err != nil {
		return err
	}
	r := parseGate(status)
	switch {
	case r.err != nil:
		return r.err
	case !r.seen:
		return errors.New("no-mistakes axi status has no gate")
	case r.waiting == "":
		r.findings = nil
	}
	if strings.HasPrefix(r.pr, "http") {
		var pr struct {
			Commits []struct{ OID, MessageHeadline string }
		}
		out, err := command(clone, "gh", "pr", "view", r.pr, "--json", "commits")
		if err == nil {
			err = json.Unmarshal([]byte(out), &pr)
		}
		if err != nil {
			return err
		}
		for _, c := range pr.Commits {
			if strings.HasPrefix(c.MessageHeadline, "no-mistakes(ci):") {
				r.ci = append(r.ci, ciCommit{c.OID, c.MessageHeadline})
			}
		}
	}
	*s = r
	return nil
}

// step adds to s a row of the gate's steps, `step,status,...`, of a run parked or not.
func (s *gateState) step(row string, parked bool) {
	step, rest, _ := strings.Cut(row, ",")
	state, _, _ := strings.Cut(rest, ",")
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
