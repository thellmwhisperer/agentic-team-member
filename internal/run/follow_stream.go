package run

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"
)

// Follows says whether args, atm run's command line, ask for --follow.
func Follows(args []string) bool {
	_, _, _, a, err := parseArgs(args)
	return err == nil && a.follow
}

// StartFollowing is atm run --follow with args: this process takes a lease, refreshed each beat, has the
// background process of the repository at root run args bound to it, and streams that run on w until it ends,
// then drops the lease. Its error is nil on merge, exit code 1 when the run did not merge, else that no run started.
// Ending ctx leaves the run going.
func StartFollowing(ctx context.Context, root string, args []string, w io.Writer, open func(string) string) error {
	ordered, _, resume, a, err := parseArgs(args)
	switch {
	case err != nil:
		return err
	case *resume != "":
		return errors.New("--follow does not resume: follow the run again with atm watch --adopt <run>")
	case a.watcher != "":
		return errors.New("--follow makes this process the run's watcher: drop --watcher")
	}
	id, l, err := newLease(root)
	if err != nil {
		return err
	}
	stopBeating := make(chan struct{})
	beating := make(chan struct{})
	go func() {
		defer close(beating)
		t := time.NewTicker(beat)
		defer t.Stop()
		for {
			select {
			case <-stopBeating:
				return
			case <-t.C:
				l.Heartbeat = time.Now()
				_ = l.write(root, id) // a missed beat is caught up at the next
			}
		}
	}()
	defer func() { close(stopBeating); <-beating; _ = os.Remove(leasePath(root, id)) }()
	pane := open(ordered[len(ordered)-1])
	o, err := start(root, append([]string{"--watcher", id}, args...), pane)
	if err != nil {
		return err
	}
	if o.Stream(ctx, root, w, time.Now, time.After) != 0 {
		return exitError{1, errors.New(o.Run + " ended without a merge")}
	}
	return nil
}

// Stream observes existing run o.Run in root, writing each event immediately as one JSON line on w
// with time, run, issue and event. It returns 0 on merge, or 1 on closure or run/gate failure.
// An observed gate failure takes precedence over a later PR result in the same poll.
// Steps and delivery come from run frames; waits and green CI from `no-mistakes axi status` in the clone;
// No-mistakes commits and PR state from gh. The delivery branch locates the PR if the clone is gone.
// Observation continues through gate decisions and after the delivery command returns.
// Polls start at least 10 seconds apart. Pass time.Now and time.After for now and after outside tests.
// Each hour without another event emits stalled, with minutes since the last non-stalled event.
// Read errors go to stderr and are retried; they do not erase successfully observed gate state.
// Ending ctx stops only the observer and returns 1, leaving the run going.
func (o Outcome) Stream(
	ctx context.Context,
	root string,
	w io.Writer,
	now func() time.Time,
	after func(time.Duration) <-chan time.Time,
) int {
	ctx, cancel := context.WithCancel(ctx)
	frames, ended, in, followed := make(chan Frame), make(chan Outcome, 1), make(chan Input), make(chan struct{})
	go func() {
		defer close(followed)
		end, err := Follow(root, o.Run, nil, in, func(f Frame) {
			select {
			case frames <- f:
			case <-ctx.Done():
			}
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "atm:", o.Run+":", err)
		}
		ended <- end
	}()
	defer func() { cancel(); close(in); <-followed }() // closing in leaves the run's screen
	s := &observer{o: o, w: w, now: now, said: now(), quiet: now(), ci: map[string]bool{}}
	var polled time.Time
	for {
		if t := now(); !t.Before(polled.Add(10 * time.Second)) {
			polled = t
			if code, done := s.poll(ctx, root); done {
				return code
			}
		}
		if t := now(); t.Sub(s.said) >= time.Hour {
			s.say("stalled", map[string]any{"minutes": int(t.Sub(s.quiet).Minutes())})
		}
		select {
		case <-ctx.Done():
			return 1
		case f := <-frames:
			s.frame(f)
		case end := <-ended:
			if code, done := s.end(root, end); done {
				return code
			}
		case <-after(polled.Add(10 * time.Second).Sub(now())):
		}
	}
}

// observer is what Stream has seen of a run and said of it: when it last wrote a line, and one but stalled.
type observer struct {
	o                          Outcome
	w                          io.Writer
	now                        func() time.Time
	said, quiet                time.Time
	clone, branch, pr, waiting string
	delivering, seen, green    bool
	reason                     string // the run's, failed at delivery after its command ran: its gate may go on
	ci                         map[string]bool
}

// say writes event, at now, with fields.
func (s *observer) say(event string, fields map[string]any) {
	t := s.now()
	line := map[string]any{"time": t.UTC().Format(time.RFC3339Nano), "run": s.o.Run, "issue": s.o.Issue,
		"event": event}
	maps.Copy(line, fields)
	b, _ := json.Marshal(line)
	_, _ = s.w.Write(append(b, '\n'))
	s.said = t
	if event != "stalled" {
		s.quiet = t
	}
}

func (s *observer) diag(err error) { fmt.Fprintln(os.Stderr, "atm:", s.o.Run+":", err) }

// frame takes a frame of the run's screen: each node's start is a step, delivery's its delivery.
func (s *observer) frame(f Frame) {
	var ev struct{ Step, State, Clone, Branch string }
	if f.Line == nil || json.Unmarshal(f.Event, &ev) != nil { // a node's start or end has a line
		return
	}
	s.clone, s.branch = cmp.Or(ev.Clone, s.clone), cmp.Or(ev.Branch, s.branch)
	if ev.State != "started" {
		return
	}
	s.say("step", map[string]any{"step": ev.Step})
	if ev.Step == "delivery" {
		s.delivering = true
		s.say("delivery", map[string]any{"clone": s.clone})
	}
}

// end takes how the run ended. One that failed at delivery after its delivery command ran may leave its gate
// going, which says how it ends.
func (s *observer) end(root string, o Outcome) (int, bool) {
	switch {
	case o.Ended.IsZero(): // its screen is lost; its gate is not
		return 0, false
	case o.Reason == errPRClosed.Error():
		s.say("closed", map[string]any{"url": s.pr})
		return 1, true
	case o.Outcome == "passed" && s.delivering:
		return 0, false
	case o.Outcome == "passed":
		s.diag(errors.New("the run delivered nothing: no PR to follow"))
		return 1, true
	case o.FailedNode == "delivery" && !(&gate{seen: s.seen}).failedFirst(root, o.Run):
		s.reason = o.Reason
		return 0, false
	}
	s.say("run_failed", map[string]any{"step": cmp.Or(o.FailedNode, o.Step), "reason": o.Reason})
	return 1, true
}

// poll reads the gate, once the run delivers, and its PR, once there is one or the gate cannot be read.
func (s *observer) poll(ctx context.Context, root string) (int, bool) {
	if !s.delivering {
		return 0, false
	}
	var g gateState
	err := g.read(ctx, s.clone)
	if errors.Is(err, errNoGate) && s.reason != "" && !s.seen { // the delivery failed and started none
		s.say("run_failed", map[string]any{"step": "delivery", "reason": s.reason})
		return 1, true
	}
	if err != nil {
		s.diag(err)
	}
	if g.seen {
		s.seen = true
		s.gate(g)
	}
	var prState string
	if err != nil || s.pr != "" {
		prState = s.prState(ctx, root)
	}
	if g.seen {
		if g.green && !s.green {
			s.say("ci_green", map[string]any{"url": s.pr})
		}
		s.green = g.green
	}
	if g.failed != "" {
		s.say("run_failed", map[string]any{"step": "delivery",
			"reason": cmp.Or(s.reason, g.reason, "no-mistakes step "+g.failed+" failed")})
		return 1, true
	}
	switch prState {
	case "MERGED":
		s.say("merged", map[string]any{"url": s.pr})
		return 0, true
	case "CLOSED":
		s.say("closed", map[string]any{"url": s.pr})
		return 1, true
	}
	return 0, false
}

// gate takes a gate's state: a new wait and the PR.
func (s *observer) gate(g gateState) {
	switch {
	case g.waiting == "":
		s.waiting = ""
	case g.waiting != s.waiting:
		s.waiting = g.waiting
		findings := []map[string]string{}
		for _, f := range g.findings {
			findings = append(findings, map[string]string{"id": f.id, "severity": f.severity, "file": f.file,
				"description": f.description})
		}
		s.say("gate_waiting", map[string]any{"step": g.waiting, "clone": s.clone, "findings": findings})
	}
	if strings.HasPrefix(g.pr, "http") {
		s.opened(g.pr)
	}
}

// prState reads the PR, by its URL or the delivery's branch, through gh: its no-mistakes commits, then its end.
func (s *observer) prState(ctx context.Context, root string) string {
	ref := cmp.Or(s.pr, s.branch)
	if ref == "" {
		return ""
	}
	var pr prView
	out, err := ghPR(ctx, root, ref, "--json", "url,state,commits")
	if err == nil {
		err = json.Unmarshal([]byte(out), &pr)
	}
	if err != nil {
		s.diag(err)
		return ""
	}
	s.opened(pr.URL)
	s.commits(pr)
	return pr.State
}

func (s *observer) opened(url string) {
	if url != "" && s.pr == "" {
		s.pr = url
		s.say("pr_opened", map[string]any{"url": url})
	}
}

func (s *observer) commits(pr prView) {
	for _, c := range pr.Commits {
		if rest, ok := strings.CutPrefix(c.MessageHeadline, "no-mistakes("); ok && !s.ci[c.OID] {
			step, _, _ := strings.Cut(rest, ")")
			s.ci[c.OID] = true
			s.say("gate_fixing_ci", map[string]any{"commit": map[string]string{
				"sha": c.OID, "subject": c.MessageHeadline, "step": step,
			}})
		}
	}
}
